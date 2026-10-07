package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMobileConfigurationRequiresTokenAndDoesNotExposeSecrets(t *testing.T) {
	calls := 0
	secret := strings.Repeat("a", 64)
	server := NewServer(Options{Token: "service-secret", RegisterSession: func(id, s string) error {
		calls++
		if id != "new-number" || s != secret {
			return errors.New("private-secret")
		}
		return nil
	}})
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {"Bearer wrong", 401}, {"Bearer service-secret", 200}} {
		req := httptest.NewRequest("POST", "/sessions/new-number/configuration", strings.NewReader(`{"webhook_secret":"`+secret+`"}`))
		req.Header.Set("Authorization", tc.token)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("got %d want %d", rec.Code, tc.status)
		}
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatal("response exposed secret")
		}
	}
	if calls != 1 {
		t.Fatal("unauthorized registration ran")
	}
	req := httptest.NewRequest("POST", "/sessions/new-number/configuration", strings.NewReader(`{"webhook_secret":"invalid"}`))
	req.Header.Set("Authorization", "Bearer service-secret")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || strings.Contains(rec.Body.String(), "private-secret") {
		t.Fatal("refusal exposed private details")
	}
}
