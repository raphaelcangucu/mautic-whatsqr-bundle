package session

import (
	"strings"
	"testing"
)

func TestPrivateLoggerRemovesSignedMediaCredentials(t *testing.T) {
	got := privateLogMessage(`failed Delete %q: denied`, "https://user:password@cdn.whatsapp.net/history/blob?auth=PRIVATE_AUTH&token=PRIVATE_TOKEN#secret")
	for _, secret := range []string{"password", "PRIVATE_AUTH", "PRIVATE_TOKEN", "secret"} {
		if strings.Contains(got, secret) {
			t.Fatal("log leaked a media credential")
		}
	}
	if !strings.Contains(got, "cdn.whatsapp.net/history/blob") || !strings.Contains(got, "denied") {
		t.Fatal("lost useful failure context")
	}
}
