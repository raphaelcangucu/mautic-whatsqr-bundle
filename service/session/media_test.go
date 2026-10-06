package session

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/macro-markets/whatsqr/media"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func attachmentEvent() *events.Message {
	return &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Sender: types.NewJID("5511999999999", types.DefaultUserServer), Chat: types.NewJID("5511999999999", types.DefaultUserServer)}, ID: "received-photo"}, Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("foto com legenda"), Mimetype: proto.String("image/png"), FileLength: proto.Uint64(68), DirectPath: proto.String("/v/t62/attachment.enc?token=private"), MediaKey: bytes.Repeat([]byte{7}, 32), FileSHA256: bytes.Repeat([]byte{8}, 32), FileEncSHA256: bytes.Repeat([]byte{9}, 32)}}}
}

func TestAttachmentCapturePersistsOnlyPrivateCryptoAndKeepsCaption(t *testing.T) {
	s, err := media.New(filepath.Join(t.TempDir(), "attachments"))
	if err != nil {
		t.Fatal(err)
	}
	c := &whatsmeowClient{mediaStore: s, mediaSession: "one"}
	evt := attachmentEvent()
	msg := translateMessage(evt)
	c.captureMedia(evt, msg)
	if msg.Unsupported || msg.Text != "foto com legenda" || msg.Attachment == nil || !media.ValidID(msg.Attachment.ID) {
		t.Fatalf("lost attachment %#v", msg)
	}
	record, err := s.Read("one", msg.Attachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ref mediaReference
	if json.Unmarshal(record.Descriptor, &ref) != nil || len(ref.Key) != 32 {
		t.Fatal("crypto not retained privately")
	}
	public, _ := json.Marshal(msg.Attachment)
	if bytes.Contains(public, []byte("direct_path")) || bytes.Contains(public, []byte("attachment.enc")) || bytes.Contains(public, []byte("private")) || bytes.Contains(public, []byte("key")) {
		t.Fatal("private descriptor leaked")
	}
	if _, err = s.Read("two", msg.Attachment.ID); err == nil {
		t.Fatal("cross-account descriptor")
	}
}

func TestAttachmentCaptureHonorsViewOnceAndSizeLimits(t *testing.T) {
	s, _ := media.New(filepath.Join(t.TempDir(), "attachments"))
	c := &whatsmeowClient{mediaStore: s, mediaSession: "one"}
	evt := attachmentEvent()
	evt.IsViewOnce = true
	msg := translateMessage(evt)
	c.captureMedia(evt, msg)
	if msg.Attachment != nil {
		t.Fatal("view-once attachment archived")
	}
	evt = attachmentEvent()
	evt.Message.ImageMessage.FileLength = proto.Uint64(uint64(media.MaxBytes) + 1)
	msg = translateMessage(evt)
	c.captureMedia(evt, msg)
	if msg.Attachment == nil || msg.Attachment.ID != "" || msg.Attachment.Error != "too_large" || msg.Unsupported {
		t.Fatal("large attachment silently discarded")
	}
}

func TestAttachmentKindsAndSafeFilenames(t *testing.T) {
	cases := []struct {
		msg  *waE2E.Message
		kind string
	}{
		{&waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}, "audio"},
		{&waE2E.Message{VideoMessage: &waE2E.VideoMessage{}}, "video"},
		{&waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{}}, "document"},
		{&waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, "sticker"},
	}
	for _, tc := range cases {
		_, kind, _, _ := mediaPart(&events.Message{Message: tc.msg})
		if kind != tc.kind {
			t.Fatalf("kind %s", kind)
		}
	}
	if name := cleanFilename("..\\..\\relatório\r\n.pdf"); name != "relatório.pdf" {
		t.Fatal(name)
	}
}
