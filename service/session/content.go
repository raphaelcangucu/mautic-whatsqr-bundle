package session

import (
	"fmt"
	"math"
	"strings"
	"unicode"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
)

// Only bounded human-readable fields cross the webhook, never raw protobufs,
// thumbnails, poll keys, full vCards or private media descriptors.
func messageContent(evt *events.Message) (text, kind, reason string) {
	m := evt.Message
	if evt.IsViewOnce || evt.IsViewOnceV2 || evt.IsViewOnceV2Extension {
		return "", "view_once", "view_once"
	}
	if text = m.GetConversation(); text != "" {
		return text, "text", ""
	}
	if text = m.GetExtendedTextMessage().GetText(); text != "" {
		return text, "text", ""
	}
	switch {
	case m.ContactMessage != nil:
		return contactSummary(m.ContactMessage), "contact", ""
	case m.ContactsArrayMessage != nil:
		var parts []string
		for i, contact := range m.ContactsArrayMessage.GetContacts() {
			if i >= 20 {
				break
			}
			if summary := contactSummary(contact); summary != "" {
				parts = append(parts, summary)
			}
		}
		return strings.Join(parts, "\n\n"), "contact", ""
	case m.LocationMessage != nil:
		p := m.LocationMessage
		return locationSummary(p.GetName(), p.GetDegreesLatitude(), p.GetDegreesLongitude()), "location", ""
	case m.LiveLocationMessage != nil:
		p := m.LiveLocationMessage
		return locationSummary(p.GetCaption(), p.GetDegreesLatitude(), p.GetDegreesLongitude()), "location", ""
	case m.ButtonsResponseMessage != nil:
		return safeContent(m.ButtonsResponseMessage.GetSelectedDisplayText(), 2048), "interactive", ""
	case m.TemplateButtonReplyMessage != nil:
		return safeContent(m.TemplateButtonReplyMessage.GetSelectedDisplayText(), 2048), "interactive", ""
	case m.InteractiveResponseMessage != nil:
		return safeContent(m.InteractiveResponseMessage.GetBody().GetText(), 2048), "interactive", ""
	case m.InteractiveMessage != nil:
		return safeContent(m.InteractiveMessage.GetBody().GetText(), 4096), "interactive", ""
	case m.ButtonsMessage != nil:
		return safeContent(m.ButtonsMessage.GetContentText(), 4096), "interactive", ""
	case m.ListResponseMessage != nil:
		return safeContent(m.ListResponseMessage.GetTitle(), 2048), "interactive", ""
	case m.TemplateMessage != nil:
		p := m.TemplateMessage.GetHydratedTemplate()
		if p == nil {
			p = m.TemplateMessage.GetHydratedFourRowTemplate()
		}
		return safeContent(p.GetHydratedContentText(), 4096), "interactive", ""
	case m.ImageMessage != nil:
		return "", "image", ""
	case m.VideoMessage != nil || m.PtvMessage != nil:
		return "", "video", ""
	case m.AudioMessage != nil:
		return "", "audio", ""
	case m.DocumentMessage != nil:
		return "", "document", ""
	case m.StickerMessage != nil:
		return "", "sticker", ""
	}
	for _, poll := range []*waE2E.PollCreationMessage{m.GetPollCreationMessage(), m.GetPollCreationMessageV2(), m.GetPollCreationMessageV3(), m.GetPollCreationMessageV5(), m.GetPollCreationMessageV6()} {
		if poll == nil {
			continue
		}
		parts := []string{safeContent(poll.GetName(), 1024)}
		for i, option := range poll.GetOptions() {
			if i >= 12 {
				break
			}
			if value := safeContent(option.GetOptionName(), 256); value != "" {
				parts = append(parts, "• "+value)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n")), "poll", ""
	}
	return "", "unknown", "unsupported_type"
}

func safeContent(value string, limit int) string {
	value = strings.Map(func(c rune) rune {
		if (unicode.IsControl(c) && c != '\n' && c != '\t') || unicode.Is(unicode.Cf, c) {
			return -1
		}
		return c
	}, value)
	runes := []rune(value)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return strings.TrimSpace(string(runes))
}

func contactSummary(contact *waE2E.ContactMessage) string {
	parts := []string{safeContent(contact.GetDisplayName(), 150)}
	// Only telephone values from bounded vCards; no raw embedded URLs or photos.
	vcard := contact.GetVcard()
	if len(vcard) > 64*1024 {
		return parts[0]
	}
	for _, line := range strings.Split(vcard, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !(strings.EqualFold(key, "TEL") || strings.HasPrefix(strings.ToUpper(key), "TEL;")) {
			continue
		}
		value = strings.TrimPrefix(value, "tel:")
		if len(value) > 32 || len(parts) >= 6 {
			continue
		}
		if strings.Trim(value, "+0123456789 ()-") == "" && strings.ContainsAny(value, "0123456789") {
			parts = append(parts, value)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func locationSummary(name string, lat, lon float64) string {
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return ""
	}
	return strings.TrimSpace(safeContent(name, 256) + "\n" + fmt.Sprintf("https://maps.google.com/?q=%.6f,%.6f", lat, lon))
}
