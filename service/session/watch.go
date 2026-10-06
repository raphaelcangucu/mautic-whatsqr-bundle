package session

import (
	"errors"
	"sync"
)

var ErrWatchLimit = errors.New("session: limite de assinantes atingido")

// Watch returns an atomic initial snapshot plus future changes for one account.
// Buffers hold only the latest state, so a slow browser cannot block WhatsApp.
// An unopened account has an empty State and can be watched before Open.
func (m *Manager) Watch(id string) (<-chan Snapshot, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, ErrSessionClosed
	}
	if id == "" {
		return nil, nil, ErrEmptyID
	}
	if m.watcherCount >= 128 {
		return nil, nil, ErrWatchLimit
	}
	ch := make(chan Snapshot, 1)
	if m.watchers[id] == nil {
		m.watchers[id] = map[chan Snapshot]struct{}{}
	}
	m.watchers[id][ch] = struct{}{}
	m.watcherCount++
	initial, ok := m.latest[id]
	if !ok {
		initial = Snapshot{ID: id}
	}
	ch <- initial
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if _, ok := m.watchers[id][ch]; ok {
				delete(m.watchers[id], ch)
				m.watcherCount--
				close(ch)
				if len(m.watchers[id]) == 0 {
					delete(m.watchers, id)
				}
			}
		})
	}
	return ch, cancel, nil
}

// Caller owns m.mu. No database reads, network requests or polling here.
func (m *Manager) publishLocked(snap Snapshot) {
	if m.latest[snap.ID] == snap {
		return
	}
	m.latest[snap.ID] = snap
	for ch := range m.watchers[snap.ID] {
		select {
		case ch <- snap:
		default:
			select {
			case <-ch:
			default:
			}
			ch <- snap
		}
	}
}

// Only the session goroutine reads its state. The client owns its QR mutex.
func (m *Manager) publishLive(lv *live) {
	snap := Snapshot{ID: lv.id, State: lv.state.State(), JID: lv.state.JID(), Reason: lv.state.FailureReason()}
	if snap.State == Pairing {
		snap.QR = lv.client.CurrentQR()
	}
	if lv.lastRefusal != nil {
		snap.LastRefusal = lv.lastRefusal.Error()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// A late event from an already removed session must not resurrect it.
	if m.sessions[lv.id] != lv || m.closed {
		return
	}
	m.publishLocked(snap)
}
