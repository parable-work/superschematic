package inbound

import (
	"context"
	"errors"

	"github.com/parable-work/superschematic/runtime/http/go/notify"
	"github.com/parable-work/superschematic/runtime/http/go/providers"
)

// JSON is the outcome as a relay's callback answers it, its members in
// camel case as TypeScript's are; an empty member is left out.
func (o Outcome) JSON() map[string]any {
	out := map[string]any{"outcome": o.Outcome}
	for name, value := range map[string]string{"message": o.Message, "conversation": o.Conversation, "grant": o.Grant, "detail": o.Detail} {
		if value != "" {
			out[name] = value
		}
	}
	if o.Party != nil {
		out["party"] = map[string]any{"table": o.Party.Table, "key": o.Party.Key}
	}
	if o.Reply != nil {
		out["reply"] = o.Reply.JSON()
	}
	return out
}

// RelayInbound is Inbound as the notifier's relay routes read a callback
// (notify.RoutesOptions.Inbound): every message through the inbound path,
// a user's reply among them, answered as JSON.
func (in *Inbound) RelayInbound(ctx context.Context, slot string, request providers.WebhookRequest) (any, error) {
	outcomes, err := in.Inbound(ctx, slot, request)
	var refusal *Error
	if errors.As(err, &refusal) {
		// The routes answer a refusal of the notifier's 404.
		err = &notify.Error{Message: refusal.Message}
	}
	out := make([]map[string]any, len(outcomes))
	for i, o := range outcomes {
		out[i] = o.JSON()
	}
	return out, err
}
