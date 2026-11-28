// Package inbound is D157's outside parties and inbound messages in Go
// (D192): one path every inbound message takes, conversations whose
// messages are rows, and sending to a party as a permission. It is the
// TypeScript runtime's inbound.ts, with its tables, statements, states and
// refusals, and keeps to the points the Rust port (D193) settled.
//
// The path. An adapter's @webhook request (Inbound) gives its messages,
// each of which Receive takes:
//  1. The sender's address is normalised as its channel's kind of address.
//  2. An email whose adapter does not report DMARC pass, or reports SPF or
//     DKIM failing, is from an unknown sender: the adapter reports, the
//     core decides (EmailAuthenticated). An email to another address than
//     the deployment's inbound address is not the deployment's, and is
//     dropped.
//  3. An opt-out or opt-in keyword the adapter read is recorded on every
//     identity of the address, and nothing is sent to an address that
//     opted out.
//  4. The address resolves through the channel identities (D153): a
//     user's is a reply, the notifier's, as is; a party's whose grants are
//     active is a row of the party's conversation on the channel, its
//     files filed to it, and a run request { trigger: "inbound", party,
//     message, grant } on the @runRequests queue, which an agent run (D158)
//     takes, acting for the party; a party's with no active grant is on
//     the party's timeline and held in triage; a party's whose grants have
//     all ended since it was verified (it lapsed) is held in triage as no
//     one's until it is verified again; and no one's, a shared address no
//     thread settles, or a user's and a party's at once, is held in triage,
//     given to no one.
//
// Two parties may share an address. A message from it belongs to the
// party whose conversation it continues: an email's In-Reply-To or
// References, or the service's id of the text it answers, names a message
// of that party's conversation. A user never shares.
//
// Sending to a party (Send) is a permission, held to the sender's read of
// the party's row, to the party's identity on the channel (verified or
// entered, not opted out, not lapsed), to a rate limit per party, and to
// an approval policy the app or the agent dispatcher plugs in
// (SendApproval): allow sends, deny refuses, ask holds the message
// awaiting approval until Release sends it.
//
// The conversations' tables are the loader's, read and written by
// statements over the columns the generated ORM's definitions name, as
// the notifier reads its own (D190); a party's row and a row a message is
// filed onto are read through their repository as the caller, so the
// reads keep to its audience and grants (D156, D180). Postgres alone, as
// the notifier is.
package inbound

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/notify"
	"github.com/parable-work/superschematic/runtime/http/go/providers"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/http/go/worker"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/notifications"
	"github.com/parable-work/superschematic/runtime/schema/go/orchestration"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

// What became of an inbound message (InboundDisposition).
const (
	Reply    = "reply"
	Run      = "run"
	Timeline = "timeline"
	Unknown  = "unknown"
	Lapsed   = "lapsed"
	OptOut   = "optOut"
	Verified = "verified"
	Ignored  = "ignored"
)

// What a send or a release came to.
const (
	Sent             = "sent"
	Failed           = "failed"
	Refused          = "refused"
	AwaitingApproval = "awaitingApproval"
)

// Decision is what an approval policy decides of a send: send it, refuse
// it, or hold it for approval.
type Decision string

// The decisions of an approval policy.
const (
	Allow Decision = "allow"
	Deny  Decision = "deny"
	Ask   Decision = "ask"
)

// RunRequest is the message of the @runRequests queue an inbound message
// from a party with an active grant puts on it (D157): the party the run
// acts for, the message row that asked for it, and the party's grant the
// run acts under, each key in base62.
type RunRequest struct {
	Trigger string                 `json:"trigger"`
	Party   orchestration.PartyRef `json:"party"`
	Message string                 `json:"message"`
	Grant   string                 `json:"grant"`
}

// ReceivedMessage is what Receive takes: a message an adapter read, and
// the channel kind and slot it came on.
type ReceivedMessage struct {
	providers.InboundMessage
	Channel string
	Slot    string
}

// Outcome is what Receive came to.
type Outcome struct {
	Outcome string
	// Message is the message's row; "" for a reply (the notifier's) and an
	// email not to the deployment.
	Message      string
	Conversation string
	// Party is the party it came from, when one was settled.
	Party *orchestration.PartyRef
	// Grant is the grant its run request names.
	Grant string
	// Reply is a user's reply, as the notifier answered it.
	Reply  *notify.ReplyOutcome
	Detail string
}

// SendInput is what Send takes.
type SendInput struct {
	// As sends: a user, an agent run; it holds the send permission, and
	// reads the party's row.
	As      orchestration.Principal
	Party   orchestration.PartyRef
	Channel string
	Body    string
	Subject string
	// About are the rows it is about, which list it in their backlinks.
	About []rowrefs.RowRef
}

// SendOutcome is what Send and Release came to.
type SendOutcome struct {
	Outcome string `json:"outcome"`
	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// SendApproval is the approval policy of a send (D157, D158): the app's,
// or the agent dispatcher's, which holds a send as an agent run to its
// autonomy.
type SendApproval interface {
	ApproveSend(ctx context.Context, input SendInput) (Decision, error)
}

// ApprovalFunc is a SendApproval of a function.
type ApprovalFunc func(ctx context.Context, input SendInput) (Decision, error)

// ApproveSend calls f.
func (f ApprovalFunc) ApproveSend(ctx context.Context, input SendInput) (Decision, error) {
	return f(ctx, input)
}

// RateLimit is how many messages a party may be sent within Seconds.
type RateLimit struct {
	Messages int
	Seconds  int
}

// Bucket is where an inbound message's files go: a bucket attachments
// sign URLs for (rowrefs.Bucket) that the path writes an object to
// itself. BucketOf makes one of the HTTP runtime's bucket.Bucket.
type Bucket interface {
	rowrefs.Bucket
	PutObject(ctx context.Context, name, contentType string, body []byte) error
}

// Options are what New takes.
type Options struct {
	// Notifier is the notifier (D153), whose channel identities senders
	// resolve through and to which a user's reply goes.
	Notifier *notify.Notifier
	// Providers are the providers an environment configures (D159): every
	// channel slot is a channel the path sends and receives on.
	Providers *providers.Providers
	// Bucket is where an inbound message's files go, through the messages
	// table's @attachments; files are dropped, and the message says so,
	// without it.
	Bucket Bucket
	// InboundAddress is the deployment's inbound email address: an email
	// to another is not the deployment's, and an outbound email's replies
	// go to it.
	InboundAddress string
	// SendPermission and TriagePermission are parties.message and
	// parties.triage unless set.
	SendPermission   string
	TriagePermission string
	// Approve is the approval policy of a send; every send is allowed
	// unless set (or SetApproval).
	Approve SendApproval
	// RateLimit is 20 an hour unless set.
	RateLimit RateLimit
	// AddressKinds are the kind of address a channel kind uses, beside
	// the notifier's defaults.
	AddressKinds map[string]string
	// Now is the clock; time.Now unless set.
	Now func() time.Time
	// Logger logs what is dropped; a no-op unless set.
	Logger *zap.Logger
}

// Error is a refusal of an inbound call: no permission, no such party,
// no conversations tables.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func refused(format string, args ...any) error {
	return &Error{Message: fmt.Sprintf(format, args...)}
}

