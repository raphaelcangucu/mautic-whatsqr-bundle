package api

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/macro-markets/whatsqr/session"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSERequiresBearerAndKnownAccount(t *testing.T) {
	m := session.NewManager(nil, session.Options{})
	defer m.Shutdown()
	s := NewServer(Options{Manager: m, Token: testToken, HasSecret: func(id string) bool { return id == "one" }})
	for _, test := range []struct {
		token, id string
		code      int
	}{{"", "one", 401}, {testToken, "other", 404}} {
		r := httptest.NewRequest("GET", "/sessions/"+test.id+"/events", nil)
		r.Header.Set("Authorization", "Bearer "+test.token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != test.code {
			t.Fatal(w.Code)
		}
	}
}
func TestSSEFlushesInitialStateAndChangesAndCancels(t *testing.T) {
	c := newFakeClient("code", "")
	m := session.NewManager(func(string) (session.Client, error) { return c, nil }, session.Options{})
	defer m.Shutdown()
	if _, err := m.Open(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(NewServer(Options{Manager: m, Token: testToken}).Handler())
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/sessions/one/events", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal(resp.Header)
	}
	reader := bufio.NewReader(resp.Body)
	read := func() map[string]any {
		t.Helper()
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "data: ") {
				var body map[string]any
				if err = json.Unmarshal([]byte(line[6:]), &body); err != nil {
					t.Fatal(err)
				}
				return body
			}
		}
	}
	if read()["status"] != "pairing" {
		t.Fatal("missing initial")
	}
	c.events <- session.Event{Kind: session.EventPaired, JID: testJID}
	next := read()
	if next["status"] != "connected" || next["qr"] != nil {
		t.Fatal(next)
	}
	cancel()
}
