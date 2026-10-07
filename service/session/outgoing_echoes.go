package session

import (
	"sync"
	"time"
)

// Suppress mirrors of API sends before the HTTP caller persists their ID.
// Older/replayed events are also deduplicated by Mautic's external message ID.
// This cache contains only IDs, has a fixed bound and needs no persistent secrets.
type outgoingEchoes struct {
	mu  sync.Mutex
	ids map[string]time.Time
}

const outgoingEchoTTL = 10 * time.Minute
const outgoingEchoLimit = 4096

func (e *outgoingEchoes) remember(id string, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ids == nil {
		e.ids = make(map[string]time.Time)
	}
	var oldestID string
	var oldest time.Time
	for key, at := range e.ids {
		if now.Sub(at) >= outgoingEchoTTL {
			delete(e.ids, key)
			continue
		}
		if oldestID == "" || at.Before(oldest) {
			oldestID, oldest = key, at
		}
	}
	if len(e.ids) >= outgoingEchoLimit {
		delete(e.ids, oldestID)
	}
	e.ids[id] = now
}

func (e *outgoingEchoes) contains(id string, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	at, found := e.ids[id]
	if found && now.Sub(at) >= outgoingEchoTTL {
		delete(e.ids, id)
		return false
	}
	return found
}
