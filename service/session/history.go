package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

var ErrHistoryBusy = errors.New("history sync was requested recently; wait before retrying")

// RequestHistory asks the primary phone for the history it can share. An ack
// means requested, not completed: actual messages arrive as HistorySync events.
type HistoryAnchor struct {
	JID       string `json:"jid"`
	ID        string `json:"id"`
	FromMe    bool   `json:"from_me"`
	Timestamp int64  `json:"timestamp"`
}

func ValidateHistoryAnchors(anchors []HistoryAnchor) error {
	if len(anchors) > 32 {
		return errors.New("too many history chats")
	}
	for _, anchor := range anchors {
		jid, err := types.ParseJID(anchor.JID)
		if err != nil || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) || jid.User == "" || jid.Device != 0 || len(anchor.ID) == 0 || len(anchor.ID) > 256 || anchor.Timestamp <= 946684800 || anchor.Timestamp > time.Now().Unix()+300 {
			return errors.New("invalid private history anchor")
		}
	}
	return nil
}

func (c *whatsmeowClient) RequestHistory(ctx context.Context, anchors []HistoryAnchor) error {
	if err := ValidateHistoryAnchors(anchors); err != nil {
		return err
	}
	c.mu.Lock()
	if !c.historyRequested.IsZero() && time.Since(c.historyRequested) < 5*time.Minute {
		c.mu.Unlock()
		return ErrHistoryBusy
	}
	c.historyRequested = time.Now()
	c.mu.Unlock()
	requested := 0
	var lastError error
	for _, anchor := range anchors {
		jid, _ := types.ParseJID(anchor.JID)
		info := &types.MessageInfo{MessageSource: types.MessageSource{Chat: jid, IsFromMe: anchor.FromMe}, ID: types.MessageID(anchor.ID), Timestamp: time.Unix(anchor.Timestamp, 0)}
		if _, err := c.cli.SendPeerMessage(ctx, c.cli.BuildHistorySyncRequest(info, 50)); err != nil {
			lastError = err
			break
		}
		requested++
	}
	if ctx.Err() == nil {
		if _, err := c.cli.SendPeerMessage(ctx, fullHistoryRequest(string(c.cli.GenerateMessageID()), time.Now())); err != nil {
			lastError = err
		} else {
			requested++
		}
	}
	c.logConnection("History requested (known_private_chats=%d, requests_accepted=%d)", len(anchors), requested)
	if requested == 0 {
		return fmt.Errorf("no history request accepted: %w", lastError)
	}
	return nil
}

func fullHistoryRequest(requestID string, now time.Time) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_PEER_DATA_OPERATION_REQUEST_MESSAGE.Enum(),
		PeerDataOperationRequestMessage: &waE2E.PeerDataOperationRequestMessage{
			PeerDataOperationRequestType: waE2E.PeerDataOperationRequestType_FULL_HISTORY_SYNC_ON_DEMAND.Enum(),
			FullHistorySyncOnDemandRequest: &waE2E.PeerDataOperationRequestMessage_FullHistorySyncOnDemandRequest{
				RequestMetadata:               &waE2E.FullHistorySyncOnDemandRequestMetadata{RequestID: proto.String(requestID)},
				HistorySyncConfig:             &waCompanionReg.DeviceProps_HistorySyncConfig{FullSyncDaysLimit: proto.Uint32(365), FullSyncSizeMbLimit: proto.Uint32(64), StorageQuotaMb: proto.Uint32(128), SupportGroupHistory: proto.Bool(false), OnDemandReady: proto.Bool(true), CompleteOnDemandReady: proto.Bool(true)},
				FullHistorySyncOnDemandConfig: &waE2E.FullHistorySyncOnDemandConfig{HistoryFromTimestamp: proto.Uint64(uint64(now.AddDate(-1, 0, 0).Unix())), HistoryDurationDays: proto.Uint32(365)},
			},
		},
	}}
}

// Filter groups/broadcasts before parsing, retaining both directions and exact
// original timestamps. Sorting is per chat and bounded by the supplied blob.
func translateHistory(data *waHistorySync.HistorySync, parse func(types.JID, *waWeb.WebMessageInfo) (*events.Message, error), accept func(*events.Message, *Inbound), resolvers ...func(types.JID) types.JID) (accepted, skipped int) {
	if data == nil {
		return
	}
	phones := make(map[types.JID]types.JID)
	for _, mapping := range data.GetPhoneNumberToLidMappings() {
		lid, _ := types.ParseJID(mapping.GetLidJID())
		phone, _ := types.ParseJID(mapping.GetPnJID())
		if lid.Server == types.HiddenUserServer && phone.Server == types.DefaultUserServer {
			phones[lid.ToNonAD()] = phone.ToNonAD()
		}
	}
	for _, conversation := range data.GetConversations() {
		chat, err := types.ParseJID(conversation.GetID())
		if err != nil || (chat.Server != types.DefaultUserServer && chat.Server != types.HiddenUserServer) {
			skipped += len(conversation.GetMessages())
			continue
		}
		phone, _ := types.ParseJID(conversation.GetPnJID())
		if phone.Server != types.DefaultUserServer && chat.Server == types.HiddenUserServer {
			phone = phones[chat.ToNonAD()]
			if phone.Server != types.DefaultUserServer && len(resolvers) > 0 {
				phone = resolvers[0](chat)
			}
		}
		messages := append([]*waHistorySync.HistorySyncMsg(nil), conversation.GetMessages()...)
		sort.SliceStable(messages, func(i, j int) bool {
			return messages[i].GetMessage().GetMessageTimestamp() < messages[j].GetMessage().GetMessageTimestamp()
		})
		for _, historyMessage := range messages {
			evt, err := parse(chat, historyMessage.GetMessage())
			if err != nil || evt == nil || evt.Info.ID == "" || evt.Info.Timestamp.Unix() <= 946684800 {
				skipped++
				continue
			}
			if phone.Server == types.DefaultUserServer {
				if evt.Info.IsFromMe {
					evt.Info.RecipientAlt = phone
				} else {
					evt.Info.SenderAlt = phone
				}
			}
			msg := translateMessage(evt)
			if msg == nil {
				skipped++
				continue
			}
			msg.Historical = true
			// Names from chat metadata identify the peer even for outbound history.
			if conversation.GetName() != "" {
				msg.Name = conversation.GetName()
			}
			accept(evt, msg)
			accepted++
		}
	}
	return
}

type historyClient interface {
	RequestHistory(context.Context, []HistoryAnchor) error
}

func (m *Manager) RequestHistory(ctx context.Context, id string, anchors []HistoryAnchor) error {
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
	history, ok := client.(historyClient)
	if !ok {
		return errors.New("history sync is not supported by this driver")
	}
	return history.RequestHistory(ctx, anchors)
}
