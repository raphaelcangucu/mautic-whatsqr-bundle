package session

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestPhoneReplyUsesPeerInsteadOfOwnAccountAndName(t *testing.T) {
	for _, privacy := range []bool{false, true} {
		chat := types.NewJID("5511988887777", types.DefaultUserServer)
		alternate := types.EmptyJID
		if privacy {
			chat = types.NewJID("opaque", types.HiddenUserServer)
			alternate = types.NewJID("5511988887777", types.DefaultUserServer)
		}
		evt := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{IsFromMe: true, Chat: chat, RecipientAlt: alternate, Sender: types.NewJID("5511999990000", types.DefaultUserServer), SenderAlt: types.NewJID("5511999990000", types.DefaultUserServer)}, ID: "phone-reply", PushName: "Own account"}, Message: &waE2E.Message{Conversation: proto.String("Resposta do telefone")}}
		got := translateMessage(evt)
		if got == nil || !got.FromMe || got.From != "5511988887777@s.whatsapp.net" || got.Name != "" {
			t.Fatalf("wrong outbound peer: %+v", got)
		}
		if got.Text != "Resposta do telefone" || got.Historical {
			t.Fatal("lost real-time text")
		}
	}
}

func TestGroupsBroadcastsAndNewslettersNeverBecomeMirrors(t *testing.T) {
	for _, fromMe := range []bool{false, true} {
		for _, server := range []string{types.GroupServer, types.BroadcastServer, types.NewsletterServer} {
			evt := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{IsFromMe: fromMe, IsGroup: server == types.GroupServer, Chat: types.NewJID("group", server), Sender: types.NewJID("5511999990000", types.DefaultUserServer)}}, Message: &waE2E.Message{Conversation: proto.String("private")}}
			if translateMessage(evt) != nil {
				t.Fatalf("non-private chat accepted: %s from_me=%t", server, fromMe)
			}
		}
	}
}

func TestApiEchoGuardIsBoundedExpiresAndIsConcurrent(t *testing.T) {
	var guard outgoingEchoes
	now := time.Now()
	guard.remember("api-id", now)
	if !guard.contains("api-id", now) || guard.contains("phone-id", now) {
		t.Fatal("wrong echo guard")
	}
	if guard.contains("api-id", now.Add(outgoingEchoTTL)) {
		t.Fatal("echo cache never expires")
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 600; i++ {
				id := fmt.Sprintf("%d-%d", n, i)
				guard.remember(id, now)
				guard.contains(id, now)
			}
		}(n)
	}
	wg.Wait()
	if len(guard.ids) > outgoingEchoLimit {
		t.Fatal("echo cache is unbounded")
	}
}
