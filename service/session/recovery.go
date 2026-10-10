package session

import (
	"context"
	"errors"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// Ask the primary phone for one original message, not a full-history import and
// not a customer-visible send. Acknowledgement does not guarantee recovery.
func (c *whatsmeowClient) RequestMessage(ctx context.Context, anchor HistoryAnchor) error {
	if err := ValidateHistoryAnchors([]HistoryAnchor{anchor}); err != nil {
		return err
	}
	c.mu.Lock()
	if !c.historyRequested.IsZero() && time.Since(c.historyRequested) < 5*time.Minute {
		c.mu.Unlock()
		return ErrHistoryBusy
	}
	c.historyRequested = time.Now()
	c.mu.Unlock()
	chat, _ := types.ParseJID(anchor.JID)
	sender := chat
	if anchor.FromMe {
		if c.cli.Store.ID == nil {
			return errors.New("paired identity unavailable")
		}
		sender = c.cli.Store.ID.ToNonAD()
	}
	_, err := c.cli.SendPeerMessage(ctx, c.cli.BuildUnavailableMessageRequest(chat, sender, anchor.ID))
	return err
}

type messageRecoveryClient interface {
	RequestMessage(context.Context, HistoryAnchor) error
}

func (m *Manager) RequestMessage(ctx context.Context, id string, anchor HistoryAnchor) error {
	if err := ValidateHistoryAnchors([]HistoryAnchor{anchor}); err != nil {
		return err
	}
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
	recovery, ok := client.(messageRecoveryClient)
	if !ok {
		return errors.New("message recovery is not supported by this driver")
	}
	return recovery.RequestMessage(ctx, anchor)
}
