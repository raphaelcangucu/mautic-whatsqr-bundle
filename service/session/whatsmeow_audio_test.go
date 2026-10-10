package session

import "testing"

func TestAudioRetryIdentity(t *testing.T) {
	id := audioMessageID("session-a", "recipient-a", "request-a")
	if len(id) != 28 || id != audioMessageID("session-a", "recipient-a", "request-a") {
		t.Fatal("retry identity changed")
	}
	for _, other := range []string{string(audioMessageID("session-b", "recipient-a", "request-a")), string(audioMessageID("session-a", "recipient-b", "request-a")), string(audioMessageID("session-a", "recipient-a", "request-b"))} {
		if string(id) == other {
			t.Fatal("audio identity leaked across session, recipient or send")
		}
	}
}
