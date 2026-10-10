package session

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"path"
	"strings"
	"unicode"

	"github.com/macro-markets/whatsqr/media"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

// Only this safe summary is included in the signed webhook. Direct paths and
// media encryption keys remain in the private service attachment store.
type Attachment struct {
	ID    string `json:"id,omitempty"`
	Type  string `json:"type"`
	Name  string `json:"filename,omitempty"`
	MIME  string `json:"mime_type,omitempty"`
	Size  uint64 `json:"file_size,omitempty"`
	Error string `json:"error,omitempty"`
}

type mediaClient interface {
	setMediaStore(string, *media.Store)
	downloadMedia(context.Context, media.Record, *media.LimitedFile) error
}

func WithMediaStore(factory func(string) (Client, error), storage *media.Store) func(string) (Client, error) {
	return func(id string) (Client, error) {
		client, err := factory(id)
		if err == nil {
			if c, ok := client.(mediaClient); ok {
				c.setMediaStore(id, storage)
			}
		}
		return client, err
	}
}

type mediaReference struct {
	Path         string              `json:"path"`
	Key          []byte              `json:"key"`
	SHA          []byte              `json:"sha"`
	EncryptedSHA []byte              `json:"encrypted_sha"`
	MediaType    whatsmeow.MediaType `json:"media_type"`
}

func (r *mediaReference) GetDirectPath() string             { return r.Path }
func (r *mediaReference) GetMediaKey() []byte               { return r.Key }
func (r *mediaReference) GetFileSHA256() []byte             { return r.SHA }
func (r *mediaReference) GetFileEncSHA256() []byte          { return r.EncryptedSHA }
func (r *mediaReference) GetMediaType() whatsmeow.MediaType { return r.MediaType }

type downloadable interface {
	whatsmeow.DownloadableMessage
	GetFileLength() uint64
	GetMimetype() string
}

func mediaPart(evt *events.Message) (downloadable, string, string, string) {
	if evt == nil || evt.Message == nil || evt.IsViewOnce || evt.IsViewOnceV2 || evt.IsViewOnceV2Extension {
		return nil, "", "", ""
	}
	m := evt.Message
	switch {
	case m.ImageMessage != nil:
		return m.ImageMessage, "image", "", m.ImageMessage.GetCaption()
	case m.VideoMessage != nil:
		return m.VideoMessage, "video", "", m.VideoMessage.GetCaption()
	case m.PtvMessage != nil:
		return m.PtvMessage, "video", "", m.PtvMessage.GetCaption()
	case m.AudioMessage != nil:
		return m.AudioMessage, "audio", "", ""
	case m.DocumentMessage != nil:
		return m.DocumentMessage, "document", m.DocumentMessage.GetFileName(), m.DocumentMessage.GetCaption()
	case m.StickerMessage != nil:
		return m.StickerMessage, "sticker", "", ""
	default:
		return nil, "", "", ""
	}
}

func cleanFilename(value string) string {
	value = path.Base(strings.ReplaceAll(value, "\\", "/"))
	value = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
			return -1
		}
		return c
	}, value)
	if value == "." || value == "/" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > 160 {
		runes = runes[:160]
	}
	return string(runes)
}

func (c *whatsmeowClient) setMediaStore(id string, storage *media.Store) {
	c.mediaSession = id
	c.mediaStore = storage
}

func (c *whatsmeowClient) captureMedia(evt *events.Message, inbound *Inbound) {
	part, kind, name, caption := mediaPart(evt)
	if part == nil {
		return
	}
	inbound.Text = caption
	inbound.Unsupported = false
	inbound.ContentType = kind
	inbound.UnsupportedReason = ""
	a := &Attachment{Type: kind, Name: cleanFilename(name), MIME: part.GetMimetype(), Size: part.GetFileLength()}
	inbound.Attachment = a
	if _, _, err := mime.ParseMediaType(a.MIME); a.MIME != "" && (err != nil || len(a.MIME) > 256) {
		a.MIME = ""
		a.Error = "unavailable"
		return
	}
	if c.mediaStore == nil {
		a.Error = "unavailable"
		return
	}
	if a.Size == 0 || a.Size > uint64(media.MaxBytes) {
		a.Error = "too_large"
		return
	}
	ref := mediaReference{Path: part.GetDirectPath(), Key: part.GetMediaKey(), SHA: part.GetFileSHA256(), EncryptedSHA: part.GetFileEncSHA256(), MediaType: whatsmeow.GetMediaType(part)}
	if !validMediaReference(ref) {
		a.Error = "unavailable"
		return
	}
	descriptor, err := json.Marshal(ref)
	if err != nil {
		a.Error = "unavailable"
		return
	}
	a.ID, err = c.mediaStore.Put(c.mediaSession, inbound.ID, inbound.From, media.Record{Type: kind, Name: a.Name, MIME: a.MIME, Size: a.Size, Descriptor: descriptor, SHA256: hex.EncodeToString(ref.SHA)})
	if errors.Is(err, media.ErrFull) {
		a.Error = "storage_full"
	} else if err != nil {
		a.Error = "unavailable"
	}
}

func validMediaReference(r mediaReference) bool {
	return strings.HasPrefix(r.Path, "/") && !strings.HasPrefix(r.Path, "//") && len(r.Path) <= 4096 && !strings.ContainsAny(r.Path, "\r\n#") && len(r.Key) == 32 && len(r.SHA) == 32 && len(r.EncryptedSHA) == 32 && (r.MediaType == whatsmeow.MediaImage || r.MediaType == whatsmeow.MediaAudio || r.MediaType == whatsmeow.MediaVideo || r.MediaType == whatsmeow.MediaDocument)
}

func (c *whatsmeowClient) downloadMedia(ctx context.Context, record media.Record, file *media.LimitedFile) error {
	var ref mediaReference
	if json.Unmarshal(record.Descriptor, &ref) != nil || !validMediaReference(ref) {
		return media.ErrUnavailable
	}
	return c.cli.DownloadToFile(ctx, &ref, file)
}

func (m *Manager) DownloadMedia(ctx context.Context, id string, record media.Record, file *media.LimitedFile) error {
	lv, err := m.find(id)
	if err != nil {
		return err
	}
	var client Client
	if err = lv.ask(func() {
		if lv.state.State() == Connected {
			client = lv.client
		}
	}); err != nil {
		return err
	}
	if client == nil {
		return ErrNotConnected
	}
	provider, ok := client.(mediaClient)
	if !ok {
		return media.ErrUnavailable
	}
	return provider.downloadMedia(ctx, record, file)
}