// Inbound is the inbound path, conversations and sends (D157). It is safe
// for concurrent use.
type Inbound struct {
	host          orchestration.Host
	opts          Options
	log           *zap.Logger
	conversations orchestration.Table
	messages      orchestration.Table
	identities    orchestration.Table
	messageType   string
	inboxAddress  string

	approveMu sync.RWMutex
	approve   SendApproval
}

// New is the inbound path over host, the generated ORM's
// Orchestration(): it refuses a database with no conversations or channel
// identities.
func New(host orchestration.Host, opts Options) (*Inbound, error) {
	if host == nil {
		return nil, errors.New("inbound: New needs the database's Orchestration()")
	}
	if opts.Notifier == nil {
		return nil, errors.New("inbound: New needs the notifier, which senders resolve through")
	}
	defs := host.Orchestration()
	conversations, ok := defs.PartOf("conversations")
	messages, ok2 := defs.PartOf("messages")
	if !ok || !ok2 {
		return nil, errors.New(`inbound: the database has no @conversations("conversations") and @conversations("messages") tables`)
	}
	identities, ok := host.Definitions().Parts["channelIdentities"]
	if !ok {
		return nil, errors.New("inbound: the database has no @channelIdentities table")
	}
	in := &Inbound{host: host, opts: opts, log: opts.Logger, conversations: orchestration.Table{Part: conversations}, messages: orchestration.Table{Part: messages}, identities: orchestration.Table{Part: identities}, messageType: messages.Type, approve: opts.Approve}
	if in.log == nil {
		in.log = zap.NewNop()
	}
	if in.opts.SendPermission == "" {
		in.opts.SendPermission = "parties.message"
	}
	if in.opts.TriagePermission == "" {
		in.opts.TriagePermission = "parties.triage"
	}
	if in.opts.RateLimit.Messages <= 0 {
		in.opts.RateLimit = RateLimit{Messages: 20, Seconds: 3600}
	}
	if opts.InboundAddress != "" {
		in.inboxAddress = notify.NormaliseAddress("email", opts.InboundAddress)
	}
	return in, nil
}

// SetApproval sets the approval policy of a send on a path already built:
// the agent dispatcher's, which is built before it (D193's set_approval).
func (in *Inbound) SetApproval(approve SendApproval) {
	in.approveMu.Lock()
	defer in.approveMu.Unlock()
	in.approve = approve
}

func (in *Inbound) approval() SendApproval {
	in.approveMu.RLock()
	defer in.approveMu.RUnlock()
	return in.approve
}

// Host is the database the path runs through.
func (in *Inbound) Host() orchestration.Host { return in.host }

func (in *Inbound) now() time.Time {
	if in.opts.Now != nil {
		return in.opts.Now()
	}
	return time.Now()
}

func (in *Inbound) system(ctx context.Context) context.Context { return in.host.System(ctx) }

func (in *Inbound) addressKindOf(channel string) string {
	if kind, ok := in.opts.AddressKinds[channel]; ok {
		return kind
	}
	return notify.DefaultAddressKind(channel)
}

// partyTable is table's @party definition, refused for a table that is
// none.
func (in *Inbound) partyTable(table string) error {
	if _, ok := in.host.Orchestration().Parties[table]; !ok {
		return refused("%s is no @party table", table)
	}
	return nil
}

func requirePermission(as orchestration.Principal, permission, what string) error {
	for _, held := range as.Permissions {
		if session.Covers(held, permission) {
			return nil
		}
	}
	return refused("%s may not %s: it takes %s", as.Subject, what, permission)
}

// failing are the results of an SPF or DKIM check that fail it.
var failing = map[string]bool{"fail": true, "softfail": true, "permerror": true}

// EmailAuthenticated reports whether an email's authentication, as its
// adapter reported it, lets its sender be trusted (D157): DMARC passed,
// and neither SPF nor DKIM failed. An email that reports none is from an
// unknown sender.
func EmailAuthenticated(authentication *providers.InboundAuthentication) bool {
	if authentication == nil {
		return false
	}
	result := func(value string) string {
		if strings.TrimSpace(value) == "" {
			return "none"
		}
		return strings.ToLower(strings.TrimSpace(value))
	}
	if result(authentication.DMARC) != "pass" {
		return false
	}
	return !failing[result(authentication.SPF)] && !failing[result(authentication.DKIM)]
}

// authenticationText is why an email's authentication does not pass, in a
// line: dmarc=fail spf=pass dkim=none.
func authenticationText(authentication *providers.InboundAuthentication) string {
	if authentication == nil {
		return "the adapter reported no authentication"
	}
	or := func(v string) string {
		if v == "" {
			return "none"
		}
		return v
	}
	return fmt.Sprintf("dmarc=%s spf=%s dkim=%s", or(authentication.DMARC), or(authentication.SPF), or(authentication.DKIM))
}

