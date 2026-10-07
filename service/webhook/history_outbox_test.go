package webhook

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/macro-markets/whatsqr/session"
)

func TestFailedHistoryDeliveryIsRecoveredAfterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	outbox, err := NewHistoryOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	attempted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		attempted <- struct{}{}
	}))
	defer server.Close()
	secret := func(string) (string, bool) { return "test-only-secret", true }
	sender := New(Options{URL: server.URL, Secret: secret, History: outbox, Attempts: 1})
	notice := messageNotice("one", "recovered", "old message")
	notice.Message.Historical = true
	sender.Notify(notice)
	select {
	case <-attempted:
	case <-time.After(3 * time.Second):
		sender.Close()
		t.Fatal("no delivery attempted")
	}
	sender.Close()
	restored, err := NewHistoryOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if event, err := restored.peek(); err != nil || event == nil {
		t.Fatal("failed delivery was lost")
	}
	delivered := make(chan struct{}, 1)
	accepted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		delivered <- struct{}{}
	}))
	defer accepted.Close()
	restarted := New(Options{URL: accepted.URL, Secret: secret, History: restored, Attempts: 1})
	select {
	case <-delivered:
	case <-time.After(3 * time.Second):
		restarted.Close()
		t.Fatal("no recovered delivery")
	}
	// The server observing a POST precedes the client receiving its response.
	// Wait for the actual durable acknowledgement before shutting it down.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		event, err := restored.peek()
		if err != nil {
			restarted.Close()
			t.Fatal(err)
		}
		if event == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	restarted.Close()
	if event, err := restored.peek(); err != nil || event != nil {
		t.Fatal("accepted delivery not acknowledged")
	}
}

func TestHistoricalOutboxSurvivesRestartAndDeletesOnlyAfterAcknowledgement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private-history")
	outbox, err := NewHistoryOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	notice := messageNotice("account-one", "history-id", "old text")
	notice.Message.FromMe = true
	notice.Message.Historical = true
	event, err := newEvent(notice)
	if err != nil {
		t.Fatal(err)
	}
	if err = outbox.enqueue(event); err != nil {
		t.Fatal(err)
	}
	if err = outbox.enqueue(event); err != nil {
		t.Fatal(err)
	}
	if len(outbox.files) != 1 {
		t.Fatal("duplicated queued message")
	}
	info, _ := os.Stat(filepath.Join(dir, outbox.files[0]))
	if info.Mode().Perm() != 0600 {
		t.Fatal("history file is public")
	}
	restarted, err := NewHistoryOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.peek()
	if err != nil || got == nil || got.sessionID != "account-one" || got.id != event.id {
		t.Fatalf("history not recovered: %v", err)
	}
	var body payload
	if err = json.Unmarshal(got.body, &body); err != nil || !body.Message.Historical || !body.Message.FromMe {
		t.Fatal("lost history direction")
	}
	if err = restarted.complete(got); err != nil {
		t.Fatal(err)
	}
	if got, err = restarted.peek(); err != nil || got != nil {
		t.Fatal("acknowledged history retained")
	}
}

func TestHistoricalOutboxRejectsLinksAndOversizedMessages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outside")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHistoryOutbox(link); err == nil {
		t.Fatal("symlink history directory accepted")
	}
	outbox, _ := NewHistoryOutbox(filepath.Join(dir, "safe"))
	ev := &event{id: "a", kind: session.NoticeMessage, sessionID: "one", body: make([]byte, maxHistoryEventBytes+1)}
	if outbox.enqueue(ev) == nil {
		t.Fatal("oversized history event accepted")
	}
}
