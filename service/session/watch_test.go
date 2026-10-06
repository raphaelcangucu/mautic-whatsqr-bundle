package session

import (
	"context"
	"testing"
	"time"
)

func watchNext(t *testing.T, ch <-chan Snapshot) Snapshot {
	t.Helper()
	select {
	case s, ok := <-ch:
		if !ok {
			t.Fatal("closed unexpectedly")
		}
		return s
	case <-time.After(time.Second):
		t.Fatal("missing push")
		return Snapshot{}
	}
}
func TestWatchPushesInitialAndTransitionsWithoutCrossAccountEvents(t *testing.T) {
	client := newFakeClient("one", "qr-one")
	m := NewManager(func(string) (Client, error) { return client, nil }, Options{})
	defer m.Shutdown()
	ch, cancel, err := m.Watch("one")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	other, stop, _ := m.Watch("two")
	defer stop()
	watchNext(t, other)
	if watchNext(t, ch).State != "" {
		t.Fatal("expected ready")
	}
	if _, err = m.Open(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if s := watchNext(t, ch); s.QR != "qr-one" || s.State != Pairing {
		t.Fatal(s)
	}
	client.events <- Event{Kind: EventPaired, JID: jidPaired}
	if s := watchNext(t, ch); s.State != Connected || s.QR != "" {
		t.Fatal(s)
	}
	client.events <- Event{Kind: EventDropped}
	if s := watchNext(t, ch); s.State != Reconnecting {
		t.Fatal(s)
	}
	client.events <- Event{Kind: EventResumed, JID: jidPaired}
	if watchNext(t, ch).State != Connected {
		t.Fatal("did not reconnect")
	}
	select {
	case <-other:
		t.Fatal("cross-account event")
	default:
	}
}
func TestWatchPushesQRRenewalAndCoalescesSlowConsumers(t *testing.T) {
	client := newFakeClient("one", "qr-old")
	m := NewManager(func(string) (Client, error) { return client, nil }, Options{})
	defer m.Shutdown()
	m.Open(context.Background(), "one")
	ch, cancel, _ := m.Watch("one")
	defer cancel()
	watchNext(t, ch)
	for _, qr := range []string{"qr-next", "qr-current"} {
		client.mu.Lock()
		client.qr = qr
		client.mu.Unlock()
		client.events <- Event{Kind: EventQRChanged}
		m.Snapshot("one")
	}
	if s := watchNext(t, ch); s.QR != "qr-current" {
		t.Fatal(s)
	}
	if m.watcherCount != 1 {
		t.Fatal("unexpected subscribers")
	}
	cancel()
	cancel()
	if m.watcherCount != 0 {
		t.Fatal("subscriber leak")
	}
	if _, ok := <-ch; ok {
		t.Fatal("cancel must close")
	}
}
func TestWatchLimitsAndShutdownReleaseAllSubscribers(t *testing.T) {
	m := NewManager(nil, Options{})
	for i := 0; i < 128; i++ {
		if _, _, err := m.Watch("one"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := m.Watch("one"); err != ErrWatchLimit {
		t.Fatal(err)
	}
	m.Shutdown()
	if m.watcherCount != 0 {
		t.Fatal("shutdown leak")
	}
	if _, _, err := m.Watch("one"); err != ErrSessionClosed {
		t.Fatal(err)
	}
}
