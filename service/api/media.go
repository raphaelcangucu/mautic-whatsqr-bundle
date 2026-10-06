package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/macro-markets/whatsqr/media"
)

var singleRange = regexp.MustCompile(`^bytes=(?:[0-9]{1,12}-[0-9]{0,12}|-[0-9]{1,12})$`)

func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	id, key := r.PathValue("id"), r.PathValue("mediaID")
	if s.opts.Media == nil || !media.ValidID(key) || s.opts.HasSecret != nil && !s.opts.HasSecret(id) {
		http.NotFound(w, r)
		return
	}
	if value := r.Header.Get("Range"); value != "" && !singleRange.MatchString(value) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), media.DownloadTimeout)
	defer cancel()
	f, _, mime, err := s.opts.Media.Open(ctx, id, key, func(ctx context.Context, record media.Record, file *media.LimitedFile) error {
		return s.opts.Manager.DownloadMedia(ctx, id, record, file)
	})
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("ETag", "\""+key+"\"")
	if mime == "application/pdf" || mime == "text/plain" || mime == "text/csv" || strings.HasPrefix(mime, "application/vnd.") {
		w.Header().Set("Content-Disposition", `attachment; filename="attachment"`)
	}
	// ServeContent provides byte ranges for audio/video seeking without loading
	// the full file into memory. Neither filename nor path is taken from the URL.
	http.ServeContent(w, r, "attachment", info.ModTime(), f)
}
