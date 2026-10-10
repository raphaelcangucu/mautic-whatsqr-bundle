package session

import "context"

// Optional extension preserves all existing text-only clients and mocks.
type audioClient interface {
	SendAudio(context.Context, string, []byte, string, string) (string, error)
}

func (m *Manager) SendAudio(ctx context.Context, id, to string, data []byte, mime, requestID string) (string, error) {
	lv, err := m.find(id)
	if err != nil {
		return "", err
	}
	var client Client
	if err = lv.ask(func() {
		if lv.state.State() == Connected {
			client = lv.client
		}
	}); err != nil {
		return "", err
	}
	if client == nil {
		return "", ErrNotConnected
	}
	audio, ok := client.(audioClient)
	if !ok {
		return "", ErrNotConnected
	}
	return audio.SendAudio(ctx, to, data, mime, requestID)
}
