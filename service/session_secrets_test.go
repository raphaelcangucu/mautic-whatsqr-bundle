package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMobileSecretsPersistAndNeverReplaceConnectedSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mobile.json")
	initial := map[string]SessionConfig{"existing": {WebhookSecret: "original-secret"}}
	r, err := loadSessionSecrets(path, initial)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("a", 64)
	if err = r.Register("mobile-9-new", secret); err != nil {
		t.Fatal(err)
	}
	if err = r.Register("mobile-9-new", secret); err != nil {
		t.Fatal(err)
	}
	if err = r.Register("mobile-9-new", strings.Repeat("b", 64)); err == nil {
		t.Fatal("replaced existing secret")
	}
	if err = r.Register("existing", secret); err == nil {
		t.Fatal("replaced static secret")
	}
	if err = r.Register("../outside", secret); err == nil {
		t.Fatal("accepted invalid id")
	}
	if err = r.Register("other", "short"); err == nil {
		t.Fatal("accepted weak secret")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("registry is not private")
	}
	restored, err := loadSessionSecrets(path, initial)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := restored.Secret("mobile-9-new"); !ok || v != secret {
		t.Fatal("lost persisted secret")
	}
	if v, _ := restored.Secret("existing"); v != "original-secret" {
		t.Fatal("changed static secret")
	}
}
func TestMobileSecretsConcurrentReadsAndWrites(t *testing.T) {
	r, _ := loadSessionSecrets(filepath.Join(t.TempDir(), "mobile.json"), nil)
	var wg sync.WaitGroup
	for _, id := range []string{"one", "two", "three"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if err := r.Register(id, strings.Repeat("a", 64)); err != nil {
					t.Error(err)
				}
				r.HasSecret(id)
			}
		}(id)
	}
	wg.Wait()
	restored, err := loadSessionSecrets(r.path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two", "three"} {
		if !restored.HasSecret(id) {
			t.Fatal("lost concurrent registration")
		}
	}
}
func TestMobileRegistryRefusesUnsafePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry")
	os.WriteFile(path, []byte(`{}`), 0644)
	if _, err := loadSessionSecrets(path, nil); err == nil {
		t.Fatal("accepted public registry")
	}
}
