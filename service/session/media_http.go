package session

import (
	"net/http"
	"time"

	"github.com/macro-markets/whatsqr/media"
)

type mediaTransport struct{ transport http.RoundTripper }

func (t mediaTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet || !allowedProfileURL(r.URL.String()) {
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
