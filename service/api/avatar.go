package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/macro-markets/whatsqr/session"
)

var errAvatarBusy = errors.New("avatar queries busy")

type avatarResult struct {
	image session.ProfileImage
	err   error
	until time.Time
}
type avatarPending struct {
	done   chan struct{}
	result avatarResult
}

// Bound both cached data and concurrent upstream requests. Negative caching avoids
// repeating privacy-denied lookups every time a message updates the Inbox.
type avatarCache struct {
	mu      sync.Mutex
	items   map[string]avatarResult
	pending map[string]*avatarPending
	slots   chan struct{}
	bytes   int
	now     func() time.Time
}

func newAvatarCache() *avatarCache {
	return &avatarCache{items: map[string]avatarResult{}, pending: map[string]*avatarPending{}, slots: make(chan struct{}, 2), now: time.Now}
}

func (c *avatarCache) load(ctx context.Context, key string, fetch func(context.Context) (session.ProfileImage, error)) (session.ProfileImage, error) {
	c.mu.Lock()
	if item, ok := c.items[key]; ok && c.now().Before(item.until) {
		c.mu.Unlock()
		return item.image, item.err
	}
	if pending := c.pending[key]; pending != nil {
		c.mu.Unlock()
		select {
		case <-pending.done:
			return pending.result.image, pending.result.err
		case <-ctx.Done():
			return session.ProfileImage{}, ctx.Err()
		}
	}
	if len(c.pending) >= 32 {
		c.mu.Unlock()
		return session.ProfileImage{}, errAvatarBusy
	}
	pending := &avatarPending{done: make(chan struct{})}
	c.pending[key] = pending
	c.mu.Unlock()
	var result avatarResult
	select {
	case c.slots <- struct{}{}:
		result.image, result.err = fetch(ctx)
		<-c.slots
	case <-ctx.Done():
		result.err = ctx.Err()
	}
	if len(result.image.Data) > session.MaxAvatarBytes {
		result.image = session.ProfileImage{}
		result.err = session.ErrAvatarUnavailable
	}
	ttl := time.Hour
	if errors.Is(result.err, session.ErrAvatarUnavailable) {
		ttl = 15 * time.Minute
	} else if result.err != nil {
		ttl = time.Minute
	}
	result.until = c.now().Add(ttl)
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, key)
	if !errors.Is(result.err, context.Canceled) && !errors.Is(result.err, context.DeadlineExceeded) {
		if old, ok := c.items[key]; ok {
			c.bytes -= len(old.image.Data)
			delete(c.items, key)
		}
		for len(c.items) >= 256 || c.bytes+len(result.image.Data) > 8<<20 {
			oldest := ""
			var until time.Time
			for k, v := range c.items {
				if oldest == "" || v.until.Before(until) {
					oldest, until = k, v.until
				}
			}
			if oldest == "" {
				break
			}
			c.bytes -= len(c.items[oldest].image.Data)
			delete(c.items, oldest)
		}
		c.items[key] = result
		c.bytes += len(result.image.Data)
	}
	pending.result = result
	close(pending.done)
	return result.image, result.err
}

func (s *Server) profileImage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	id, recipient := r.PathValue("id"), r.URL.Query().Get("to")
	if id == "" || (s.opts.HasSecret != nil && !s.opts.HasSecret(id)) {
		http.NotFound(w, r)
		return
	}
	if !validAvatarRecipient(recipient) {
		http.Error(w, "invalid recipient", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	picture, err := s.avatars.load(ctx, id+"\x00"+recipient, func(ctx context.Context) (session.ProfileImage, error) {
		return s.opts.Manager.ProfileImage(ctx, id, recipient)
	})
	if err != nil {
		if errors.Is(err, session.ErrAvatarUnavailable) || errors.Is(err, session.ErrUnknownSession) {
			http.NotFound(w, r)
		} else {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "photo temporarily unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	etag := fmt.Sprintf("\"%x\"", sha256.Sum256(picture.Data))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", picture.MIME)
	w.Header().Set("Content-Length", fmt.Sprint(len(picture.Data)))
	_, _ = w.Write(picture.Data)
}

func validAvatarRecipient(recipient string) bool {
	if recipient == "" || len(recipient) > 64 {
		return false
	}
	// Only individual phone/LID addresses, never groups or arbitrary URL input.
	user := recipient
	for i, char := range recipient {
		if char == '@' {
			user = recipient[:i]
			server := recipient[i+1:]
			if server != "s.whatsapp.net" && server != "lid" {
				return false
			}
			break
		}
	}
	if user == recipient && (len(user) < 8 || len(user) > 15) {
		return false
	}
	for _, char := range user {
		if char < '0' || char > '9' {
			return false
		}
	}
	return user != ""
}
