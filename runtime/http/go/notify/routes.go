package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/providers"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// The paths the notifier's routes are under unless RoutesOptions names
// others: the caller's in-app inbox, and the relays' callbacks.
const (
	DefaultInboxPath = "/api/inbox"
	DefaultRelayPath = "/webhooks"
)

// RoutesOptions are what Routes takes.
type RoutesOptions struct {
	// InboxPath and RelayPath are where the inbox and the relays'
	// callbacks are served; DefaultInboxPath and DefaultRelayPath unless
	// set.
	InboxPath string
	RelayPath string
	// Inbound reads the messages a relay's callback carries: the
	// notifier's Inbound unless set. A database with parties sets the
	// inbound path's (D157), which hands a user's reply to the notifier.
	Inbound func(ctx context.Context, slot string, request providers.WebhookRequest) (any, error)
}

// Routes are the notifier's routes, a handler the app mounts under
// InboxPath and RelayPath (D153, D190):
//
//   - GET {inbox}?state&limit: the caller's notices, newest first, as
//     JSON {data: [notice]}; 401 with no caller.
//   - POST {inbox}/{id}/read and {inbox}/{id}/ack: marks the caller's
//     notice read, or acknowledges it, which stops its escalation; 204,
//     and 404 for a notice that is not the caller's.
//   - POST {relay}/{slot}/inbound: the replies, and on a database with
//     parties the messages, a channel adapter's callback carries, as
//     JSON {data: [outcome]}.
//   - POST {relay}/{slot}/status: the delivery statuses it carries, as
//     JSON {data: {moved}}.
//
// {slot} is a channel slot of the environment, in any case: /webhooks/sms
// is slot SMS. A relay's callback carries no caller: its adapter checks
// the relay's signature, and a sender runs as the principal its verified
// address resolves to. The inbox's caller is the one the auth step put
// on the request (session.GetCaller), so the app mounts the inbox behind
// its auth step and the relays' callbacks outside it.
func (n *Notifier) Routes(opts RoutesOptions) http.Handler {
	inbox := strings.TrimSuffix(opts.InboxPath, "/")
	if inbox == "" {
		inbox = DefaultInboxPath
	}
	relay := strings.TrimSuffix(opts.RelayPath, "/")
	if relay == "" {
		relay = DefaultRelayPath
	}
	read := opts.Inbound
	if read == nil {
		read = func(ctx context.Context, slot string, request providers.WebhookRequest) (any, error) {
			outcomes, err := n.Inbound(ctx, slot, request)
			return RepliesJSON(outcomes), err
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == inbox || strings.HasPrefix(path, inbox+"/"):
			n.serveInbox(w, r, strings.TrimPrefix(path, inbox))
		case strings.HasPrefix(path, relay+"/"):
			n.serveRelay(w, r, strings.TrimPrefix(path, relay+"/"), read)
		default:
			routeProblem(w, http.StatusNotFound, "Not Found", "no such notifier route")
		}
	})
}

