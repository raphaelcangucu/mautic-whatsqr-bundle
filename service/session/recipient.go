package session

import (
	"context"
	"fmt"
	"go.mau.fi/whatsmeow/types"
	"strings"
)

// Brazilian accounts may still use an 8-digit mobile identifier. Always ask
// WhatsApp to confirm the canonical recipient; never send to a guessed JID.
func resolvePhone(ctx context.Context, number string, lookup func(context.Context, []string) ([]types.IsOnWhatsAppResponse, error)) (types.JID, error) {
	candidates := []string{"+" + number}
	if strings.HasPrefix(number, "55") && len(number) == 13 && number[4] == '9' {
		candidates = append(candidates, "+"+number[:4]+number[5:])
	}
	for _, phone := range candidates {
		results, err := lookup(ctx, []string{phone})
		if err != nil {
			return types.EmptyJID, err
		}
		for _, result := range results {
			if strings.TrimPrefix(result.Query, "+") != strings.TrimPrefix(phone, "+") || !result.IsIn || result.JID.IsEmpty() {
				continue
			}
			if result.JID.Server != types.DefaultUserServer && result.JID.Server != types.HiddenUserServer {
				continue
			}
			return result.JID.ToNonAD(), nil
		}
	}
	return types.EmptyJID, fmt.Errorf("session: numero de destino nao confirmado no WhatsApp")
}
