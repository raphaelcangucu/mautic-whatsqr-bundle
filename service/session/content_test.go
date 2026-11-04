package session

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestContentSummariesDecodeCommonNonTextWithoutPrivatePayloads(t *testing.T) {
	cases := []struct {
		message    *waE2E.Message
		kind, text string
	}{
		{&waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Raphael"), Vcard: proto.String("BEGIN:VCARD\nTEL;waid=5511999999999:+5511999999999\nPHOTO:https://private.invalid/photo\nURL:javascript:bad\nEND:VCARD")}}, "contact", "Raphael\n+5511999999999"},
		{&waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{Contacts: []*waE2E.ContactMessage{{DisplayName: proto.String("Carol")}, {DisplayName: proto.String("Raphael")}}}}, "contact", "Carol\n\nRaphael"},
		{&waE2E.Message{LocationMessage: &waE2E.LocationMessage{Name: proto.String("Local"), DegreesLatitude: proto.Float64(-19.9), DegreesLongitude: proto.Float64(-43.9)}}, "location", "Local\nhttps://maps.google.com/?q=-19.900000,-43.900000"},
		{&waE2E.Message{TemplateButtonReplyMessage: &waE2E.TemplateButtonReplyMessage{SelectedDisplayText: proto.String("Relatório")}}, "interactive", "Relatório"},
		{&waE2E.Message{ListResponseMessage: &waE2E.ListResponseMessage{Title: proto.String("Futebol")}}, "interactive", "Futebol"},
		{&waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("Qual time?"), Options: []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("A")}, {OptionName: proto.String("B")}}, EncKey: []byte("SECRET")}}, "poll", "Qual time?\n• A\n• B"},
	}
	for _, tc := range cases {
		evt := attachmentEvent()
		evt.Message = tc.message
		msg := translateMessage(evt)
		if msg.ContentType != tc.kind || msg.Text != tc.text || msg.Unsupported {
			t.Fatalf("incorrect summary for %s", tc.kind)
		}
		body, _ := json.Marshal(msg)
		if strings.Contains(string(body), "SECRET") || strings.Contains(string(body), "private.invalid") || strings.Contains(string(body), "javascript:") {
			t.Fatal("private/untrusted content leaked")
		}
	}
}

func TestContentViewOnceNeverCopiesBodyCaptionOrAttachment(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		evt := attachmentEvent()
		evt.Message = &waE2E.Message{Conversation: proto.String("private content")}
		evt.IsViewOnce = version == 1
		evt.IsViewOnceV2 = version == 2
		evt.IsViewOnceV2Extension = version == 3
		msg := translateMessage(evt)
		c := &whatsmeowClient{}
		c.captureMedia(evt, msg)
		if msg.Text != "" || !msg.Unsupported || msg.UnsupportedReason != "view_once" || msg.Attachment != nil {
			t.Fatal("view-once content archived")
		}
	}
}

func TestContentCircularVideoUsesExistingSecureMediaPath(t *testing.T) {
	evt := attachmentEvent()
	evt.Message = &waE2E.Message{PtvMessage: &waE2E.VideoMessage{Mimetype: proto.String("video/mp4"), FileLength: proto.Uint64(128)}}
	msg := translateMessage(evt)
	c := &whatsmeowClient{}
	c.captureMedia(evt, msg)
	if msg.ContentType != "video" || msg.Attachment == nil || msg.Attachment.Type != "video" || msg.Unsupported || msg.Attachment.Error != "unavailable" {
		t.Fatal("circular video not classified safely")
	}
}

func TestContentUnknownAndInvalidSummariesRemainUnsupported(t *testing.T) {
	for _, message := range []*waE2E.Message{{}, {LocationMessage: &waE2E.LocationMessage{DegreesLatitude: proto.Float64(math.NaN())}}, {ButtonsResponseMessage: &waE2E.ButtonsResponseMessage{}}} {
		evt := attachmentEvent()
		evt.Message = message
		if msg := translateMessage(evt); msg == nil || !msg.Unsupported {
			t.Fatal("invented readable content")
		}
	}
	if got := safeContent(strings.Repeat("á", 3000)+"\x00", 150); len([]rune(got)) != 150 {
		t.Fatal("unbounded summary")
	}
}

func TestContentWrappedDocumentKeepsCaptionAndViewOnceProtection(t *testing.T) {
	evt := attachmentEvent()
	evt.RawMessage = &waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Caption: proto.String("Relatório"), FileName: proto.String("r.pdf"), FileLength: proto.Uint64(128)}}}}
	evt.UnwrapRaw()
	msg := translateMessage(evt)
	c := &whatsmeowClient{}
	c.captureMedia(evt, msg)
	if msg.Text != "Relatório" || msg.Attachment == nil || msg.Attachment.Type != "document" {
		t.Fatal("wrapped document lost")
	}
}
