package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testPNG() []byte {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	return b.Bytes()
}

type cleanScanner struct{}

func (cleanScanner) Scan(_ context.Context, r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	return err
}
func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "private"), cleanScanner{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func testRecord(data []byte, kind string) Record {
	sum := sha256.Sum256(data)
	return Record{Type: kind, Size: uint64(len(data)), Descriptor: []byte("private crypto metadata"), SHA256: hex.EncodeToString(sum[:])}
}
func storeID(t *testing.T, s *Store, data []byte, kind string) string {
	t.Helper()
	id, err := s.Put("one", "message", "sender", testRecord(data, kind))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAttachmentStoreIsPrivatePersistentAndIsolated(t *testing.T) {
	s := testStore(t)
	data := testPNG()
	id := storeID(t, s, data, "image")
	f, _, mime, err := s.Open(context.Background(), "one", id, func(_ context.Context, _ Record, out *LimitedFile) error { _, err := out.Write(data); return err })
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	info, _ := f.Stat()
	f.Close()
	if !bytes.Equal(got, data) || mime != "image/png" || info.Mode().Perm() != 0600 {
		t.Fatalf("bad attachment %s %v", mime, info.Mode())
	}
	root, _ := os.Stat(s.root)
	if root.Mode().Perm() != 0700 {
		t.Fatal("public media directory")
	}
	restarted, err := New(s.root, cleanScanner{})
	if err != nil {
		t.Fatal(err)
	}
	f, _, _, err = restarted.Open(context.Background(), "one", id, func(context.Context, Record, *LimitedFile) error { t.Fatal("cache lost after restart"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err = s.Read("two", id); !errors.Is(err, ErrUnavailable) {
		t.Fatal("cross-account access")
	}
	for _, key := range []string{"../message", id + "/..", "../../secret"} {
		if _, err = s.Read("one", key); err == nil {
			t.Fatal("unsafe path")
		}
	}
	meta, _, _ := s.paths("one", id)
	if bytes.Contains(got, []byte("crypto")) {
		t.Fatal("metadata exposed as media")
	}
	mi, _ := os.Stat(meta)
	if mi.Mode().Perm() != 0600 {
		t.Fatal("public metadata")
	}
}

func TestAttachmentDownloadCoalescesConcurrentRequests(t *testing.T) {
	s := testStore(t)
	data := testPNG()
	id := storeID(t, s, data, "image")
	var calls atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			f, _, _, err := s.Open(context.Background(), "one", id, func(_ context.Context, _ Record, out *LimitedFile) error {
				calls.Add(1)
				time.Sleep(20 * time.Millisecond)
				_, err := out.Write(data)
				return err
			})
			if err != nil {
				t.Error(err)
			} else {
				f.Close()
			}
		}()
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("downloads %d", calls.Load())
	}
}

func TestAttachmentRejectsUnsafePreviewAndDocumentDownload(t *testing.T) {
	for _, kind := range []string{"image", "video", "document"} {
		s := testStore(t)
		data := []byte("<html><script>malicious()</script></html>")
		id := storeID(t, s, data, kind)
		_, _, _, err := s.Open(context.Background(), "one", id, func(_ context.Context, _ Record, out *LimitedFile) error { _, err := out.Write(data); return err })
		if !errors.Is(err, ErrUnsafe) {
			t.Fatalf("unsafe preview accepted %s", kind)
		}
	}
}

func TestLimitedFileCannotBypassSizeBoundThroughIoCopy(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "test-file")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	bounded := &LimitedFile{File: f, limit: 8}
	_, err = io.Copy(bounded, io.LimitReader(bytes.NewReader(bytes.Repeat([]byte("a"), 20)), 20))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("io.Copy bypassed limit: %v", err)
	}
	if _, err = bounded.WriteAt([]byte("xx"), 7); !errors.Is(err, ErrTooLarge) {
		t.Fatal("WriteAt bypass")
	}
	if err = bounded.Truncate(9); !errors.Is(err, ErrTooLarge) {
		t.Fatal("truncate bypass")
	}
}

func TestAttachmentLengthQuotaAndSymlinkGuards(t *testing.T) {
	s := testStore(t)
	data := testPNG()
	big := testRecord(data, "image")
	big.Size = uint64(MaxBytes) + 1
	if _, err := s.Put("one", "big", "sender", big); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	id := storeID(t, s, data, "image")
	_, _, _, err := s.Open(context.Background(), "one", id, func(_ context.Context, _ Record, out *LimitedFile) error {
		_, err := out.Write([]byte("short"))
		return err
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatal("incorrect length accepted")
	}
	s.bytes = maxStoreBytes
	_, _, _, err = s.Open(context.Background(), "one", id, func(context.Context, Record, *LimitedFile) error { t.Fatal("download started over quota"); return nil })
	if !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
	meta, _, _ := s.paths("one", id)
	if err = os.Remove(meta); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(target, []byte("secret"), 0600)
	os.Symlink(target, meta)
	if _, err = s.Read("one", id); !errors.Is(err, ErrUnavailable) {
		t.Fatal("symlink read")
	}
}

func TestAttachmentDownloaderBoundsParallelWork(t *testing.T) {
	s := testStore(t)
	data := testPNG()
	var active, max atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		id, err := s.Put("one", string(rune('a'+i)), "sender", testRecord(data, "image"))
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, _, _, err := s.Open(context.Background(), "one", id, func(_ context.Context, _ Record, out *LimitedFile) error {
				n := active.Add(1)
				defer active.Add(-1)
				for old := max.Load(); n > old; old = max.Load() {
					if max.CompareAndSwap(old, n) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				_, err := out.Write(data)
				return err
			})
			if err != nil {
				t.Error(err)
			} else {
				f.Close()
			}
		}()
	}
	wg.Wait()
	if max.Load() > 2 {
		t.Fatalf("parallel downloads %d", max.Load())
	}
}
