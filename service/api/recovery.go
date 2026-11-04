package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/macro-markets/whatsqr/session"
)

// Uses the same loopback-only authenticated router and strict JSON/body limit.
func (s *Server) recoverMessage(w http.ResponseWriter, r *http.Request) {
	var anchor session.HistoryAnchor
	if !s.decode(w, r, &anchor) {
		return
	}
	if err := session.ValidateHistoryAnchors([]session.HistoryAnchor{anchor}); err != nil {
		writeError(w, http.StatusBadRequest, false, "invalid private message anchor")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.opts.Manager.RequestMessage(ctx, r.PathValue("id"), anchor); err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, session.ErrUnknownSession) {
			status = http.StatusNotFound
		}
		if errors.Is(err, session.ErrHistoryBusy) {
			status = http.StatusTooManyRequests
		}
		writeError(w, status, true, "message recovery could not be requested")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "requested", "messages": 1})
}
