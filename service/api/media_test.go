package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/macro-markets/whatsqr/media"
)

type cleanMediaScanner struct{}

func (cleanMediaScanner) Scan(_ context.Context, r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	return err
}

func TestAttachmentAPIAuthIsolationRangeAndEtag(t *testing.T) {
	s, err := media.New(filepath.Join(t.TempDir(), "attachments"), cleanMediaScanner{})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	data := b.Bytes()
	sum := sha256.Sum256(data)
	id, err := s.Put("one", "external-message", "sender", media.Record{Type: "image", Size: uint64(len(data)), Descriptor: []byte("private"), SHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	f, _, _, err := s.Open(context.Background(), "one", id, func(_ context.Context, _ media.Record, out *media.LimitedFile) error {
		_, err := out.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	h := NewServer(Options{Token: "private-token", Media: s, HasSecret: func(id string) bool { return id == "one" || id == "two" }}).Handler()
	call := func(url, token, rangeHeader, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, url, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if rangeHeader != "" {
			r.Header.Set("Range", rangeHeader)
		}
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	url := "/sessions/one/media/" + id
	if w := call(url, "", "", ""); w.Code != 401 {
		t.Fatal("public media")
	}
	if w := call("/sessions/two/media/"+id, "private-token", "", ""); w.Code != 404 {
		t.Fatal("cross-session media")
	}
	w := call(url, "private-token", "", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Body.Len() != len(data) || w.Header().Get("Cache-Control") != "private, max-age=300" {
		t.Fatalf("bad media response %d %v", w.Code, w.Header())
	}
	w = call(url, "private-token", "bytes=0-7", "")
	if w.Code != 206 || w.Body.Len() != 8 || w.Header().Get("Content-Range") == "" {
		t.Fatalf("range failed %d", w.Code)
	}
	w = call(url, "private-token", "bytes=999999-", "")
	if w.Code != 416 {
		t.Fatal("invalid range accepted")
	}
	if w = call(url, "private-token", "bytes=0-3,5-8", ""); w.Code != 416 {
		t.Fatal("multipart range allowed")
	}
	w = call(url, "private-token", "", "\""+id+"\"")
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Fatal("etag not honored")
	}
}
