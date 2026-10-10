package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/macro-markets/whatsqr/session"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var audioRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// Native AAC is sent as bytes over the authenticated loopback API, never a fetched URL.
func (s *Server) sendAudio(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2800000)
	var body struct {
		To        string `json:"to"`
		Data      string `json:"data"`
		Mime      string `json:"mime"`
		RequestID string `json:"request_id"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil {
		writeError(w, 400, false, "invalid audio")
		return
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		writeError(w, 400, false, "invalid audio")
		return
	}
	bytes, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil || len(bytes) < 32 || len(bytes) > 2097152 || string(bytes[4:8]) != "ftyp" || body.Mime != "audio/mp4" || body.To == "" || !audioRequestID.MatchString(body.RequestID) {
		writeError(w, 422, false, "invalid audio")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	id, err := s.opts.Manager.SendAudio(ctx, r.PathValue("id"), body.To, bytes, body.Mime, body.RequestID)
	if err != nil {
		// Report a bounded operational reason, never recipient, media URL, token or content.
		reason := "unknown"
		detail := err.Error()
		for _, known := range []string{"audio recipient lookup", "audio recipient", "audio upload", "audio send", "context deadline exceeded", "numero de destino nao confirmado", "upload failed with status code 400", "upload failed with status code 401", "upload failed with status code 403", "upload failed with status code 404", "upload failed with status code 429", "failed to refresh media connections", "audio receipt missing"} {
			if strings.Contains(detail, known) {
				reason += ";" + known
			}
		}
		log.Printf("native audio delivery failure: %s", reason)
		switch {
		case errors.Is(err, session.ErrUnknownSession):
			writeError(w, 404, false, "session unavailable")
		case errors.Is(err, session.ErrNotConnected), errors.Is(err, session.ErrSessionClosed):
			w.Header().Set("Retry-After", "5")
			writeError(w, 503, true, "session disconnected")
		default:
			writeError(w, 502, false, "audio delivery not confirmed")
		}
		return
	}
	writeJSON(w, 200, struct {
		MessageID string `json:"message_id"`
		RequestID string `json:"request_id"`
	}{id, body.RequestID})
}
