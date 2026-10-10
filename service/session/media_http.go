package session

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/macro-markets/whatsqr/media"
)

type mediaTransport struct{ transport http.RoundTripper }

func (t mediaTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !allowedMediaRequest(r) {
		return nil, media.ErrUnsafe
	}
	response, err := t.transport.RoundTrip(r)
	if err == nil && response.ContentLength > media.MaxBytes+32 {
		response.Body.Close()
		return nil, media.ErrTooLarge
	}
	return response, err
}

func attachmentHTTPClient() *http.Client {
	return &http.Client{Timeout: 25 * time.Second, Transport: mediaTransport{http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Audio uploads share whatsmeow's media client with downloads. Permit only the
// encrypted AAC upload route on WhatsApp's HTTPS CDN; keep other POSTs blocked.
var audioUploadPath = regexp.MustCompile(`^/mms/audio/[A-Za-z0-9_-]{43}=$`)

func allowedMediaRequest(r *http.Request) bool {
	if r.URL == nil || !allowedProfileURL(r.URL.String()) {
		return false
	}
	if r.Method == http.MethodGet {
		return true
	}
	host := strings.ToLower(r.URL.Hostname())
	return r.Method == http.MethodPost && (host == "whatsapp.net" || strings.HasSuffix(host, ".whatsapp.net")) && audioUploadPath.MatchString(r.URL.Path) && r.ContentLength > 0 && r.ContentLength <= 2097152+32 && r.URL.Query().Get("auth") != "" && r.URL.Query().Get("token") == strings.TrimPrefix(r.URL.Path, "/mms/audio/")
}