func (n *Notifier) serveInbox(w http.ResponseWriter, r *http.Request, rest string) {
	caller := session.GetCaller(r.Context())
	if caller.Anonymous() {
		routeProblem(w, http.StatusUnauthorized, "Unauthorized", "Authentication required")
		return
	}
	recipient := RecipientRef{Kind: string(caller.KindOrUser()), ID: caller.Subject}
	ctx := r.Context()
	if rest == "" || rest == "/" {
		if r.Method != http.MethodGet {
			routeProblem(w, http.StatusMethodNotAllowed, "Method Not Allowed", "the inbox is a GET")
			return
		}
		q := r.URL.Query()
		var states []string
		for _, v := range q["state"] {
			for _, s := range strings.Split(v, ",") {
				if s != "" {
					states = append(states, s)
				}
			}
		}
		limit := 0
		if q.Has("limit") {
			parsed, err := strconv.Atoi(q.Get("limit"))
			if err != nil || parsed <= 0 {
				routeProblem(w, http.StatusBadRequest, "Bad Request", "limit is a positive whole number, not "+q.Get("limit"))
				return
			}
			limit = parsed
		}
		notices, err := n.Inbox(ctx, recipient, states, limit)
		if err != nil {
			n.routeFailure(w, err)
			return
		}
		out := make([]map[string]any, len(notices))
		for i, notice := range notices {
			out[i] = noticeJSON(notice)
		}
		writeRouteJSON(w, http.StatusOK, map[string]any{"data": out})
		return
	}
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	if len(parts) != 2 || (parts[1] != "read" && parts[1] != "ack") {
		routeProblem(w, http.StatusNotFound, "Not Found", "no such inbox route")
		return
	}
	if r.Method != http.MethodPost {
		routeProblem(w, http.StatusMethodNotAllowed, "Method Not Allowed", parts[1]+" is a POST")
		return
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil || id == "" {
		routeProblem(w, http.StatusNotFound, "Not Found", "no such notice of the caller")
		return
	}
	if parts[1] == "read" {
		err = n.MarkRead(ctx, recipient, id)
	} else {
		err = n.Acknowledge(ctx, recipient, id, InApp)
	}
	if err != nil {
		n.routeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (n *Notifier) serveRelay(w http.ResponseWriter, r *http.Request, rest string, read func(context.Context, string, providers.WebhookRequest) (any, error)) {
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || (parts[1] != "inbound" && parts[1] != "status") {
		routeProblem(w, http.StatusNotFound, "Not Found", "no such relay route")
		return
	}
	if r.Method != http.MethodPost {
		routeProblem(w, http.StatusMethodNotAllowed, "Method Not Allowed", "a relay's callback is a POST")
		return
	}
	// A relay's callback is the relay's: its work runs to its end whether
	// or not the relay waits for the answer.
	ctx := context.WithoutCancel(r.Context())
	slot, ok := n.slotNamed(ctx, parts[0])
	if !ok {
		routeProblem(w, http.StatusNotFound, "Not Found", "no channel slot "+parts[0]+" in this environment")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		routeProblem(w, http.StatusBadRequest, "Bad Request", "the body could not be read")
		return
	}
	headers := make(map[string]string, len(r.Header))
	for name, values := range r.Header {
		headers[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	request := providers.WebhookRequest{Headers: headers, Body: string(body)}
	if parts[1] == "status" {
		moved, err := n.Statuses(ctx, slot, request)
		if err != nil {
			n.routeFailure(w, err)
			return
		}
		writeRouteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"moved": moved}})
		return
	}
	outcomes, err := read(ctx, slot, request)
	if err != nil {
		n.routeFailure(w, err)
		return
	}
	if outcomes == nil {
		outcomes = []any{}
	}
	writeRouteJSON(w, http.StatusOK, map[string]any{"data": outcomes})
}

// slotNamed is the channel slot of the environment segment names, in any
// case.
func (n *Notifier) slotNamed(ctx context.Context, segment string) (string, bool) {
	all, err := n.channels(ctx)
	if err != nil {
		return "", false
	}
	for _, c := range all {
		if strings.EqualFold(c.slot, segment) {
			return c.slot, true
		}
	}
	return "", false
}

// routeFailure answers a refusal of the notifier 404, since what it
// refuses is a notice or a slot the caller has none of, and any other
// failure 500, its cause logged and kept from the answer.
func (n *Notifier) routeFailure(w http.ResponseWriter, err error) {
	var refusal *Error
	if errors.As(err, &refusal) {
		routeProblem(w, http.StatusNotFound, "Not Found", refusal.Message)
		return
	}
	n.log.Error("a notifier route failed")
	routeProblem(w, http.StatusInternalServerError, "Internal Server Error", "the notifier could not answer")
}

func noticeJSON(notice Notice) map[string]any {
	at := func(t *time.Time) any {
		if t == nil {
			return nil
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{
		"id": notice.ID, "recipient": notice.Recipient, "recipientKind": notice.RecipientKind, "reference": notice.Reference,
		"eventId": notice.EventID, "eventKind": notice.EventKind, "urgency": notice.Urgency, "subjectTable": notice.SubjectTable,
		"subjectKey": notice.SubjectKey, "title": notice.Title, "body": notice.Body, "payload": notice.Payload, "route": notice.Route,
		"state": notice.State, "escalation": notice.Escalation, "createdAt": at(notice.CreatedAt), "readAt": at(notice.ReadAt),
		"acknowledgedAt": at(notice.AcknowledgedAt), "acknowledgedVia": notice.AcknowledgedVia, "digestedAt": at(notice.DigestedAt),
	}
}

func writeRouteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func routeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": title, "status": status, "detail": detail})
}

// JSON is the outcome as a relay's callback answers it, its members in
// camel case as TypeScript's are; an empty member is left out.
func (o ReplyOutcome) JSON() map[string]any {
	out := map[string]any{"outcome": o.Outcome}
	for name, value := range map[string]string{"reply": o.Reply, "notice": o.Notice, "verb": o.Verb, "echo": o.Echo, "detail": o.Detail} {
		if value != "" {
			out[name] = value
		}
	}
	if o.Principal != nil {
		principal := map[string]any{"kind": o.Principal.Kind, "id": o.Principal.ID}
		if o.Principal.Table != "" {
			principal["table"] = o.Principal.Table
		}
		out["principal"] = principal
	}
	return out
}

// RepliesJSON are outcomes as a relay's callback answers them.
func RepliesJSON(outcomes []ReplyOutcome) []map[string]any {
	out := make([]map[string]any, len(outcomes))
	for i, o := range outcomes {
		out[i] = o.JSON()
	}
	return out
}
