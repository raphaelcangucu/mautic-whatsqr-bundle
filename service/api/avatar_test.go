package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/macro-markets/whatsqr/session"
)

func TestAvatarCacheCoalescesConcurrentLookupsAndIsolatesAccounts(t *testing.T) {
	cache := newAvatarCache()
	var calls atomic.Int32
	fetch := func(ctx context.Context) (session.ProfileImage, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return session.ProfileImage{Data: []byte("photo"), MIME: "image/jpeg"}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			picture, err := cache.load(context.Background(), "one\x00contact", fetch)
			if err != nil || string(picture.Data) != "photo" {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	cache.load(context.Background(), "two\x00contact", fetch)
	if calls.Load() != 2 {
		t.Fatal("cache shared between accounts")
	}
}

func TestAvatarCacheBoundsMemoryAndRetriesMissingPhotosAfterTTL(t *testing.T) {
	cache := newAvatarCache()
	now := time.Now()
	cache.now = func() time.Time { return now }
	calls := 0
	fetch := func(context.Context) (session.ProfileImage, error) {
		calls++
		return session.ProfileImage{}, session.ErrAvatarUnavailable
	}
	for i := 0; i < 2; i++ {
		if _, err := cache.load(context.Background(), "one", fetch); !errors.Is(err, session.ErrAvatarUnavailable) {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("missing photo repeatedly queried")
	}
	now = now.Add(16 * time.Minute)
	cache.load(context.Background(), "one", fetch)
	if calls != 2 {
		t.Fatal("missing photo never refreshed")
	}
	for i := 0; i < 350; i++ {
		cache.load(context.Background(), string(rune(i)), func(context.Context) (session.ProfileImage, error) {
			return session.ProfileImage{Data: make([]byte, 256<<10), MIME: "image/png"}, nil
		})
	}
	if cache.bytes > 8<<20 || len(cache.items) > 256 {
		t.Fatal("unbounded avatar cache", cache.bytes, len(cache.items))
	}
}

func TestAvatarCacheCapsConcurrentUpstreamRequests(t *testing.T) {
	cache := newAvatarCache()
	var active, peak atomic.Int32
	fetch := func(context.Context) (session.ProfileImage, error) {
		n := active.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		active.Add(-1)
		return session.ProfileImage{Data: []byte("a"), MIME: "image/jpeg"}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); cache.load(context.Background(), string(rune(i)), fetch) }(i)
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatal(peak.Load())
	}
}

type avatarClient struct {
	*fakeClient
	picture session.ProfileImage
	calls   atomic.Int32
}

func (c *avatarClient) ProfileImage(context.Context, string) (session.ProfileImage, error) {
	c.calls.Add(1)
	return c.picture, nil
}

func TestAvatarHTTPRequiresAuthenticationAndReturnsImageWithCacheHeaders(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII=")
	client := &avatarClient{fakeClient: newFakeClient("", testJID), picture: session.ProfileImage{Data: png, MIME: "image/png"}}
	manager := session.NewManager(func(string) (session.Client, error) { return client, nil }, session.Options{})
	defer manager.Shutdown()
	if _, err := manager.Open(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Options{Manager: manager, Token: testToken, HasSecret: func(id string) bool { return id == "one" }})
	send := func(path, token, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("If-None-Match", etag)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w
	}
	if w := send("/sessions/one/avatar?to=5511999990000", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := send("/sessions/two/avatar?to=5511999990000", testToken, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := send("/sessions/one/avatar?to=123@g.us", testToken, ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	w := send("/sessions/one/avatar?to=5511999990000", testToken, "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "private, max-age=3600" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(w.Code, w.Header())
	}
	if r := send("/sessions/one/avatar?to=5511999990000", testToken, w.Header().Get("ETag")); r.Code != 304 || r.Body.Len() != 0 {
		t.Fatal(r.Code)
	}
	if client.calls.Load() != 1 {
		t.Fatal(client.calls.Load())
	}
}
