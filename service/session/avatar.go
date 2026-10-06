package session

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	_ "golang.org/x/image/webp"
)

const MaxAvatarBytes = 256 << 10

var ErrAvatarUnavailable = errors.New("session: profile photo unavailable")

// Optional capability: adding avatars does not change the messaging driver contract.
type ProfileImageClient interface {
	ProfileImage(context.Context, string) (ProfileImage, error)
}

type ProfileImage struct {
	Data []byte
	MIME string
}

func (m *Manager) ProfileImage(ctx context.Context, id, recipient string) (ProfileImage, error) {
	lv, err := m.find(id)
	if err != nil {
		return ProfileImage{}, err
	}
	var client Client
	if err = lv.ask(func() {
		if lv.state.State() == Connected {
			client = lv.client
		}
	}); err != nil {
		return ProfileImage{}, err
	}
	if client == nil {
		return ProfileImage{}, ErrNotConnected
	}
	provider, ok := client.(ProfileImageClient)
	if !ok {
		return ProfileImage{}, ErrAvatarUnavailable
	}
	// Network I/O stays outside the session goroutine and the manager lock.
	return provider.ProfileImage(ctx, recipient)
}

func validProfileRecipient(recipient string) bool {
	if recipient == "" || len(recipient) > 64 {
		return false
	}
	user, server, isJID := strings.Cut(recipient, "@")
	if isJID && server != types.DefaultUserServer && server != types.HiddenUserServer {
		return false
	}
	if !isJID && (len(user) < 8 || len(user) > 15) {
		return false
	}
	for _, digit := range user {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return user != ""
}

func (c *whatsmeowClient) ProfileImage(ctx context.Context, recipient string) (ProfileImage, error) {
	if !validProfileRecipient(recipient) {
		return ProfileImage{}, ErrAvatarUnavailable
	}
	jid, err := toJID(recipient)
	if err != nil {
		return ProfileImage{}, err
	}
	if !strings.Contains(recipient, "@") {
		jid, err = resolvePhone(ctx, jid.User, c.cli.IsOnWhatsApp)
		if err != nil {
			return ProfileImage{}, err
		}
	}
	info, err := c.cli.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{Preview: true})
	if errors.Is(err, whatsmeow.ErrProfilePictureNotSet) || errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized) || (err == nil && info == nil) {
		return ProfileImage{}, ErrAvatarUnavailable
	}
	if err != nil {
		return ProfileImage{}, err
	}
	client := &http.Client{
		Timeout: 4 * time.Second,
		// Do not follow redirects from an otherwise trusted WhatsApp CDN.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return downloadProfileImage(ctx, info.URL, client)
}

func allowedProfileURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{"whatsapp.net", "whatsapp.com", "fbcdn.net", "fbsbx.com"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func downloadProfileImage(ctx context.Context, raw string, client *http.Client) (ProfileImage, error) {
	if !allowedProfileURL(raw) {
		return ProfileImage{}, ErrAvatarUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return ProfileImage{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ProfileImage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > MaxAvatarBytes {
		return ProfileImage{}, ErrAvatarUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxAvatarBytes+1))
	if err != nil {
		return ProfileImage{}, err
	}
	if len(data) > MaxAvatarBytes {
		return ProfileImage{}, ErrAvatarUnavailable
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 2048 || config.Height > 2048 {
		return ProfileImage{}, ErrAvatarUnavailable
	}
	mime := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "webp": "image/webp"}[format]
	if mime == "" {
		return ProfileImage{}, ErrAvatarUnavailable
	}
	return ProfileImage{Data: data, MIME: mime}, nil
}
