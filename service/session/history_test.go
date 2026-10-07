package session

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func historicalText(id string, fromMe bool, timestamp uint64) *waHistorySync.HistorySyncMsg {
	return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String(id), FromMe: proto.Bool(fromMe)}, MessageTimestamp: proto.Uint64(timestamp), Message: &waE2E.Message{Conversation: proto.String(id)}}}
}

func TestHistoryImportsBothDirectionsInOriginalOrderAndExcludesGroups(t *testing.T) {
	own := types.NewJID("5511999990000", types.DefaultUserServer)
	client := whatsmeow.NewClient(&store.Device{ID: &own, LID: types.NewJID("own-lid", types.HiddenUserServer)}, nil)
	data := &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{
		{ID: proto.String("peer@lid"), PnJID: proto.String("5511988887777@s.whatsapp.net"), Name: proto.String("Vandir"), Messages: []*waHistorySync.HistorySyncMsg{historicalText("out", true, 1700000001), historicalText("in", false, 1700000000)}},
		{ID: proto.String("group@g.us"), Messages: []*waHistorySync.HistorySyncMsg{historicalText("group", false, 1700000000)}},
		{ID: proto.String("status@broadcast"), Messages: []*waHistorySync.HistorySyncMsg{historicalText("status", true, 1700000000)}},
	}}
	var messages []*Inbound
	accepted, skipped := translateHistory(data, client.ParseWebMessage, func(_ *events.Message, msg *Inbound) { messages = append(messages, msg) })
	if accepted != 2 || skipped != 2 || len(messages) != 2 {
		t.Fatalf("history counts accepted=%d skipped=%d", accepted, skipped)
	}
	if messages[0].ID != "in" || messages[0].FromMe || messages[1].ID != "out" || !messages[1].FromMe {
		t.Fatal("wrong history order/direction")
	}
	for _, msg := range messages {
		if !msg.Historical || msg.From != "5511988887777@s.whatsapp.net" || msg.Name != "Vandir" {
			t.Fatalf("history lost context: %+v", msg)
		}
	}
}

func TestHistoryRequestHasBoundedStorageAndNoGroupHistory(t *testing.T) {
	request := fullHistoryRequest("local-request-id", time.Unix(1790000000, 0)).GetProtocolMessage().GetPeerDataOperationRequestMessage()
	if request.GetPeerDataOperationRequestType() != waE2E.PeerDataOperationRequestType_FULL_HISTORY_SYNC_ON_DEMAND {
		t.Fatal("wrong protocol request")
	}
	cfg := request.GetFullHistorySyncOnDemandRequest().GetHistorySyncConfig()
	if cfg.GetSupportGroupHistory() || cfg.GetFullSyncSizeMbLimit() != 64 || cfg.GetFullSyncDaysLimit() != 365 {
		t.Fatal("unbounded history request")
	}
}

func TestHistoryAnchorsRejectGroupsFutureDatesAndUnboundedBatches(t *testing.T) {
	valid := HistoryAnchor{JID: "5511999999999@s.whatsapp.net", ID: "real-id", Timestamp: time.Now().Unix()}
	if err := ValidateHistoryAnchors([]HistoryAnchor{valid}); err != nil {
		t.Fatal(err)
	}
	for _, jid := range []string{"123@g.us", "status@broadcast", "123@newsletter", "5511999999999:3@s.whatsapp.net"} {
		bad := valid
		bad.JID = jid
		if ValidateHistoryAnchors([]HistoryAnchor{bad}) == nil {
			t.Fatalf("non-private anchor accepted: %s", jid)
		}
	}
	bad := valid
	bad.Timestamp = time.Now().Unix() + 3600
	if ValidateHistoryAnchors([]HistoryAnchor{bad}) == nil {
		t.Fatal("future date accepted")
	}
	if ValidateHistoryAnchors(make([]HistoryAnchor, 33)) == nil {
		t.Fatal("unbounded batch accepted")
	}
}