var code = regexp.MustCompile(`^[0-9A-Za-z]{4,12}$`)

// ---- channels ----

type channelOf struct {
	slot    string
	adapter providers.ChannelAdapter
}

func (in *Inbound) adapterOf(ctx context.Context, slot string) (providers.ChannelAdapter, error) {
	if in.opts.Providers == nil {
		return nil, refused("the inbound path has no providers")
	}
	return in.opts.Providers.Channel(ctx, slot)
}

// slotOfKind is the first slot whose adapter sends on kind and answers
// its health.
func (in *Inbound) slotOfKind(ctx context.Context, kind string) (*channelOf, error) {
	if in.opts.Providers == nil {
		return nil, nil
	}
	for _, configured := range in.opts.Providers.Configured(providers.Channel) {
		adapter, err := in.adapterOf(ctx, configured.Slot)
		if err != nil {
			return nil, err
		}
		if adapter.Kind() != kind {
			continue
		}
		health, err := adapter.Health(ctx)
		if err != nil {
			in.log.Warn("channel health failed", zap.String("slot", configured.Slot), zap.Error(err))
			continue
		}
		if health.OK {
			return &channelOf{slot: configured.Slot, adapter: adapter}, nil
		}
	}
	return nil, nil
}

// ---- conversations and messages ----

