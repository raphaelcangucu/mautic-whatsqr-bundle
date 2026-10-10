package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
	"strings"
	"time"
)

func (c *whatsmeowClient) SendAudio(ctx context.Context, to string, data []byte, mime, requestID string) (string, error) {
	dest, err := toJID(to)
	if err != nil {
		return "", fmt.Errorf("audio recipient: %w", err)
	}
	if !strings.Contains(to, "@") {
		dest, err = resolvePhone(ctx, dest.User, c.cli.IsOnWhatsApp)
		if err != nil {
			return "", fmt.Errorf("audio recipient lookup: %w", err)
		}
	}
	upload, err := c.cli.Upload(ctx, data, whatsmeow.MediaAudio)
	if err != nil {
		return "", fmt.Errorf("audio upload: %w", err)
	}
	// Queue retries use the same WhatsApp message ID, including after a service restart.
	id := audioMessageID(c.mediaSession, to, requestID)
	c.ownSends.remember(string(id), time.Now())
	message := &waE2E.Message{AudioMessage: &waE2E.AudioMessage{URL: proto.String(upload.URL), DirectPath: proto.String(upload.DirectPath), MediaKey: upload.MediaKey, FileSHA256: upload.FileSHA256, FileEncSHA256: upload.FileEncSHA256, FileLength: proto.Uint64(upload.FileLength), Mimetype: proto.String(mime), PTT: proto.Bool(false)}}
	response, err := c.cli.SendMessage(ctx, dest, message, whatsmeow.SendRequestExtra{ID: id})
	if err != nil {
		return "", fmt.Errorf("audio send: %w", err)
	}
	if response.ID == "" {
		return "", errors.New("audio receipt missing")
	}
	return string(response.ID), nil
}

func audioMessageID(session, to, requestID string) types.MessageID {
	sum := sha256.Sum256([]byte(session + "\x00" + to + "\x00" + requestID))
	return types.MessageID("3EB0" + strings.ToUpper(hex.EncodeToString(sum[:12])))
}
