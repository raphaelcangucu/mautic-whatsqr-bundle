package session

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/macro-markets/whatsqr/media"
)

type mediaRoundTrip func(*http.Request) (*http.Response, error)

func (f mediaRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAttachmentHTTPRestrictsDestinationsRedirectsAndSize(t *testing.T) {
	transport := mediaTransport{mediaRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, ContentLength: media.MaxBytes + 33, Body: io.NopCloser(strings.NewReader("too large"))}, nil
	})}
	for _, raw := range []string{"http://mmg.whatsapp.net/file", "https://127.0.0.1/private", "https://whatsapp.net.attacker.test/file", "https://user:pass@mmg.whatsapp.net/file", "https://mmg.whatsapp.net:8443/file"} {
		r, _ := http.NewRequest("GET", raw, nil)
		if _, err := transport.RoundTrip(r); !errors.Is(err, media.ErrUnsafe) {
			t.Fatal("unsafe destination accepted", raw)
		}
	}
	r, _ := http.NewRequest("GET", "https://mmg.whatsapp.net/file", nil)
	if _, err := transport.RoundTrip(r); !errors.Is(err, media.ErrTooLarge) {
		t.Fatal("huge content length accepted")
	}
	if err := attachmentHTTPClient().CheckRedirect(r, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal("redirects allowed")
	}
}

func TestAudioUploadHTTPAllowsOnlyBoundedWhatsAppAudio(t *testing.T) {
	hash := strings.Repeat("a", 43) + "="
	endpoint := "https://mmg.whatsapp.net/mms/audio/" + hash + "?auth=fixture&token=" + hash
	calls := 0
	transport := mediaTransport{mediaRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, ContentLength: 2, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	req, _ := http.NewRequest("POST", endpoint, strings.NewReader("encrypted audio"))
	if _, err := transport.RoundTrip(req); err != nil || calls != 1 {
		t.Fatal("native audio upload refused", err)
	}
	for _, raw := range []string{strings.Replace(endpoint, "https:", "http:", 1), strings.Replace(endpoint, "mmg.whatsapp.net", "mmg.whatsapp.net.attacker.test", 1), strings.Replace(endpoint, "mmg.whatsapp.net", "cdn.fbcdn.net", 1), strings.Replace(endpoint, "/mms/audio/", "/private/", 1), strings.Replace(endpoint, "auth=fixture", "auth=", 1), strings.Replace(endpoint, "token="+hash, "token=wrong", 1)} {
		bad, _ := http.NewRequest("POST", raw, strings.NewReader("encrypted audio"))
		if _, err := transport.RoundTrip(bad); !errors.Is(err, media.ErrUnsafe) {
			t.Fatal("unsafe audio upload accepted")
		}
	}
	for _, size := range []int64{-1, 0, 2097152 + 33} {
		bad := req.Clone(req.Context())
		bad.ContentLength = size
		if _, err := transport.RoundTrip(bad); !errors.Is(err, media.ErrUnsafe) {
			t.Fatal("invalid upload length accepted")
		}
	}
	bad := req.Clone(req.Context())
	bad.Method = "PUT"
	if _, err := transport.RoundTrip(bad); !errors.Is(err, media.ErrUnsafe) {
		t.Fatal("unexpected method accepted")
	}
	if calls != 1 {
		t.Fatal("rejected request reached network")
	}
}
