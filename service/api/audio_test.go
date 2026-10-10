package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func (c *fakeClient) SendAudio(ctx context.Context, to string, data []byte, mime, id string) (string, error) {
	return c.SendText(ctx, to, string(data))
}
func TestAudioUploadValidation(t *testing.T) {
	h := newHarness(t)
	for _, body := range []string{`{"to":"+551100000000","data":"bad","mime":"audio/mp4","request_id":"audio-test-123456789"}`, `{"to":"+551100000000","data":"YWJj","mime":"audio/mp4","request_id":"audio-test-123456789"}`, `{"to":"x","data":"https://example.test/audio","mime":"audio/mp4","request_id":"audio-test-123456789"}`} {
		r := httptest.NewRequest("POST", "/sessions/test/messages/audio", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		if w.Code != 422 {
			t.Fatalf("invalid audio accepted: %d", w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/sessions/test/messages/audio", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("audio route not authenticated: %d", w.Code)
	}
	bytes := []byte("\x00\x00\x00\x20ftypM4A " + strings.Repeat("0", 30))
	body, _ := json.Marshal(map[string]string{"to": "+551100000000", "data": base64.StdEncoding.EncodeToString(bytes), "mime": "audio/mp4", "request_id": "audio-test-123456789"})
	r = httptest.NewRequest("POST", "/sessions/missing/messages/audio", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w = httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("missing session sent audio: %d", w.Code)
	}
}

func TestAudioDeliveryRequiresConnectedSessionAndReturnsReceipt(t *testing.T) {
	h := newHarness(t)
	data := base64.StdEncoding.EncodeToString([]byte("\x00\x00\x00\x20ftypM4A " + strings.Repeat("0", 30)))
	body, _ := json.Marshal(map[string]string{"to": "+551100000000", "data": data, "mime": "audio/mp4", "request_id": "audio-test-123456789"})
	h.openPaired("paired")
	w := h.do("POST", "/sessions/paired/messages/audio", string(body))
	if w.Code != 200 {
		t.Fatalf("connected audio rejected: %d", w.Code)
	}
	var result map[string]string
	decode(t, w, &result)
	if result["message_id"] != "wamid-de-mentira" || result["request_id"] != "audio-test-123456789" {
		t.Fatal("receipt or idempotency identity missing")
	}
}
