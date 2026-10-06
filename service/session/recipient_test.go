package session

import (
	"context"
	"errors"
	"go.mau.fi/whatsmeow/types"
	"reflect"
	"testing"
)

func TestResolvePhoneUsesOnlyConfirmedCanonicalBrazilianRecipient(t *testing.T) {
	var calls []string
	jid, err := resolvePhone(context.Background(), "5511999990000", func(_ context.Context, p []string) ([]types.IsOnWhatsAppResponse, error) {
		calls = append(calls, p[0])
		if len(calls) == 1 {
			return []types.IsOnWhatsAppResponse{{Query: p[0], IsIn: false}}, nil
		}
		return []types.IsOnWhatsAppResponse{{Query: p[0], IsIn: true, JID: types.NewJID("123456", types.HiddenUserServer)}}, nil
	})
	if err != nil || jid.User != "123456" || !reflect.DeepEqual(calls, []string{"+5511999990000", "+551199990000"}) {
		t.Fatal(jid, err, calls)
	}
}
func TestResolvePhoneNeverGuessesOrUsesAnotherQuery(t *testing.T) {
	jid, err := resolvePhone(context.Background(), "5511987654321", func(_ context.Context, p []string) ([]types.IsOnWhatsAppResponse, error) {
		return []types.IsOnWhatsAppResponse{{Query: "+wrong", IsIn: true, JID: types.NewJID("wrong", types.DefaultUserServer)}}, nil
	})
	if err == nil || !jid.IsEmpty() {
		t.Fatal("unsafe recipient")
	}
}
func TestResolvePhoneDoesNotRetryATransportFailure(t *testing.T) {
	calls := 0
	_, err := resolvePhone(context.Background(), "5511999990000", func(context.Context, []string) ([]types.IsOnWhatsAppResponse, error) {
		calls++
		return nil, errors.New("offline")
	})
	if err == nil || calls != 1 {
		t.Fatal(err, calls)
	}
}
