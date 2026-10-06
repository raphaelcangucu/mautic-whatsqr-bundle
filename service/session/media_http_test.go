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