// conversationOf is the conversation of party (or of address, for no
// party) on channel, made when there is none.
func (in *Inbound) conversationOf(ctx context.Context, h orchestration.Statements, channel, address string, party *orchestration.PartyRef, subject string) (orchestration.Row, error) {
	t := in.conversations
	w := t.Where().Eq("channelKind", channel)
	if party != nil {
		w.Eq("partyTable", party.Table).Eq("party", party.Key)
	} else {
		w.Null("party").Eq("address", address)
	}
	found, err := orchestration.FindOne(ctx, h, w, "")
	if err != nil || found != nil {
		return found, err
	}
	values := map[string]any{"channelKind": channel, "address": address, "subject": nullable(subject)}
	if party != nil {
		values["partyTable"] = party.Table
		values["party"] = party.Key
	}
	return orchestration.Insert(ctx, h, t, values)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// threadOf is the message an inbound message's thread names: its
// In-Reply-To, its References, newest first, or the service's id of the
// text it answers.
func (in *Inbound) threadOf(ctx context.Context, input ReceivedMessage) (orchestration.Row, error) {
	ids := []string{input.InReplyTo}
	for i := len(input.References) - 1; i >= 0; i-- {
		ids = append(ids, input.References[i])
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		for _, field := range []string{"messageId", "providerMessageId"} {
			found, err := orchestration.FindOne(ctx, in.host, in.messages.Where().Eq(field, id), "")
			if err != nil {
				return nil, err
			}
			if found != nil {
				return found, nil
			}
		}
	}
	return nil, nil
}

// store stores an inbound message's files on its row through the messages
// table's @attachments, and answers what could not be.
func (in *Inbound) store(ctx context.Context, message string, files []providers.InboundAttachment) string {
	if len(files) == 0 {
		return ""
	}
	attachments := in.host.Attachments()
	if attachments == nil || !attachments.Holds(in.messageType) || in.opts.Bucket == nil {
		return fmt.Sprintf("%d file(s) dropped: the inbound path has no bucket or the messages table no @attachments", len(files))
	}
	sys := in.system(ctx)
	var dropped []string
	for _, file := range files {
		sum := sha256.Sum256(file.Bytes)
		err := func() error {
			begun, err := attachments.Begin(sys, rowrefs.BeginInput{To: rowrefs.RowRef{Table: in.messageType, Key: message}, Name: file.Name, MediaType: file.MediaType, Size: int64(len(file.Bytes)), SHA256: hex.EncodeToString(sum[:])}, in.opts.Bucket, 0)
			if err != nil {
				return err
			}
			if err := in.opts.Bucket.PutObject(ctx, begun.Attachment.ObjectKey, file.MediaType, file.Bytes); err != nil {
				return err
			}
			_, err = attachments.Complete(sys, rowrefs.FileRef{Table: in.messageType, ID: begun.Attachment.ID}, in.opts.Bucket)
			return err
		}()
		if err != nil {
			in.log.Warn("inbound file dropped", zap.String("message", message), zap.String("name", file.Name), zap.Error(err))
			dropped = append(dropped, file.Name+": "+err.Error())
		}
	}
	if len(dropped) > 0 {
		return "files dropped: " + strings.Join(dropped, "; ")
	}
	return ""
}

// refer points message at row, when the messages table keeps references
// that may point at it.
func (in *Inbound) refer(ctx context.Context, message string, row rowrefs.RowRef, role string) {
	references := in.host.References()
	if references == nil {
		return
	}
	if _, err := references.Add(in.system(ctx), rowrefs.AddInput{From: rowrefs.RowRef{Table: in.messageType, Key: message}, To: row, Role: role}); err != nil {
		in.log.Warn("reference not added", zap.String("message", message), zap.String("table", row.Table), zap.Error(err))
	}
}

// recordInbound records an inbound message in its conversation, its files
// filed to it, and answers the row and its conversation.
func (in *Inbound) recordInbound(ctx context.Context, input ReceivedMessage, address string, values map[string]any, party *orchestration.PartyRef, conversation orchestration.Row) (orchestration.Row, orchestration.Row, error) {
	at := input.ReceivedAt
	if at.IsZero() {
		at = in.now()
	}
	var row orchestration.Row
	held := conversation
	err := in.host.Transaction(ctx, func(tx notifications.Host) error {
		if held == nil {
			c, err := in.conversationOf(ctx, tx, input.Channel, address, party, input.Subject)
			if err != nil {
				return err
			}
			held = c
		}
		to := ""
		if len(input.To) > 0 {
			to = input.To[0]
		}
		fields := map[string]any{
			"conversation":      held.Str("id"),
			"direction":         "inbound",
			"via":               input.Channel,
			"slot":              nullable(input.Slot),
			"fromAddress":       address,
			"toAddress":         nullable(to),
			"subject":           nullable(input.Subject),
			"body":              input.Body,
			"messageId":         nullable(input.MessageID),
			"inReplyTo":         nullable(input.InReplyTo),
			"providerMessageId": nullable(input.ProviderMessageID),
			"at":                at,
		}
		if input.References != nil {
			fields["threadRefs"] = input.References
		}
		if input.Authentication != nil {
			fields["authentication"] = input.Authentication
		}
		if party != nil {
			fields["partyTable"] = party.Table
			fields["party"] = party.Key
		}
		for k, v := range values {
			fields[k] = v
		}
		created, err := orchestration.Insert(ctx, tx, in.messages, fields)
		if err != nil {
			return err
		}
		row = created
		_, err = orchestration.Update(ctx, tx, in.conversations.Where().Eq("id", held.Str("id")), map[string]any{"lastMessageAt": at})
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	if dropped := in.store(ctx, row.Str("id"), input.Attachments); dropped != "" {
		detail := dropped
		if d := row.Str("detail"); d != "" {
			detail = d + "; " + dropped
		}
		updated, err := orchestration.UpdateID(ctx, in.host, in.messages, row.Str("id"), map[string]any{"detail": detail})
		if err != nil {
			return nil, nil, err
		}
		row = updated
	}
	return row, held, nil
}

func (in *Inbound) held(ctx context.Context, input ReceivedMessage, address, disposition, detail string, identity *notify.ChannelIdentity) (Outcome, error) {
	values := map[string]any{"disposition": disposition, "triage": "open", "detail": detail, "identity": nil, "trust": nil}
	if identity != nil {
		values["identity"] = identity.ID
		values["trust"] = nullable(identity.Trust)
	}
	message, conversation, err := in.recordInbound(ctx, input, address, values, nil, nil)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Outcome: disposition, Message: message.Str("id"), Conversation: conversation.Str("id"), Detail: detail}, nil
}

// fromParty is a party's message: a run request when its grants are
// active, its timeline and triage when not.
func (in *Inbound) fromParty(ctx context.Context, input ReceivedMessage, address string, identity notify.ChannelIdentity, party orchestration.PartyRef, thread orchestration.Row) (Outcome, error) {
	grants, err := in.host.Grants(ctx, party.Key, true, in.now())
	if err != nil {
		return Outcome{}, err
	}
	var grant *access.Grant
	if thread != nil && !thread.IsNull("grant") {
		for i := range grants {
			if orchestration.SameUUID(grants[i].ID, thread.Str("grant")) {
				grant = &grants[i]
				break
			}
		}
	}
	if grant == nil && len(grants) > 0 {
		// Grants come newest first.
		grant = &grants[0]
	}
	// The thread's conversation, when it is this party's on this channel.
	var conversation orchestration.Row
	if thread != nil {
		named, err := orchestration.ByID(ctx, in.host, in.conversations, thread.Str("conversation"))
		if err != nil {
			return Outcome{}, err
		}
		if named != nil && !named.IsNull("party") && orchestration.SameUUID(named.Str("party"), party.Key) && named.Str("channelKind") == input.Channel {
			conversation = named
		}
	}
	base := map[string]any{"identity": identity.ID, "trust": nullable(identity.Trust)}
	partyRow := rowrefs.RowRef{Table: party.Table, Key: party.Key}
	if grant == nil {
		detail := "the party holds no active grant"
		values := map[string]any{"disposition": Timeline, "triage": "open", "detail": detail}
		for k, v := range base {
			values[k] = v
		}
		message, c, err := in.recordInbound(ctx, input, address, values, &party, conversation)
		if err != nil {
			return Outcome{}, err
		}
		in.refer(ctx, message.Str("id"), partyRow, "from")
		return Outcome{Outcome: Timeline, Message: message.Str("id"), Conversation: c.Str("id"), Party: &party, Detail: detail}, nil
	}
	values := map[string]any{"disposition": Run, "grant": grant.ID}
	for k, v := range base {
		values[k] = v
	}
	message, c, err := in.recordInbound(ctx, input, address, values, &party, conversation)
	if err != nil {
		return Outcome{}, err
	}
	in.refer(ctx, message.Str("id"), partyRow, "from")
	request := RunRequest{Trigger: "inbound", Party: party, Message: notify.SameID(message.Str("id")), Grant: notify.SameID(grant.ID)}
	data, err := json.Marshal(request)
	if err != nil {
		return Outcome{}, err
	}
	if err := in.host.EnqueueOn(ctx, orchestration.RunRequests, data, 0); err != nil {
		return Outcome{}, err
	}
	return Outcome{Outcome: Run, Message: message.Str("id"), Conversation: c.Str("id"), Party: &party, Grant: request.Grant}, nil
}

func (in *Inbound) optOut(ctx context.Context, input ReceivedMessage, address, kind string) (Outcome, error) {
	out := input.OptOut == providers.OptOut
	var at any
	if out {
		at = in.now()
	}
	if in.identities.Has("optedOutAt") {
		if _, err := orchestration.Update(ctx, in.host, in.identities.Where().Eq("kind", kind).Eq("address", address).Null("revokedAt"), map[string]any{"optedOutAt": at}); err != nil {
			return Outcome{}, err
		}
	}
	detail := "the address opted in"
	if out {
		detail = "the address opted out"
	}
	message, conversation, err := in.recordInbound(ctx, input, address, map[string]any{"disposition": OptOut, "detail": detail}, nil, nil)
	if err != nil {
		return Outcome{}, err
	}
	said := "opted in"
	if out {
		said = "opted out"
	}
	return Outcome{Outcome: OptOut, Message: message.Str("id"), Conversation: conversation.Str("id"), Detail: said}, nil
}

// Receive takes one inbound message down the path.
func (in *Inbound) Receive(ctx context.Context, input ReceivedMessage) (Outcome, error) {
	kind := in.addressKindOf(input.Channel)
	address := notify.NormaliseAddress(kind, input.From)
	if kind == "email" && in.inboxAddress != "" && len(input.To) > 0 && !slices.ContainsFunc(input.To, func(to string) bool { return notify.NormaliseAddress("email", to) == in.inboxAddress }) {
		return Outcome{Outcome: Ignored, Detail: "not sent to the deployment's inbound address"}, nil
	}
	if input.OptOut != "" {
		return in.optOut(ctx, input, address, kind)
	}
	if kind == "email" && !EmailAuthenticated(input.Authentication) {
		// The adapter reports, the core decides: a sender an email cannot
		// prove is no one's.
		return in.held(ctx, input, address, Unknown, "the email's authentication did not pass: "+authenticationText(input.Authentication), nil)
	}
	resolved, err := in.opts.Notifier.ResolveSender(ctx, kind, address)
	if err != nil {
		return Outcome{}, err
	}
	var users, parties []notify.ChannelIdentity
	for _, identity := range resolved {
		switch identity.PrincipalKind {
		case "user", "":
			users = append(users, identity)
		case "party":
			parties = append(parties, identity)
		}
	}
	if len(users) > 0 && len(parties) == 0 {
		reply, err := in.opts.Notifier.Reply(ctx, notify.ReplyInput{Channel: input.Channel, Slot: input.Slot, From: address, Body: input.Body, ReceivedAt: input.ReceivedAt, ProviderMessageID: input.ProviderMessageID})
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Outcome: Reply, Reply: &reply}, nil
	}
	if len(users) > 0 {
		return in.held(ctx, input, address, Unknown, "the address is a user's and a party's: it resolves to no one", nil)
	}
	var current, lapsed []notify.ChannelIdentity
	for _, identity := range parties {
		gone, err := orchestration.IdentityLapsed(ctx, in.host, standingOf(identity), in.now())
		if err != nil {
			return Outcome{}, err
		}
		if gone {
			lapsed = append(lapsed, identity)
		} else {
			current = append(current, identity)
		}
	}
	var candidates []notify.ChannelIdentity
	seen := map[string]bool{}
	for _, identity := range current {
		key := identity.PrincipalTable + ":" + notify.SameID(identity.Principal)
		if !seen[key] {
			seen[key] = true
			candidates = append(candidates, identity)
		}
	}
	refOf := func(identity notify.ChannelIdentity) orchestration.PartyRef {
		return orchestration.PartyRef{Table: identity.PrincipalTable, Key: notify.SameID(identity.Principal)}
	}
	if len(candidates) == 1 {
		thread, err := in.threadOf(ctx, input)
		if err != nil {
			return Outcome{}, err
		}
		return in.fromParty(ctx, input, address, candidates[0], refOf(candidates[0]), thread)
	}
	if len(candidates) > 1 {
		thread, err := in.threadOf(ctx, input)
		if err != nil {
			return Outcome{}, err
		}
		if thread != nil && !thread.IsNull("party") {
			for _, c := range candidates {
				if orchestration.SameUUID(c.Principal, thread.Str("party")) && c.PrincipalTable == thread.Str("partyTable") {
					return in.fromParty(ctx, input, address, c, refOf(c), thread)
				}
			}
		}
		return in.held(ctx, input, address, Unknown, fmt.Sprintf("the address is shared by %d parties, and no thread names one", len(candidates)), nil)
	}
	body := strings.TrimSpace(input.Body)
	if len(lapsed) > 0 {
		// A code sent to verify the lapsed identity again, replied with.
		if code.MatchString(body) {
			for _, identity := range lapsed {
				ok, err := in.opts.Notifier.ConfirmIdentity(ctx, identity.ID, body)
				if err != nil {
					return Outcome{}, err
				}
				if ok {
					party := refOf(identity)
					message, conversation, err := in.recordInbound(ctx, input, address, map[string]any{"disposition": Verified, "identity": identity.ID, "trust": "verified", "detail": "the identity is verified again"}, &party, nil)
					if err != nil {
						return Outcome{}, err
					}
					return Outcome{Outcome: Verified, Message: message.Str("id"), Conversation: conversation.Str("id"), Party: &party}, nil
				}
			}
		}
		return in.held(ctx, input, address, Lapsed, "the identity's grants have all ended since it was verified: verify it again before it is trusted", &lapsed[0])
	}
	// No identity: a code that verifies a new one is the notifier's.
	if code.MatchString(body) {
		reply, err := in.opts.Notifier.Reply(ctx, notify.ReplyInput{Channel: input.Channel, Slot: input.Slot, From: address, Body: input.Body})
		if err != nil {
			return Outcome{}, err
		}
		if reply.Outcome == "verified" {
			var party *orchestration.PartyRef
			if reply.Principal != nil && reply.Principal.Kind == "party" && reply.Principal.Table != "" {
				party = &orchestration.PartyRef{Table: reply.Principal.Table, Key: notify.SameID(reply.Principal.ID)}
			}
			message, conversation, err := in.recordInbound(ctx, input, address, map[string]any{"disposition": Verified, "detail": "the address is verified"}, party, nil)
			if err != nil {
				return Outcome{}, err
			}
			return Outcome{Outcome: Verified, Message: message.Str("id"), Conversation: conversation.Str("id"), Party: party}, nil
		}
	}
	return in.held(ctx, input, address, Unknown, "no verified identity holds the address", nil)
}

func standingOf(identity notify.ChannelIdentity) orchestration.IdentityStanding {
	return orchestration.IdentityStanding{PrincipalKind: identity.PrincipalKind, Principal: identity.Principal, VerifiedAt: identity.VerifiedAt, CreatedAt: identity.CreatedAt}
}

// Inbound is the messages an adapter's @webhook request carries, each
// through Receive.
func (in *Inbound) Inbound(ctx context.Context, slot string, request providers.WebhookRequest) ([]Outcome, error) {
	adapter, err := in.adapterOf(ctx, slot)
	if err != nil {
		return nil, err
	}
	reader, ok := adapter.(providers.ChannelInboundReader)
	if !ok {
		return nil, nil
	}
	messages, err := reader.Inbound(ctx, request)
	if err != nil {
		return nil, err
	}
	out := make([]Outcome, 0, len(messages))
	for _, message := range messages {
		outcome, err := in.Receive(ctx, ReceivedMessage{InboundMessage: message, Channel: adapter.Kind(), Slot: slot})
		if err != nil {
			return out, err
		}
		out = append(out, outcome)
	}
	return out, nil
}

// ---- sending ----

// destinationOf is the party's identity a send on channel goes to, or why
// there is none.
func (in *Inbound) destinationOf(ctx context.Context, party orchestration.PartyRef, channel string) (orchestration.Row, string, error) {
	kind := in.addressKindOf(channel)
	if !in.identities.Holds("principal", party.Key) {
		return nil, fmt.Sprintf("the party has no verified %s address", kind), nil
	}
	rows, err := orchestration.Find(ctx, in.host, in.identities.Where().Eq("principalKind", "party").Eq("principal", party.Key).Eq("kind", kind).In("trust", "verified", "entered").Null("revokedAt"), "", 10)
	if err != nil {
		return nil, "", err
	}
	if len(rows) == 0 {
		return nil, fmt.Sprintf("the party has no verified %s address", kind), nil
	}
	var open []orchestration.Row
	for _, r := range rows {
		if r.IsNull("optedOutAt") {
			open = append(open, r)
		}
	}
	if len(open) == 0 {
		return nil, "the address opted out", nil
	}
	for _, r := range open {
		gone, err := orchestration.IdentityLapsed(ctx, in.host, orchestration.IdentityStanding{PrincipalKind: r.Str("principalKind"), Principal: r.Str("principal"), VerifiedAt: r.Time("verifiedAt"), CreatedAt: r.Time("createdAt")}, in.now())
		if err != nil {
			return nil, "", err
		}
		if !gone {
			return r, "", nil
		}
	}
	return nil, "the party's address lapsed when its grants ended: verify it again", nil
}

func (in *Inbound) domainOf() string {
	if i := strings.LastIndex(in.inboxAddress, "@"); i >= 0 {
		return in.inboxAddress[i+1:]
	}
	return "inbound.invalid"
}

// deliver sends an outbound message's row through a channel of its kind.
func (in *Inbound) deliver(ctx context.Context, row orchestration.Row) (SendOutcome, error) {
	channel := row.Str("via")
	id := row.Str("id")
	found, err := in.slotOfKind(ctx, channel)
	if err != nil {
		return SendOutcome{}, err
	}
	if found == nil {
		detail := fmt.Sprintf("no %s channel is up", channel)
		if _, err := orchestration.UpdateID(ctx, in.host, in.messages, id, map[string]any{"state": Failed, "detail": detail}); err != nil {
			return SendOutcome{}, err
		}
		return SendOutcome{Outcome: Failed, Message: id, Detail: detail}, nil
	}
	message := providers.OutboundMessage{To: row.Str("toAddress"), Body: row.Str("body"), Subject: row.Str("subject"), IdempotencyKey: id}
	if in.addressKindOf(channel) == "email" {
		last, err := orchestration.FindOne(ctx, in.host, in.messages.Where().Eq("conversation", row.Str("conversation")).Eq("direction", "inbound").NotNull("messageId"), in.messages.C("at")+" DESC")
		if err != nil {
			return SendOutcome{}, err
		}
		message.MessageID = "<" + uuid.NewString() + "@" + in.domainOf() + ">"
		if last != nil {
			message.InReplyTo = last.Str("messageId")
			message.References = append(append([]string{}, last.Strings("threadRefs")...), last.Str("messageId"))
		}
		message.ReplyTo = in.inboxAddress
	}
	sent, err := found.adapter.Send(ctx, message)
	if err != nil {
		detail := err.Error()
		if _, err := orchestration.UpdateID(ctx, in.host, in.messages, id, map[string]any{"state": Failed, "slot": found.slot, "detail": detail}); err != nil {
			return SendOutcome{}, err
		}
		return SendOutcome{Outcome: Failed, Message: id, Detail: detail}, nil
	}
	values := map[string]any{"state": Sent, "slot": found.slot, "providerMessageId": sent.ProviderMessageID, "at": in.now()}
	if message.MessageID != "" {
		values["messageId"] = message.MessageID
		values["inReplyTo"] = nullable(message.InReplyTo)
		if message.References != nil {
			values["threadRefs"] = message.References
		} else {
			values["threadRefs"] = nil
		}
	}
	if _, err := orchestration.UpdateID(ctx, in.host, in.messages, id, values); err != nil {
		return SendOutcome{}, err
	}
	if _, err := orchestration.Update(ctx, in.host, in.conversations.Where().Eq("id", row.Str("conversation")), map[string]any{"lastMessageAt": in.now()}); err != nil {
		return SendOutcome{}, err
	}
	return SendOutcome{Outcome: Sent, Message: id}, nil
}

// visibleParty reports whether as reads party's row.
func (in *Inbound) visibleParty(ctx context.Context, as orchestration.Principal, party orchestration.PartyRef) (bool, error) {
	if err := in.partyTable(party.Table); err != nil {
		return false, err
	}
	_, found, err := in.host.Record(in.host.As(ctx, as.Principal), party.Table, party.Key)
	return found, err
}

// Send sends a message to a party, held to the permission, its identity,
// the rate limit and the approval policy.
func (in *Inbound) Send(ctx context.Context, input SendInput) (SendOutcome, error) {
	if err := requirePermission(input.As, in.opts.SendPermission, "message a party"); err != nil {
		return SendOutcome{}, err
	}
	// The sender reads the party's row: a party it cannot see it cannot
	// message, and nothing says the party exists.
	visible, err := in.visibleParty(ctx, input.As, input.Party)
	if err != nil {
		return SendOutcome{}, err
	}
	if !visible {
		return SendOutcome{}, refused("no party %s %s", input.Party.Table, input.Party.Key)
	}
	destination, refusal, err := in.destinationOf(ctx, input.Party, input.Channel)
	if err != nil {
		return SendOutcome{}, err
	}
	address := ""
	if destination != nil {
		address = destination.Str("address")
	}
	var conversation orchestration.Row
	if err := in.host.Transaction(ctx, func(tx notifications.Host) error {
		c, err := in.conversationOf(ctx, tx, input.Channel, address, &input.Party, input.Subject)
		conversation = c
		return err
	}); err != nil {
		return SendOutcome{}, err
	}
	// The party's address on the channel is the one it is reached at now.
	if destination != nil && conversation.Str("address") != address {
		if conversation, err = orchestration.UpdateID(ctx, in.host, in.conversations, conversation.Str("id"), map[string]any{"address": address}); err != nil {
			return SendOutcome{}, err
		}
	}
	from := input.Channel
	if in.addressKindOf(input.Channel) == "email" && in.inboxAddress != "" {
		from = in.inboxAddress
	}
	subject := nullable(input.Subject)
	if input.Subject == "" && conversation.Str("subject") != "" {
		subject = "Re: " + conversation.Str("subject")
	}
	authorKind := string(input.As.KindOrUser())
	var forSubject any
	if input.As.ActingFor != nil && input.As.ActingFor.Subject != input.As.Subject {
		forSubject = input.As.ActingFor.Subject
	}
	base := map[string]any{
		"conversation": conversation.Str("id"),
		"direction":    "outbound",
		"via":          input.Channel,
		"fromAddress":  from,
		"toAddress":    nullable(address),
		"subject":      subject,
		"body":         input.Body,
		"identity":     nil,
		"partyTable":   input.Party.Table,
		"party":        input.Party.Key,
		"author":       input.As.Subject,
		"authorKind":   authorKind,
		"forSubject":   forSubject,
		"at":           in.now(),
	}
	if destination != nil {
		base["identity"] = destination.Str("id")
	}
	with := func(extra map[string]any) map[string]any {
		out := make(map[string]any, len(base)+len(extra))
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	refuse := func(detail string) (SendOutcome, error) {
		row, err := orchestration.Insert(ctx, in.host, in.messages, with(map[string]any{"state": Refused, "detail": detail}))
		if err != nil {
			return SendOutcome{}, err
		}
		return SendOutcome{Outcome: Refused, Message: row.Str("id"), Detail: detail}, nil
	}
	if refusal != "" {
		return refuse(refusal)
	}
	limit := in.opts.RateLimit
	since := in.now().Add(-time.Duration(limit.Seconds) * time.Second)
	recent, err := orchestration.Find(ctx, in.host, in.messages.Where().Eq("partyTable", input.Party.Table).Eq("party", input.Party.Key).Eq("direction", "outbound").In("state", Sent, "queued", AwaitingApproval).Cmp("at", ">", since), "", limit.Messages)
	if err != nil {
		return SendOutcome{}, err
	}
	if len(recent) >= limit.Messages {
		return refuse(fmt.Sprintf("the party was sent %d messages within %d s, the most it may be", len(recent), limit.Seconds))
	}
	decision := Allow
	if approve := in.approval(); approve != nil {
		if decision, err = approve.ApproveSend(ctx, input); err != nil {
			return SendOutcome{}, err
		}
	}
	if decision == Deny {
		return refuse("the approval policy refuses it")
	}
	state := "queued"
	if decision == Ask {
		state = AwaitingApproval
	}
	row, err := orchestration.Insert(ctx, in.host, in.messages, with(map[string]any{"state": state}))
	if err != nil {
		return SendOutcome{}, err
	}
	for _, about := range input.About {
		in.refer(ctx, row.Str("id"), about, "about")
	}
	if decision == Ask {
		return SendOutcome{Outcome: AwaitingApproval, Message: row.Str("id")}, nil
	}
	return in.deliver(ctx, row)
}

// Release sends a message held awaiting approval, once it was approved;
// its party's identity is checked again.
func (in *Inbound) Release(ctx context.Context, id string) (SendOutcome, error) {
	row, err := orchestration.ByID(ctx, in.host, in.messages, id)
	if err != nil {
		return SendOutcome{}, err
	}
	if row == nil || row.Str("state") != AwaitingApproval {
		return SendOutcome{}, refused("message %s awaits no approval", id)
	}
	party := orchestration.PartyRef{Table: row.Str("partyTable"), Key: notify.SameID(row.Str("party"))}
	destination, refusal, err := in.destinationOf(ctx, party, row.Str("via"))
	if err != nil {
		return SendOutcome{}, err
	}
	if refusal != "" {
		if _, err := orchestration.UpdateID(ctx, in.host, in.messages, row.Str("id"), map[string]any{"state": Refused, "detail": refusal}); err != nil {
			return SendOutcome{}, err
		}
		return SendOutcome{Outcome: Refused, Message: id, Detail: refusal}, nil
	}
	queued, err := orchestration.UpdateID(ctx, in.host, in.messages, row.Str("id"), map[string]any{"state": "queued", "toAddress": destination.Str("address"), "identity": destination.Str("id")})
	if err != nil {
		return SendOutcome{}, err
	}
	return in.deliver(ctx, queued)
}

// Reject refuses a message held awaiting approval; detail is "not
// approved" unless set.
func (in *Inbound) Reject(ctx context.Context, id, detail string) error {
	row, err := orchestration.ByID(ctx, in.host, in.messages, id)
	if err != nil {
		return err
	}
	if row == nil || row.Str("state") != AwaitingApproval {
		return refused("message %s awaits no approval", id)
	}
	if detail == "" {
		detail = "not approved"
	}
	_, err = orchestration.UpdateID(ctx, in.host, in.messages, row.Str("id"), map[string]any{"state": Refused, "detail": detail})
	return err
}

// ---- the rest ----

// PrincipalOf is the principal a party is: its active grants' reach and
// their roles' permissions, or one grant's when grant is set (D156).
func (in *Inbound) PrincipalOf(ctx context.Context, party orchestration.PartyRef, grant string) (orchestration.Principal, error) {
	return orchestration.PartyPrincipal(ctx, in.host, party, grant, in.now())
}

// FileInput is what File takes: a message, the row it goes on, who files
// it, which must read the row, and the reference's role (about unless
// set).
type FileInput struct {
	Message string
	To      rowrefs.RowRef
	As      orchestration.Principal
	Role    string
}

// File files a message onto a row as As, which must read it: the row
// points back at the message, and holds the message's files.
func (in *Inbound) File(ctx context.Context, input FileInput) error {
	row, err := orchestration.ByID(ctx, in.host, in.messages, input.Message)
	if err != nil {
		return err
	}
	if row == nil {
		return refused("no message %s", input.Message)
	}
	// Whoever files it must read the row it goes on: a party's run, held to
	// its grants, reaches the rows its role's paths reach.
	if _, ok := in.host.Definitions().Tables[input.To.Table]; !ok {
		return refused("%s is no table of the database", input.To.Table)
	}
	_, found, err := in.host.Record(in.host.As(ctx, input.As.Principal), input.To.Table, input.To.Key)
	if err != nil {
		return err
	}
	if !found {
		return refused("%s %s is not there", input.To.Table, input.To.Key)
	}
	role := input.Role
	if role == "" {
		role = "about"
	}
	in.refer(ctx, input.Message, input.To, role)
	// Only a messages table with @attachments has files to file, and only a
	// row of a table with them takes one: any other row gets the reference
	// alone.
	attachments := in.host.Attachments()
	if attachments == nil || !attachments.Holds(in.messageType) || !attachments.Holds(input.To.Table) {
		return nil
	}
	sys := in.system(ctx)
	// Filing is idempotent, as the reference is: a run request whose
	// handler failed after filing is run again, and the row takes no file
	// twice.
	held, err := attachments.List(sys, input.To)
	if err != nil {
		return err
	}
	filed := map[string]bool{}
	for _, file := range held {
		if file.FiledFrom != nil {
			filed[*file.FiledFrom] = true
		}
	}
	files, err := attachments.List(sys, rowrefs.RowRef{Table: in.messageType, Key: input.Message})
	if err != nil {
		return err
	}
	for _, file := range files {
		if filed[in.messageType+":"+file.Key+":"+file.ID] {
			continue
		}
		if _, err := attachments.File(sys, rowrefs.FileInput{FileRef: rowrefs.FileRef{Table: in.messageType, ID: file.ID}, To: input.To, Role: role}); err != nil {
			return err
		}
	}
	return nil
}

// Triage is the messages held in triage, newest first (50 unless limit);
// as holds the triage permission.
func (in *Inbound) Triage(ctx context.Context, as orchestration.Principal, limit int) ([]orchestration.Row, error) {
	if err := requirePermission(as, in.opts.TriagePermission, "read triage"); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	return orchestration.Find(ctx, in.host, in.messages.Where().Eq("triage", "open"), in.messages.C("at")+" DESC", limit)
}

// Settle closes a message's triage; as holds the triage permission.
func (in *Inbound) Settle(ctx context.Context, as orchestration.Principal, id string) error {
	if err := requirePermission(as, in.opts.TriagePermission, "settle triage"); err != nil {
		return err
	}
	row, err := orchestration.ByID(ctx, in.host, in.messages, id)
	if err != nil {
		return err
	}
	if row == nil || row.Str("triage") != "open" {
		return refused("message %s is not held in triage", id)
	}
	_, err = orchestration.UpdateID(ctx, in.host, in.messages, row.Str("id"), map[string]any{"triage": "closed", "triageClosedBy": as.Subject, "triageClosedAt": in.now()})
	return err
}

// Timeline is a party's messages on every channel, newest first (50
// unless limit); as reads the party's row.
func (in *Inbound) Timeline(ctx context.Context, as orchestration.Principal, party orchestration.PartyRef, limit int) ([]orchestration.Row, error) {
	visible, err := in.visibleParty(ctx, as, party)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, refused("no party %s %s", party.Table, party.Key)
	}
	if limit <= 0 {
		limit = 50
	}
	if !in.messages.Holds("party", party.Key) {
		return nil, nil
	}
	return orchestration.Find(ctx, in.host, in.messages.Where().Eq("partyTable", party.Table).Eq("party", party.Key), in.messages.C("at")+" DESC", limit)
}

// Message is a message's row, nil for none.
func (in *Inbound) Message(ctx context.Context, id string) (orchestration.Row, error) {
	return orchestration.ByID(ctx, in.host, in.messages, id)
}

// MessageType is the messages table's type.
func (in *Inbound) MessageType() string { return in.messageType }

// RunQueue is the @runRequests queue as a worker claims it, each message
// handed to handle: the agent dispatcher's (D158), or an app's own.
func (in *Inbound) RunQueue(handle func(ctx context.Context, request RunRequest) error) (worker.Queue, error) {
	queue := in.host.QueueOf(orchestration.RunRequests)
	if queue == nil {
		return worker.Queue{}, errors.New("inbound: the database declares no @runRequests queue")
	}
	return worker.Queue{
		Name:  queue.Name(),
		Lease: queue.Lease(),
		Claim: func(ctx context.Context, count int, lease time.Duration) ([]worker.Message, error) {
			claims, err := queue.Claim(ctx, count, lease)
			out := make([]worker.Message, 0, len(claims))
			for _, claim := range claims {
				message := claim.Message
				out = append(out, worker.Message{ID: claim.ID, Token: claim.Token, Attempt: claim.Attempt, Handle: func(ctx context.Context) error {
					var request RunRequest
					decoder := json.NewDecoder(bytes.NewReader(message))
					if err := decoder.Decode(&request); err != nil {
						return fmt.Errorf("inbound: a run request %s: %w", message, err)
					}
					return handle(ctx, request)
				}})
			}
			return out, err
		},
		Extend:   queue.Extend,
		Complete: queue.Complete,
		Fail:     queue.Fail,
		Release:  queue.Release,
		Prune:    queue.Prune,
		Slot:     queue.Slot,
		NextDue:  queue.NextDue,
	}, nil
}
