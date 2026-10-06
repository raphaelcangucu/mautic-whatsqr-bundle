package api

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/macro-markets/whatsqr/session"
)

// Private bearer-authenticated SSE; PHP gates access with the Mautic session.
// Each request is bounded and every reconnect gets a current atomic snapshot.
func (s *Server) sessionEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.opts.HasSecret != nil && !s.opts.HasSecret(id) {
		writeError(w, http.StatusNotFound, false, "sessao nao configurada")
		return
	}
	updates, cancel, err := s.opts.Manager.Watch(id)
	if err != nil {
		code := http.StatusServiceUnavailable
		if errors.Is(err, session.ErrWatchLimit) {
			code = http.StatusTooManyRequests
		}
		writeError(w, code, true, "atualizacoes temporariamente indisponiveis")
		return
	}
	defer cancel()
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case snap, ok := <-updates:
			if !ok {
				return
			}
			status := string(snap.State)
			if status == "" {
				status = "ready"
			}
			body, _ := json.Marshal(struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				JID    string `json:"jid,omitempty"`
				QR     string `json:"qr,omitempty"`
				Reason string `json:"reason,omitempty"`
			}{snap.ID, status, snap.JID, snap.QR, snap.Reason})
			if _, err = fmt.Fprintf(w, "id: %x\nevent: session\ndata: %s\n\n", sha256.Sum256(body), body); err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err = fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
		case <-deadline.C:
			// A clean rotation should not display a network-error flash in the UI.
			fmt.Fprint(w, "event: rotate\ndata: {}\n\n")
			controller.Flush()
			return
		case <-r.Context().Done():
			return
		}
		if controller.Flush() != nil {
			return
		}
	}
}
