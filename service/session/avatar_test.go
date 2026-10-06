package session

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
)

type avatarRoundTrip func(*http.Request) (*http.Response, error)

func (r avatarRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return r(req) }

func TestAvatarDownloadAllowsOnlyTrustedHTTPSImages(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII=")
	calls := 0
	client := &http.Client{Transport: avatarRoundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(png))), ContentLength: int64(len(png))}, nil
	})}
	for _, raw := range []string{"http://pps.whatsapp.net/p.jpg", "https://evilwhatsapp.net/p.jpg", "https://pps.whatsapp.net.evil.test/p.jpg", "https://127.0.0.1/p.jpg", "https://user@pps.whatsapp.net/p.jpg", "https://pps.whatsapp.net:8080/p.jpg"} {
		if _, err := downloadProfileImage(context.Background(), raw, client); err == nil {
			t.Fatal("unsafe URL accepted", raw)
		}
	}
	if calls != 0 {
		t.Fatal("network call made for rejected URL")
	}
	picture, err := downloadProfileImage(context.Background(), "https://pps.whatsapp.net/p.jpg", client)
	if err != nil || picture.MIME != "image/png" || len(picture.Data) != len(png) {
		t.Fatal(picture.MIME, err)
	}
}

func TestAvatarDownloadRejectsHTMLLargeFilesAndRedirects(t *testing.T) {
	for _, item := range []struct {
		data   string
		status int
		size   int64
	}{{"<svg onload=alert(1)>", 200, -1}, {strings.Repeat("x", MaxAvatarBytes+1), 200, -1}, {"", 200, MaxAvatarBytes + 1}, {"", 302, 0}} {
		client := &http.Client{Transport: avatarRoundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: item.status, ContentLength: item.size, Body: io.NopCloser(strings.NewReader(item.data))}, nil
		})}
		if _, err := downloadProfileImage(context.Background(), "https://pps.whatsapp.net/p.jpg", client); err == nil {
			t.Fatal("invalid photo accepted", item.status, item.size)
		}
	}
}

func TestAvatarRecipientNeverTreatsPrivacyLIDAsAPhoneOrAcceptsGroup(t *testing.T) {
	for _, raw := range []string{"5511999990000", "5511999990000@s.whatsapp.net", "123456@lid"} {
		if !validProfileRecipient(raw) {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{"", "jid:123456@lid", "123456@g.us", "+5511999990000", "https://foo/123456789", "a@lid"} {
		if validProfileRecipient(raw) {
			t.Fatal(raw)
		}
	}
}
