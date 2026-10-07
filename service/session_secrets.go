package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// New mobile sessions are additive. Static session secrets remain immutable.
// This registry does not touch the WhatsApp credential database.
type sessionSecrets struct {
	mu      sync.RWMutex
	path    string
	initial map[string]SessionConfig
	added   map[string]SessionConfig
}

var newSessionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var newSessionSecret = regexp.MustCompile(`^[a-f0-9]{64}$`)

func loadSessionSecrets(path string, initial map[string]SessionConfig) (*sessionSecrets, error) {
	r := &sessionSecrets{path: path, initial: initial, added: map[string]SessionConfig{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, fmt.Errorf("session registry must be a private regular file")
	}
	if info.Size() > 1<<20 {
		return nil, fmt.Errorf("session registry exceeds its limit")
	}
	if err = json.NewDecoder(f).Decode(&r.added); err != nil {
		return nil, err
	}
	for id, c := range r.added {
		if !newSessionID.MatchString(id) || !newSessionSecret.MatchString(c.WebhookSecret) {
			return nil, fmt.Errorf("invalid session registry")
		}
		if static, ok := initial[id]; ok && static.WebhookSecret != c.WebhookSecret {
			return nil, fmt.Errorf("session registry conflicts with static configuration")
		}
	}
	return r, nil
}
func (r *sessionSecrets) Secret(id string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if c, ok := r.initial[id]; ok {
		return c.WebhookSecret, c.WebhookSecret != ""
	}
	c, ok := r.added[id]
	return c.WebhookSecret, ok
}
func (r *sessionSecrets) HasSecret(id string) bool { _, ok := r.Secret(id); return ok }
func (r *sessionSecrets) Register(id, secret string) error {
	if !newSessionID.MatchString(id) || !newSessionSecret.MatchString(secret) {
		return fmt.Errorf("invalid new session configuration")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.initial[id]
	if !ok {
		existing, ok = r.added[id]
	}
	if ok {
		if subtle.ConstantTimeCompare([]byte(existing.WebhookSecret), []byte(secret)) == 1 {
			return nil
		}
		return fmt.Errorf("existing session configuration cannot be replaced")
	}
	if len(r.added) >= 1000 {
		return fmt.Errorf("session registry is full")
	}
	next := make(map[string]SessionConfig, len(r.added)+1)
	for k, v := range r.added {
		next[k] = v
	}
	next[id] = SessionConfig{WebhookSecret: secret}
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(r.path), ".whatsqr-sessions-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, r.path); err != nil {
		return err
	}
	r.added = next
	return nil
}
