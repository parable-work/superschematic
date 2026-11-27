package notify

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/providers"
	"github.com/parable-work/superschematic/runtime/http/go/worker"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/notifications"
)

// Error is the refusal of a notifier call: an unknown event kind, a
// notice that is not the caller's, a table the database does not declare.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func refused(format string, args ...any) error {
	return &Error{Message: fmt.Sprintf(format, args...)}
}

// RaiseInput is an event as Raise takes it.
type RaiseInput struct {
	// Table is the table whose row raised it, by its type's name; empty for
	// an event of no record (channel.down).
	Table string
	// Key is the row's key.
	Key  string
	Kind string
	// Payload is what it says beyond the payload fields its @event names,
	// which Raise reads from the row.
	Payload map[string]any
	// Urgency is an urgency in place of the event's default.
	Urgency string
	// ID is the event's id, so a retried handler raises it once; a new one
	// unless set.
	ID string
}

// raisedEvent is an event as it travels on the dispatch queue.
type raisedEvent struct {
	ID      string         `json:"id"`
	Table   string         `json:"table,omitempty"`
	Key     string         `json:"key,omitempty"`
	Kind    string         `json:"kind"`
	Urgency string         `json:"urgency,omitempty"`
	Payload map[string]any `json:"payload"`
	At      int64          `json:"at"`
}

// BehaviorEvent is a behavior's event (D104) as the Go behaviors adapter
// hands a project's Workers method: the behavior's kind (missed), the
// row's key and the event's payload.
type BehaviorEvent struct {
	Kind    string
	Key     string
	Payload any
}

// ClearedReference is a message of a @references table's cleared queue
// (D154): the row that held a reference a target's delete cleared.
type ClearedReference struct {
	HolderTable string
	HolderKey   string
	TargetTable string
	TargetKey   string
	Role        string
}

// RecipientRef is a principal a notice is for: a user, a party (by its
// table) or an agent run.
type RecipientRef struct {
	Kind  string
	ID    string
	Table string
}

// User is the user id as a recipient.
func User(id string) RecipientRef { return RecipientRef{Kind: "user", ID: id} }

func (r RecipientRef) principalKind() access.Kind {
	switch r.Kind {
	case "party":
		return access.KindParty
	case "agent":
		return access.KindAgent
	}
	return access.KindUser
}

// Notice is a notice as the inbox holds it.
type Notice struct {
	ID              string
	Recipient       string
	RecipientKind   string
	Reference       int64
	EventID         string
	EventKind       string
	Urgency         string
	SubjectTable    string
	SubjectKey      string
	Title           string
	Body            string
	Payload         map[string]any
	Route           string
	State           string
	Escalation      int64
	CreatedAt       *time.Time
	ReadAt          *time.Time
	AcknowledgedAt  *time.Time
	AcknowledgedVia string
	DigestedAt      *time.Time
}

func noticeOf(r row) Notice {
	payload, _ := r["payload"].(map[string]any)
	return Notice{
		ID: r.str("id"), Recipient: r.str("recipient"), RecipientKind: r.str("recipientKind"), Reference: r.int("reference"),
		EventID: r.str("eventId"), EventKind: r.str("eventKind"), Urgency: r.str("urgency"), SubjectTable: r.str("subjectTable"),
		SubjectKey: r.str("subjectKey"), Title: r.str("title"), Body: r.str("body"), Payload: payload, Route: r.str("route"),
		State: r.str("state"), Escalation: r.int("escalation"), CreatedAt: r.time("createdAt"), ReadAt: r.time("readAt"),
		AcknowledgedAt: r.time("acknowledgedAt"), AcknowledgedVia: r.str("acknowledgedVia"), DigestedAt: r.time("digestedAt"),
	}
}

func (n Notice) recipient() RecipientRef {
	kind := n.RecipientKind
	if kind == "" {
		kind = "user"
	}
	return RecipientRef{Kind: kind, ID: n.Recipient}
}

func (n Notice) closed() bool { return n.State == "acknowledged" || n.State == "done" }

// Rendered is what a notice says on a channel.
type Rendered struct {
	Subject string
	Body    string
}

// ExpandInput is what a recurrence rule is expanded by (RulesCore.expand,
// D152): a digest's or a brief's rule from its start in its zone, the
// first occurrences after From.
type ExpandInput struct {
	Anchor   string
	Rule     string
	Start    string
	TimeZone string
	From     time.Time
	Limit    int
}

// Options are what New takes.
type Options struct {
	// Providers are the providers an environment configures (D159): every
	// channel slot is a channel the notifier sends on.
	Providers *providers.Providers
	// Replies are the reply actions, by event kind (or * for every kind),
	// each by its verb.
	Replies map[string]map[string]ReplyAction
	// DefaultChannels are the channels a recipient with no preference row
	// of any kind is sent on; inapp alone unless set.
	DefaultChannels []string
	// AddressKinds are the kind of address a channel kind sends to,
	// beside DefaultAddressKind's.
	AddressKinds map[string]string
	// Render is what a notice says on a channel; its title, reference and
	// how to answer unless set.
	Render func(notice Notice, channel string) Rendered
	// PermissionsOf are the permissions a recipient holds; a user's roles'
	// unless set.
	PermissionsOf func(ctx context.Context, recipient RecipientRef) ([]string, error)
	// Agents is the agent dispatcher a route's { agent } recipient puts a
	// run request on the queue of (D158, D192); such a route is logged and
	// reaches no one without it.
	Agents Agents
	// Expand is the core's expansion of a recurrence rule (D152), which
	// digests and briefs are dated by: the instants it falls due at.
	Expand func(ExpandInput) ([]time.Time, error)
	// DeliveryTimeout and DeliveryRetentionDays are the defaults' unless
	// set.
	DeliveryTimeout       time.Duration
	DeliveryRetentionDays int
	// Now is the clock; time.Now unless set, for a test.
	Now func() time.Time
	// Logger logs what the notifier skips and what failed; a no-op unless
	// set.
	Logger *zap.Logger
}

// Notifier is the notifier over a database's notifications.Host (D153,
// D190). It is safe for concurrent use.
type Notifier struct {
	host    notifications.Host
	defs    *notifications.Definitions
	opts    Options
	log     *zap.Logger
	timeout time.Duration
	retain  time.Duration

	inbox, deliveries, routes                table
	preferences, subscriptions, trust, reply *table
	identities                               *table

	channelsOnce sync.Once
	channelList  []channel
	channelErr   error

	healthMu sync.Mutex
	health   map[string]healthEntry
}

type channel struct {
	slot, kind string
	adapter    providers.ChannelAdapter
}

type healthEntry struct {
	ok     bool
	detail string
	at     time.Time
}

// New is the notifier over host, the generated ORM's Notifications(): it
// refuses a database with no inbox, deliveries or routes table.
func New(host notifications.Host, opts Options) (*Notifier, error) {
	if host == nil {
		return nil, errors.New("notify: New needs the database's Notifications()")
	}
	defs := host.Definitions()
	n := &Notifier{host: host, defs: defs, opts: opts, log: opts.Logger, health: map[string]healthEntry{}}
	if n.log == nil {
		n.log = zap.NewNop()
	}
	n.timeout = opts.DeliveryTimeout
	if n.timeout <= 0 {
		n.timeout = DefaultDeliveryTimeoutSeconds * time.Second
	}
	days := opts.DeliveryRetentionDays
	if days <= 0 {
		days = DefaultDeliveryRetentionDays
	}
	n.retain = time.Duration(days) * 24 * time.Hour
	part := func(name string) *table {
		p, ok := defs.Parts[name]
		if !ok {
			return nil
		}
		return &table{Part: p, part: name}
	}
	for _, required := range []string{"inbox", "deliveries", "routes"} {
		if part(required) == nil {
			return nil, fmt.Errorf("notify: the database has no @notifier(%q) table", required)
		}
	}
	n.inbox, n.deliveries, n.routes = *part("inbox"), *part("deliveries"), *part("routes")
	n.preferences, n.subscriptions, n.trust, n.reply = part("preferences"), part("subscriptions"), part("channelTrust"), part("replies")
	n.identities = part("channelIdentities")
	return n, nil
}

func (n *Notifier) now() time.Time {
	if n.opts.Now != nil {
		return n.opts.Now()
	}
	return time.Now()
}

func (n *Notifier) system(ctx context.Context) context.Context { return n.host.System(ctx) }

func (n *Notifier) addressKindOf(channel string) string {
	if kind, ok := n.opts.AddressKinds[channel]; ok {
		return kind
	}
	return DefaultAddressKind(channel)
}

// channelKindOfAddress is the channel kind that sends to an address kind.
func (n *Notifier) channelKindOfAddress(kind string) string {
	for channel, address := range n.opts.AddressKinds {
		if address == kind {
			return channel
		}
	}
	switch kind {
	case "email":
		return "email"
	case "phone":
		return "sms"
	}
	return kind
}

// in is the host over tx, the caller's transaction; the notifier's own
// host, whose writes open a transaction of their own, for nil.
func (n *Notifier) in(tx any) (notifications.Host, error) {
	if tx == nil {
		return n.host, nil
	}
	if h, ok := tx.(notifications.Host); ok {
		return h, nil
	}
	return n.host.In(tx)
}

// ---- channels ----

func (n *Notifier) channels(ctx context.Context) ([]channel, error) {
	n.channelsOnce.Do(func() {
		if n.opts.Providers == nil {
			return
		}
		for _, configured := range n.opts.Providers.Configured(providers.Channel) {
			adapter, err := n.opts.Providers.Channel(context.WithoutCancel(ctx), configured.Slot)
			if err != nil {
				n.channelErr = err
				return
			}
			n.channelList = append(n.channelList, channel{slot: configured.Slot, kind: adapter.Kind(), adapter: adapter})
		}
	})
	return n.channelList, n.channelErr
}

func (n *Notifier) healthOf(ctx context.Context, c channel) (bool, string, error) {
	n.healthMu.Lock()
	cached, held := n.health[c.slot]
	n.healthMu.Unlock()
	if held && n.now().Sub(cached.at) < HealthTTLMillis*time.Millisecond {
		return cached.ok, cached.detail, nil
	}
	answer, err := c.adapter.Health(ctx)
	ok, detail := answer.OK, answer.Detail
	if err != nil {
		ok, detail = false, err.Error()
	}
	n.healthMu.Lock()
	n.health[c.slot] = healthEntry{ok: ok, detail: detail, at: n.now()}
	n.healthMu.Unlock()
	if !ok && (!held || cached.ok) {
		if err := n.alertDown(ctx, c, detail); err != nil {
			return ok, detail, err
		}
	}
	return ok, detail, nil
}

// channelOfKind is a healthy channel of kind, preferring slot; false for
// none.
func (n *Notifier) channelOfKind(ctx context.Context, kind, slot string) (channel, bool, error) {
	all, err := n.channels(ctx)
	if err != nil {
		return channel{}, false, err
	}
	var list []channel
	for _, c := range all {
		if c.kind == kind && c.slot == slot && slot != "" {
			list = append(list, c)
		}
	}
	for _, c := range all {
		if c.kind == kind && (c.slot != slot || slot == "") {
			list = append(list, c)
		}
	}
	for _, c := range list {
		ok, _, err := n.healthOf(ctx, c)
		if err != nil {
			return channel{}, false, err
		}
		if ok {
			return c, true, nil
		}
	}
	return channel{}, false, nil
}

func (n *Notifier) alertDown(ctx context.Context, c channel, detail string) error {
	fields := []zap.Field{zap.String("slot", c.slot), zap.String("kind", c.kind)}
	if detail != "" {
		fields = append(fields, zap.String("detail", detail))
	}
	n.log.Warn("channel down", fields...)
	// One alert while it is down: an alert of the slot within the hour
	// stands.
	recent, err := n.findOne(ctx, n.host, n.inbox.where().eq("eventKind", "channel.down").eq("subjectKey", c.slot).cmp("createdAt", ">", n.now().Add(-time.Hour)), "")
	if err != nil || recent != nil {
		return err
	}
	payload := map[string]any{"slot": c.slot, "channel": c.kind}
	if detail != "" {
		payload["detail"] = detail
	}
	return n.host.Transaction(ctx, func(h notifications.Host) error {
		_, err := n.raiseIn(ctx, h, RaiseInput{Kind: "channel.down", Key: c.slot, Urgency: Urgent, Payload: payload}, true)
		return err
	})
}

// ---- raising ----

// Raise raises an event in tx, the ORM's transaction of the change that
// caused it (its TxInterface), and returns its id. A table's own event is
// one its @event declares.
func (n *Notifier) Raise(ctx context.Context, tx any, input RaiseInput) (string, error) {
	h, err := n.in(tx)
	if err != nil {
		return "", err
	}
	return n.raiseIn(ctx, h, input, false)
}

// FromBehaviorEvent raises a behavior's event (D104) in tx, as its
// behavior worker's Workers method hands it: the kind is the use's
// member, a dot and the behavior's kind (recurrence.missed).
func (n *Notifier) FromBehaviorEvent(ctx context.Context, tx any, typeName, member string, event BehaviorEvent) (string, error) {
	h, err := n.in(tx)
	if err != nil {
		return "", err
	}
	payload := map[string]any{}
	if event.Payload != nil {
		data, err := json.Marshal(event.Payload)
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			payload = map[string]any{"value": json.RawMessage(data)}
		}
	}
	return n.raiseIn(ctx, h, RaiseInput{Table: typeName, Key: event.Key, Kind: member + "." + event.Kind, Payload: payload}, true)
}

// Cleared raises reference.cleared on the row that held a reference a
// target's delete cleared (D154), from the cleared queue's message, in tx.
func (n *Notifier) Cleared(ctx context.Context, tx any, message ClearedReference) (string, error) {
	h, err := n.in(tx)
	if err != nil {
		return "", err
	}
	return n.raiseIn(ctx, h, RaiseInput{Table: message.HolderTable, Key: message.HolderKey, Kind: "reference.cleared", Payload: map[string]any{"targetTable": message.TargetTable, "targetKey": message.TargetKey, "role": message.Role}}, true)
}

// raiseIn raises input in h. A behavior's event and a cleared reference's
// need no declaration, which only sets their urgency and title.
func (n *Notifier) raiseIn(ctx context.Context, h notifications.Host, input RaiseInput, undeclared bool) (string, error) {
	declared, hasDeclared := notifications.Event{}, false
	if input.Table != "" {
		if _, ok := n.defs.Tables[input.Table]; !ok {
			return "", refused("raise: %s is no table of the database", input.Table)
		}
		declared, hasDeclared = n.defs.EventOf(input.Table, input.Kind)
		if !hasDeclared && !undeclared {
			return "", refused("raise: %s declares no event %s; declare it with @event", input.Table, input.Kind)
		}
	}
	payload := map[string]any{}
	if hasDeclared && len(declared.Payload) > 0 && input.Key != "" {
		record, _, err := h.Record(n.system(ctx), input.Table, input.Key)
		if err != nil {
			return "", err
		}
		for _, field := range declared.Payload {
			payload[field] = record[field]
		}
	}
	for k, v := range input.Payload {
		payload[k] = v
	}
	id := input.ID
	if id == "" {
		id = uuid.NewString()
	}
	event := raisedEvent{ID: id, Table: input.Table, Key: input.Key, Kind: input.Kind, Urgency: input.Urgency, Payload: payload, At: n.now().UnixMilli()}
	data, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	if err := h.Enqueue(ctx, notifications.Work{Step: "route", Event: data}, 0); err != nil {
		return "", err
	}
	return SameID(id), nil
}

// ---- recipients ----

func (n *Notifier) permissionsOf(ctx context.Context, recipient RecipientRef) ([]string, error) {
	if n.opts.PermissionsOf != nil {
		return n.opts.PermissionsOf(ctx, recipient)
	}
	if recipient.Kind != "user" && recipient.Kind != "" {
		return []string{}, nil
	}
	held, err := n.host.Permissions(ctx, recipient.ID)
	if held == nil {
		held = []string{}
	}
	return held, err
}

// readerOf is the principal a recipient reads as (D156): a user by its id,
// a party or an agent run with its active grants.
func (n *Notifier) readerOf(ctx context.Context, h notifications.Host, recipient RecipientRef) (access.Principal, error) {
	kind := recipient.principalKind()
	if kind == access.KindUser {
		return access.Principal{Subject: recipient.ID, Kind: access.KindUser}, nil
	}
	grants, err := h.ActiveGrants(ctx, recipient.ID, n.now())
	if err != nil {
		return access.Principal{}, err
	}
	if grants == nil {
		grants = []access.GrantReach{}
	}
	return access.Principal{Subject: recipient.ID, Kind: kind, Grants: grants}, nil
}

// reads reports whether recipient reads the record table and key name,
// through its table's repository as the recipient.
func (n *Notifier) reads(ctx context.Context, h notifications.Host, recipient RecipientRef, typeName, key string) bool {
	if typeName == "" || key == "" {
		return true
	}
	if _, ok := n.defs.Tables[typeName]; !ok {
		return false
	}
	reader, err := n.readerOf(ctx, h, recipient)
	if err != nil {
		return false
	}
	_, found, err := h.Record(h.As(ctx, reader), typeName, key)
	return err == nil && found
}

// followPath is the principals a path of the record names: each step a
// to-one relation, the last a field of ids.
func (n *Notifier) followPath(ctx context.Context, h notifications.Host, event raisedEvent, path string) ([]string, error) {
	if event.Table == "" || event.Key == "" {
		return nil, nil
	}
	sys := n.system(ctx)
	typeName := event.Table
	record, found, err := h.Record(sys, typeName, event.Key)
	if err != nil || !found {
		return nil, err
	}
	steps := strings.Split(path, ".")
	for i, step := range steps {
		if i == len(steps)-1 {
			switch value := record[step].(type) {
			case nil:
				return nil, nil
			case []any:
				out := make([]string, 0, len(value))
				for _, v := range value {
					out = append(out, textOf(v))
				}
				return out, nil
			default:
				return []string{textOf(value)}, nil
			}
		}
		relation, ok := n.defs.Tables[typeName].Relations[step]
		if !ok {
			return nil, nil
		}
		key := textOf(record[relation.Member])
		if object, ok := record[relation.Member].(map[string]any); ok {
			key = textOf(object[n.defs.Tables[relation.Target].Key])
		}
		if key == "" {
			return nil, nil
		}
		typeName = relation.Target
		if record, found, err = h.Record(sys, typeName, key); err != nil || !found {
			return nil, err
		}
	}
	return nil, nil
}

// recipientSpec is one of a route's recipients (D153).
type recipientSpec struct {
	Path      *string `json:"path"`
	Principal *string `json:"principal"`
	Kind      string  `json:"kind"`
	Role      *string `json:"role"`
	Party     *struct {
		Table string `json:"table"`
		Key   string `json:"key"`
	} `json:"party"`
	Agent *string `json:"agent"`
	Task  string  `json:"task"`
}

func specsOf(v any) []recipientSpec {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []recipientSpec
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

func (n *Notifier) recipientsOf(ctx context.Context, h notifications.Host, event raisedEvent, specs []recipientSpec) ([]RecipientRef, error) {
	var out []RecipientRef
	for _, spec := range specs {
		switch {
		case spec.Path != nil:
			ids, err := n.followPath(ctx, h, event, *spec.Path)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				out = append(out, User(id))
			}
		case spec.Principal != nil:
			kind := spec.Kind
			if kind == "" {
				kind = "user"
			}
			out = append(out, RecipientRef{Kind: kind, ID: *spec.Principal})
		case spec.Role != nil:
			holders, err := h.RoleHolders(n.system(ctx), *spec.Role)
			if err != nil {
				return nil, err
			}
			for _, id := range holders {
				out = append(out, User(id))
			}
		case spec.Party != nil:
			out = append(out, RecipientRef{Kind: "party", ID: spec.Party.Key, Table: spec.Party.Table})
		}
	}
	seen := map[string]bool{}
	unique := out[:0]
	for _, r := range out {
		key := r.Kind + ":" + SameID(r.ID)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, r)
	}
	return unique, nil
}

// ---- preferences ----

type preference struct {
	channels             []string
	quietStart, quietEnd string
	timeZone             string
	digest               string
}

func (n *Notifier) preferenceOf(ctx context.Context, h notifications.Host, recipient RecipientRef, kind, urgency string) (preference, error) {
	fallback := preference{channels: n.opts.DefaultChannels, timeZone: "UTC"}
	if fallback.channels == nil {
		fallback.channels = []string{InApp}
	}
	if n.preferences == nil {
		return fallback, nil
	}
	var rows []row
	if n.preferences.holds("principal", recipient.ID) {
		own, err := n.find(ctx, h, n.preferences.where().eq("principal", recipient.ID), "", 100)
		if err != nil {
			return fallback, err
		}
		rows = append(rows, own...)
	}
	everyone, err := n.find(ctx, h, n.preferences.where().null("principal"), "", 100)
	if err != nil {
		return fallback, err
	}
	rows = append(rows, everyone...)
	score := func(r row) int {
		s := 0
		if !r.isNull("principal") {
			s += 4
		}
		switch r.str("eventKind") {
		case kind:
			s += 2
		case "*":
		default:
			s -= 100
		}
		switch r.str("urgency") {
		case urgency:
			s++
		case "*":
		default:
			s -= 100
		}
		return s
	}
	// The most specific row wins; of rows alike, the first read.
	var best row
	bestScore := -1
	for _, r := range rows {
		if s := score(r); s >= 0 && s > bestScore {
			best, bestScore = r, s
		}
	}
	if best == nil {
		return fallback, nil
	}
	out := preference{channels: best.strings("channels"), quietStart: best.str("quietStart"), quietEnd: best.str("quietEnd"), timeZone: best.str("timeZone"), digest: best.str("digest")}
	if out.channels == nil {
		out.channels = fallback.channels
	}
	if out.timeZone == "" {
		out.timeZone = "UTC"
	}
	return out, nil
}

// wallOf is the wall time of at in zone, from Postgres, which holds the
// zone database: its minutes of the day, its second, and its date.
func (n *Notifier) wallOf(ctx context.Context, h notifications.Host, at time.Time, zone string) (int, int, string, error) {
	rows, err := h.Query(ctx, `SELECT to_char(($1::timestamptz) AT TIME ZONE $2::text, 'HH24:MI:SS YYYY-MM-DD') AS wall`, at.UTC(), zone)
	if err != nil {
		return 0, 0, "", fmt.Errorf("notify: the wall time in %s: %w", zone, err)
	}
	if len(rows) == 0 {
		return 0, 0, "", fmt.Errorf("notify: the wall time in %s: no row", zone)
	}
	wall := textOf(rows[0]["wall"])
	clock, date, _ := strings.Cut(wall, " ")
	parts := strings.Split(clock, ":")
	var hms [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		hms[i], _ = strconv.Atoi(parts[i])
	}
	return hms[0]*60 + hms[1], hms[2], date, nil
}

// outward are the channels a notice goes out on: the in-app inbox left
// out, since every notice reaches it.
func outward(list []string) []string {
	out := []string{}
	for _, c := range list {
		if c != InApp {
			out = append(out, c)
		}
	}
	return out
}

// ---- notices and deliveries ----

func (n *Notifier) render(notice Notice, channel string) Rendered {
	if n.opts.Render != nil {
		return n.opts.Render(notice, channel)
	}
	ask := ""
	if notice.Urgency == Urgent {
		ask = fmt.Sprintf(" Reply ACK %d to acknowledge.", notice.Reference)
	}
	body := ""
	if notice.Body != "" {
		body = " " + notice.Body
	}
	if channel == "email" {
		return Rendered{Subject: notice.Title, Body: fmt.Sprintf("%s%s\n\nReference %d.%s", notice.Title, body, notice.Reference, ask)}
	}
	return Rendered{Subject: notice.Title, Body: fmt.Sprintf("#%d %s%s%s", notice.Reference, notice.Title, body, ask)}
}

// noticeTitle is the title of event's notices, read once for a fan-out's
// recipients, when its first notice is written.
type noticeTitle struct {
	read bool
	text string
}

// of is the title of event's notices: its record's declared title field,
// or its kind and key.
func (t *noticeTitle) of(ctx context.Context, n *Notifier, h notifications.Host, event raisedEvent) (string, error) {
	if t.read {
		return t.text, nil
	}
	title := event.Kind
	if event.Key != "" {
		title += " " + event.Key
	}
	if declared, ok := n.defs.EventOf(event.Table, event.Kind); ok && declared.Title != "" && event.Table != "" && event.Key != "" {
		record, found, err := h.Record(n.system(ctx), event.Table, event.Key)
		if err != nil {
			return "", err
		}
		if found && record[declared.Title] != nil {
			title = textOf(record[declared.Title])
		}
	}
	t.read, t.text = true, title
	return title, nil
}

// noticed is the recipients, by SameID, who already hold a notice of event,
// of recipients, in one read.
func (n *Notifier) noticed(ctx context.Context, h notifications.Host, event raisedEvent, recipients []RecipientRef) (map[string]bool, error) {
	out := map[string]bool{}
	var ids []string
	for _, r := range recipients {
		if n.inbox.holds("recipient", r.ID) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := n.find(ctx, h, n.inbox.where().eq("eventId", event.ID).in("recipient", ids...), "", len(ids))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[SameID(r.str("recipient"))] = true
	}
	return out, nil
}

// notifyAll writes and dispatches the notice of event to each recipient
// that reads its record and holds none yet. The existing notices are read
// once for them all, and the title once, not once each.
func (n *Notifier) notifyAll(ctx context.Context, h notifications.Host, recipients []RecipientRef, event raisedEvent, title *noticeTitle, urgency, route string) error {
	noticed, err := n.noticed(ctx, h, event, recipients)
	if err != nil {
		return err
	}
	for _, recipient := range recipients {
		if noticed[SameID(recipient.ID)] || !n.reads(ctx, h, recipient, event.Table, event.Key) {
			continue
		}
		text, err := title.of(ctx, n, h, event)
		if err != nil {
			return err
		}
		notice, err := n.createNotice(ctx, h, recipient, event, text, urgency, route)
		if err != nil {
			return err
		}
		noticed[SameID(recipient.ID)] = true
		if err := n.dispatchNotice(ctx, h, *notice, recipient, urgency); err != nil {
			return err
		}
	}
	return nil
}

// createNotice writes recipient's notice of event, titled title: the
// caller has found that it holds none (notifyAll). Its reference is the
// recipient's next, taken in a savepoint: a concurrent notice of theirs
// that took it first is met by the unique index, and the notice tries
// again, the caller's transaction intact.
func (n *Notifier) createNotice(ctx context.Context, h notifications.Host, recipient RecipientRef, event raisedEvent, title, urgency, route string) (*Notice, error) {
	state := "unread"
	if urgency == Record {
		state = "read"
	}
	kind := recipient.Kind
	if kind == "" {
		kind = "user"
	}
	var created row
	for attempt := 0; ; attempt++ {
		err := h.Transaction(ctx, func(inner notifications.Host) error {
			last, err := n.findOne(ctx, inner, n.inbox.where().eq("recipient", recipient.ID), n.inbox.c("reference")+" DESC")
			if err != nil {
				return err
			}
			var reference int64 = 1
			if last != nil {
				reference = last.int("reference") + 1
			}
			values := map[string]any{
				"recipient": recipient.ID, "recipientKind": kind, "reference": reference, "eventId": event.ID, "eventKind": event.Kind,
				"urgency": urgency, "title": title, "payload": event.Payload, "state": state,
				"subjectTable": nullable(event.Table), "subjectKey": nullable(event.Key), "route": nullable(route),
			}
			created, err = n.insert(ctx, inner, n.inbox, values)
			return err
		})
		if err == nil {
			notice := noticeOf(created)
			return &notice, nil
		}
		if attempt >= 4 {
			return nil, err
		}
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (n *Notifier) queueDelivery(ctx context.Context, h notifications.Host, notice Notice, recipient RecipientRef, channel string, attempt int64, delay int) error {
	delivery, err := n.insert(ctx, h, n.deliveries, map[string]any{
		"notice": notice.ID, "purpose": "notice", "recipient": recipient.ID, "channelKind": channel,
		"attempt": attempt, "state": "queued", "dueAt": n.now().Add(time.Duration(delay) * time.Second),
	})
	if err != nil {
		return err
	}
	return h.Enqueue(ctx, notifications.Work{Step: "deliver", Delivery: delivery.str("id")}, time.Duration(delay)*time.Second)
}

func (n *Notifier) nextDigestAt(ctx context.Context, h notifications.Host, p preference) (time.Time, bool, error) {
	if p.digest == "" || n.opts.Expand == nil {
		return time.Time{}, false, nil
	}
	_, _, date, err := n.wallOf(ctx, h, n.now(), p.timeZone)
	if err != nil {
		return time.Time{}, false, err
	}
	due, err := n.opts.Expand(ExpandInput{Anchor: "calendar", Rule: p.digest, Start: date + "T00:00", TimeZone: p.timeZone, From: n.now(), Limit: 1})
	if err != nil || len(due) == 0 {
		return time.Time{}, false, err
	}
	return due[0], true, nil
}

// secondsUntil is the whole seconds from now to at, none when at is past.
func (n *Notifier) secondsUntil(at time.Time) int {
	return max(0, int(math.Ceil(at.Sub(n.now()).Seconds())))
}

// dispatchNotice sends notice to recipient as its urgency says: now, after
// quiet hours, in the next digest, or not at all.
func (n *Notifier) dispatchNotice(ctx context.Context, h notifications.Host, notice Notice, recipient RecipientRef, urgency string) error {
	if urgency == Record {
		return nil
	}
	p, err := n.preferenceOf(ctx, h, recipient, notice.EventKind, urgency)
	if err != nil {
		return err
	}
	list := outward(p.channels)
	if urgency == Digest {
		at, ok, err := n.nextDigestAt(ctx, h, p)
		if err != nil {
			return err
		}
		if ok {
			waiting, err := n.findOne(ctx, h, n.inbox.where().eq("recipient", recipient.ID).eq("urgency", Digest).null("digestedAt").ne("id", notice.ID), "")
			if err != nil {
				return err
			}
			if waiting == nil {
				return h.Enqueue(ctx, notifications.Work{Step: "digest", Recipient: recipient.ID}, time.Duration(n.secondsUntil(at))*time.Second)
			}
			return nil
		}
	}
	if len(list) == 0 {
		return nil
	}
	delay := 0
	if urgency != Urgent {
		minutes, second, _, err := n.wallOf(ctx, h, n.now(), p.timeZone)
		if err != nil {
			return err
		}
		delay = QuietDelaySeconds(minutes, second, p.quietStart, p.quietEnd)
	}
	return n.queueDelivery(ctx, h, notice, recipient, list[0], 1, delay)
}

// escalationStep is one step of a route's escalation.
type escalationStep struct {
	AfterSeconds float64         `json:"afterSeconds"`
	Channels     []string        `json:"channels"`
	Recipients   []recipientSpec `json:"recipients"`
}

func stepsOf(v any) []escalationStep {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []escalationStep
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

func seconds(s float64) time.Duration {
	return time.Duration(max(0, s)) * time.Second
}

func (n *Notifier) route(ctx context.Context, event raisedEvent) error {
	return n.host.Transaction(ctx, func(h notifications.Host) error {
		candidates, err := n.find(ctx, h, n.routes.where().eq("enabled", true).in("eventKind", event.Kind, "*"), "", 1000)
		if err != nil {
			return err
		}
		declared, _ := n.defs.EventOf(event.Table, event.Kind)
		// The notices' title, read once for every route's recipients.
		var title noticeTitle
		for _, r := range candidates {
			if subject := r.str("subjectTable"); subject != "" && subject != event.Table {
				continue
			}
			if !r.isNull("filter") && event.Table != "" && event.Key != "" {
				match, err := n.matches(ctx, h, event, r["filter"])
				if err != nil {
					return err
				}
				if !match {
					continue
				}
			}
			urgency := r.str("urgency")
			if urgency == "" {
				urgency = event.Urgency
			}
			if urgency == "" {
				urgency = declared.Urgency
			}
			if urgency == "" {
				urgency = Normal
			}
			specs := specsOf(r["recipients"])
			// A route that names an agent starts its run about the record
			// (D158), a request on the dispatcher's queue in this
			// transaction.
			if err := n.requestAgents(ctx, h, event, r.str("id"), specs); err != nil {
				return err
			}
			recipients, err := n.recipientsOf(ctx, h, event, specs)
			if err != nil {
				return err
			}
			if err := n.notifyAll(ctx, h, recipients, event, &title, urgency, r.str("id")); err != nil {
				return err
			}
			steps := stepsOf(r["escalation"])
			if urgency == Urgent && len(steps) > 0 && len(recipients) > 0 {
				notices, err := n.find(ctx, h, n.inbox.where().eq("eventId", event.ID).eq("route", r.str("id")), "", 1000)
				if err != nil {
					return err
				}
				for _, notice := range notices {
					if err := h.Enqueue(ctx, notifications.Work{Step: "escalate", Notice: notice.str("id")}, seconds(steps[0].AfterSeconds)); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

// matches reports whether the event's record is one filter, a route's
// filter in the repositories' syntax, keeps.
func (n *Notifier) matches(ctx context.Context, h notifications.Host, event raisedEvent, filter any) (bool, error) {
	object, ok := filter.(map[string]any)
	if !ok {
		return false, refused("a route's filter on %s is an object of fields, not %v", event.Table, filter)
	}
	merged := make(map[string]any, len(object)+1)
	for k, v := range object {
		merged[k] = v
	}
	merged[n.defs.Tables[event.Table].Key] = map[string]any{"eq": event.Key}
	data, err := json.Marshal(merged)
	if err != nil {
		return false, err
	}
	rows, err := h.Find(n.system(ctx), event.Table, data, 1)
	return len(rows) > 0, err
}

// addressOf is recipient's verified or entered address of the kind
// channel sends to, not revoked and not opted out (D157); "" for none.
func (n *Notifier) addressOf(ctx context.Context, h notifications.Host, recipient RecipientRef, channel string) (string, error) {
	if n.identities == nil || !n.identities.holds("principal", recipient.ID) {
		return "", nil
	}
	w := n.identities.where().eq("principal", recipient.ID).eq("kind", n.addressKindOf(channel)).in("trust", "verified", "entered").null("revokedAt")
	if _, ok := n.identities.Columns["optedOutAt"]; ok {
		w.null("optedOutAt")
	}
	rows, err := n.find(ctx, h, w, "", 10)
	if err != nil {
		return "", err
	}
	// A party's identity whose grants have all ended since it was verified
	// may be someone else's now (D157's identityLapsed): it is sent
	// nothing.
	for _, r := range rows {
		lapsed, err := n.lapsed(ctx, h, identityOf(r))
		if err != nil {
			return "", err
		}
		if !lapsed {
			return r.str("address"), nil
		}
	}
	return "", nil
}

func ended(state string) bool {
	return state == "failed" || state == "acknowledged" || state == "read"
}

// fallThrough fails delivery and queues the recipient's next channel.
func (n *Notifier) fallThrough(ctx context.Context, h notifications.Host, delivery row, detail string) error {
	at := n.now()
	if err := n.update(ctx, h, n.deliveries, delivery.str("id"), map[string]any{"state": "failed", "detail": detail, "failedAt": at, "endedAt": at}); err != nil {
		return err
	}
	if delivery.isNull("notice") || delivery.str("purpose") != "notice" {
		return nil
	}
	found, err := n.byID(ctx, h, n.inbox, delivery.str("notice"))
	if err != nil || found == nil {
		return err
	}
	notice := noticeOf(found)
	if notice.closed() {
		return nil
	}
	recipient := notice.recipient()
	p, err := n.preferenceOf(ctx, h, recipient, notice.EventKind, notice.Urgency)
	if err != nil {
		return err
	}
	all, err := n.find(ctx, h, n.deliveries.where().eq("notice", notice.ID), "", 100)
	if err != nil {
		return err
	}
	tried := map[string]bool{}
	for _, d := range all {
		tried[d.str("channelKind")] = true
	}
	for _, next := range outward(p.channels) {
		if !tried[next] {
			attempt := delivery.int("attempt")
			if attempt == 0 {
				attempt = 1
			}
			return n.queueDelivery(ctx, h, notice, recipient, next, attempt+1, 0)
		}
	}
	return nil
}

func (n *Notifier) fallThroughAlone(ctx context.Context, delivery row, detail string) error {
	return n.host.Transaction(ctx, func(h notifications.Host) error { return n.fallThrough(ctx, h, delivery, detail) })
}

func sentState(state providers.DeliveryState) string {
	if state == providers.DeliveryQueued || state == "" {
		return "sent"
	}
	return string(state)
}

func (n *Notifier) deliver(ctx context.Context, id string) error {
	delivery, err := n.byID(ctx, n.host, n.deliveries, id)
	if err != nil || delivery == nil || delivery.str("state") != "queued" {
		return err
	}
	var notice *Notice
	if !delivery.isNull("notice") {
		found, err := n.byID(ctx, n.host, n.inbox, delivery.str("notice"))
		if err != nil {
			return err
		}
		if found != nil {
			value := noticeOf(found)
			notice = &value
		}
	}
	if notice != nil && notice.closed() {
		at := n.now()
		return n.update(ctx, n.host, n.deliveries, id, map[string]any{"state": "acknowledged", "acknowledgedAt": at, "endedAt": at})
	}
	kind := delivery.str("channelKind")
	c, ok, err := n.channelOfKind(ctx, kind, delivery.str("slot"))
	if err != nil {
		return err
	}
	if !ok {
		return n.fallThroughAlone(ctx, delivery, fmt.Sprintf("no %s channel is up", kind))
	}
	var recipient RecipientRef
	if notice != nil {
		recipient = notice.recipient()
	}
	address := delivery.str("address")
	if address == "" && notice != nil {
		if address, err = n.addressOf(ctx, n.host, recipient, kind); err != nil {
			return err
		}
	}
	if address == "" {
		return n.fallThroughAlone(ctx, delivery, fmt.Sprintf("the recipient has no verified %s address", n.addressKindOf(kind)))
	}
	// A notice's recipient is held to its record again at send time: one
	// who lost the record since the event reaches nothing.
	if notice != nil && notice.SubjectTable != "" && notice.SubjectKey != "" && !n.reads(ctx, n.host, recipient, notice.SubjectTable, notice.SubjectKey) {
		at := n.now()
		return n.update(ctx, n.host, n.deliveries, id, map[string]any{"state": "failed", "detail": "withheld", "failedAt": at, "endedAt": at})
	}
	text := Rendered{Subject: delivery.str("detail"), Body: delivery.str("detail")}
	message := providers.OutboundMessage{To: address, IdempotencyKey: id}
	if notice != nil {
		text = n.render(*notice, kind)
		message.Reference = strconv.FormatInt(notice.Reference, 10)
	}
	message.Subject, message.Body = text.Subject, text.Body
	sent, err := c.adapter.Send(ctx, message)
	if err != nil {
		n.log.Warn("send failed", zap.String("slot", c.slot), zap.String("delivery", id), zap.String("detail", err.Error()))
		delivery["slot"] = c.slot
		return n.fallThroughAlone(ctx, delivery, err.Error())
	}
	state := sentState(sent.State)
	at := n.now()
	values := map[string]any{"state": state, "slot": c.slot, "address": address, "providerMessageId": nullable(sent.ProviderMessageID), "sentAt": at}
	if state == "delivered" {
		values["deliveredAt"] = at
	}
	if err := n.update(ctx, n.host, n.deliveries, id, values); err != nil {
		return err
	}
	_, reports := c.adapter.(providers.ChannelStatusReader)
	switch {
	case reports && state != "delivered" && state != "read":
		return n.host.Enqueue(ctx, notifications.Work{Step: "timeout", Delivery: id}, n.timeout)
	case !reports:
		return n.update(ctx, n.host, n.deliveries, id, map[string]any{"endedAt": n.now()})
	}
	return nil
}

func (n *Notifier) timeoutOf(ctx context.Context, id string) error {
	delivery, err := n.byID(ctx, n.host, n.deliveries, id)
	if err != nil || delivery == nil || delivery.str("state") != "sent" {
		return err
	}
	return n.fallThroughAlone(ctx, delivery, fmt.Sprintf("not delivered within %d s", int(n.timeout.Seconds())))
}

func (n *Notifier) escalate(ctx context.Context, noticeID string) error {
	return n.host.Transaction(ctx, func(h notifications.Host) error {
		found, err := n.byID(ctx, h, n.inbox, noticeID)
		if err != nil || found == nil {
			return err
		}
		notice := noticeOf(found)
		if notice.closed() || notice.Route == "" {
			return nil
		}
		routeRow, err := n.byID(ctx, h, n.routes, notice.Route)
		if err != nil || routeRow == nil {
			return err
		}
		steps := stepsOf(routeRow["escalation"])
		index := int(notice.Escalation)
		if index >= len(steps) {
			return nil
		}
		step := steps[index]
		if err := n.update(ctx, h, n.inbox, notice.ID, map[string]any{"escalation": index + 1}); err != nil {
			return err
		}
		recipient := notice.recipient()
		tried, err := n.find(ctx, h, n.deliveries.where().eq("notice", notice.ID), "", 100)
		if err != nil {
			return err
		}
		for _, c := range step.Channels {
			if err := n.queueDelivery(ctx, h, notice, recipient, c, int64(len(tried)+1), 0); err != nil {
				return err
			}
		}
		if len(step.Recipients) > 0 {
			event := raisedEvent{ID: notice.EventID, Kind: notice.EventKind, Payload: notice.Payload, At: n.now().UnixMilli(), Table: notice.SubjectTable, Key: notice.SubjectKey}
			more, err := n.recipientsOf(ctx, h, event, step.Recipients)
			if err != nil {
				return err
			}
			var title noticeTitle
			if err := n.notifyAll(ctx, h, more, event, &title, Urgent, notice.Route); err != nil {
				return err
			}
		}
		if index+1 < len(steps) {
			return h.Enqueue(ctx, notifications.Work{Step: "escalate", Notice: notice.ID}, seconds(steps[index+1].AfterSeconds))
		}
		return nil
	})
}

func (n *Notifier) digest(ctx context.Context, recipientID string) error {
	rows, err := n.find(ctx, n.host, n.inbox.where().eq("recipient", recipientID).eq("urgency", Digest).null("digestedAt"), n.inbox.c("reference"), 200)
	if err != nil || len(rows) == 0 {
		return err
	}
	waiting := make([]Notice, len(rows))
	for i, r := range rows {
		waiting[i] = noticeOf(r)
	}
	recipient := waiting[0].recipient()
	p, err := n.preferenceOf(ctx, n.host, recipient, "*", Digest)
	if err != nil {
		return err
	}
	list := outward(p.channels)
	return n.host.Transaction(ctx, func(h notifications.Host) error {
		for _, notice := range waiting {
			if err := n.update(ctx, h, n.inbox, notice.ID, map[string]any{"digestedAt": n.now()}); err != nil {
				return err
			}
		}
		if len(list) == 0 {
			return nil
		}
		lines := make([]string, len(waiting))
		for i, notice := range waiting {
			lines[i] = fmt.Sprintf("#%d %s", notice.Reference, notice.Title)
		}
		delivery, err := n.insert(ctx, h, n.deliveries, map[string]any{"purpose": "digest", "recipient": recipient.ID, "channelKind": list[0], "attempt": 1, "state": "queued", "detail": "Your digest\n" + strings.Join(lines, "\n")})
		if err != nil {
			return err
		}
		return h.Enqueue(ctx, notifications.Work{Step: "deliver", Delivery: delivery.str("id")}, 0)
	})
}

// ScheduleSubscription starts a subscription's brief at its next time, in
// tx (nil for a transaction of its own).
func (n *Notifier) ScheduleSubscription(ctx context.Context, tx any, id string) error {
	if n.subscriptions == nil || n.opts.Expand == nil {
		return refused(`scheduleSubscription: the database declares no @notifier("subscriptions"), or the notifier has no Expand`)
	}
	h, err := n.in(tx)
	if err != nil {
		return err
	}
	if h.Tx() == nil {
		return h.Transaction(ctx, func(h notifications.Host) error { return n.ScheduleSubscription(ctx, h, id) })
	}
	r, err := n.byID(ctx, h, *n.subscriptions, id)
	if err != nil || r == nil || r["enabled"] != true {
		return err
	}
	due, err := n.opts.Expand(ExpandInput{Anchor: "calendar", Rule: r.str("rule"), Start: r.str("start"), TimeZone: r.str("timeZone"), From: n.now().Add(time.Second), Limit: 1})
	if err != nil || len(due) == 0 {
		return err
	}
	if err := n.update(ctx, h, *n.subscriptions, id, map[string]any{"nextAt": due[0]}); err != nil {
		return err
	}
	return h.Enqueue(ctx, notifications.Work{Step: "brief", Subscription: id}, time.Duration(n.secondsUntil(due[0]))*time.Second)
}

func (n *Notifier) brief(ctx context.Context, id string) error {
	if n.subscriptions == nil {
		return nil
	}
	r, err := n.byID(ctx, n.host, *n.subscriptions, id)
	if err != nil || r == nil || r["enabled"] != true {
		return err
	}
	recipient := User(r.str("principal"))
	subject := r.str("subjectTable")
	var lines []string
	if def, ok := n.defs.Tables[subject]; ok {
		// The saved query runs as its recipient: a row they cannot read is
		// not in their brief.
		reader, err := n.readerOf(ctx, n.host, recipient)
		if err != nil {
			return err
		}
		var filter json.RawMessage
		if !r.isNull("filter") {
			if filter, err = json.Marshal(r["filter"]); err != nil {
				return err
			}
		}
		rows, err := n.host.Find(n.host.As(ctx, reader), subject, filter, 50)
		if err != nil {
			return err
		}
		field := r.str("titleField")
		if field == "" {
			field = def.Key
		}
		for _, item := range rows {
			line := textOf(item[field])
			if item[field] == nil {
				line = textOf(item[def.Key])
			}
			lines = append(lines, line)
		}
	}
	text := strings.Join(lines, "\n")
	if text == "" {
		text = "Nothing to report."
	}
	return n.host.Transaction(ctx, func(h notifications.Host) error {
		for _, c := range outward(r.strings("channels")) {
			address, err := n.addressOf(ctx, h, recipient, c)
			if err != nil {
				return err
			}
			delivery, err := n.insert(ctx, h, n.deliveries, map[string]any{"purpose": "digest", "recipient": recipient.ID, "channelKind": c, "address": nullable(address), "attempt": 1, "state": "queued", "detail": r.str("name") + "\n" + text})
			if err != nil {
				return err
			}
			if err := h.Enqueue(ctx, notifications.Work{Step: "deliver", Delivery: delivery.str("id")}, 0); err != nil {
				return err
			}
		}
		if err := n.update(ctx, h, *n.subscriptions, id, map[string]any{"lastSentAt": n.now()}); err != nil {
			return err
		}
		return n.ScheduleSubscription(ctx, h, id)
	})
}

// Handle handles a message of the dispatch queue: the worker of the queue
// calls it.
func (n *Notifier) Handle(ctx context.Context, work notifications.Work) error {
	switch work.Step {
	case "route":
		var event raisedEvent
		if err := json.Unmarshal(work.Event, &event); err != nil {
			return fmt.Errorf("notify: the route step's event: %w", err)
		}
		if event.Payload == nil {
			event.Payload = map[string]any{}
		}
		return n.route(ctx, event)
	case "deliver":
		return n.deliverAny(ctx, work.Delivery)
	case "timeout":
		return n.timeoutOf(ctx, work.Delivery)
	case "escalate":
		return n.escalate(ctx, work.Notice)
	case "digest":
		return n.digest(ctx, work.Recipient)
	case "brief":
		return n.brief(ctx, work.Subscription)
	}
	return refused("the dispatch queue holds a step the notifier does not take: %s", work.Step)
}

// deliverAny delivers a notice's delivery, or one of no notice (a digest,
// an echo, a code) to the address it holds or its recipient's.
func (n *Notifier) deliverAny(ctx context.Context, id string) error {
	delivery, err := n.byID(ctx, n.host, n.deliveries, id)
	if err != nil || delivery == nil {
		return err
	}
	if !delivery.isNull("notice") {
		return n.deliver(ctx, id)
	}
	if delivery.str("state") != "queued" {
		return nil
	}
	kind := delivery.str("channelKind")
	c, ok, err := n.channelOfKind(ctx, kind, delivery.str("slot"))
	if err != nil {
		return err
	}
	address := delivery.str("address")
	if address == "" && !delivery.isNull("recipient") {
		if address, err = n.addressOf(ctx, n.host, User(delivery.str("recipient")), kind); err != nil {
			return err
		}
	}
	at := n.now()
	if !ok || address == "" {
		detail := "no address"
		if !ok {
			detail = fmt.Sprintf("no %s channel is up", kind)
		}
		return n.update(ctx, n.host, n.deliveries, id, map[string]any{"state": "failed", "detail": detail, "failedAt": at, "endedAt": at})
	}
	text := delivery.str("detail")
	subject, rest, split := strings.Cut(text, "\n")
	body := text
	if split {
		body = rest
	}
	sent, err := c.adapter.Send(ctx, providers.OutboundMessage{To: address, Subject: subject, Body: body, IdempotencyKey: id})
	if err != nil {
		return n.update(ctx, n.host, n.deliveries, id, map[string]any{"state": "failed", "slot": c.slot, "address": address, "detail": err.Error(), "failedAt": at, "endedAt": at})
	}
	return n.update(ctx, n.host, n.deliveries, id, map[string]any{"state": sentState(sent.State), "slot": c.slot, "address": address, "providerMessageId": nullable(sent.ProviderMessageID), "sentAt": at, "endedAt": at})
}

// WorkerQueue is the dispatch queue as a worker claims it, for worker.Run
// or worker.RunAll: each message handed to Handle, and its prune the
// queue's and the deliveries' past retention.
func (n *Notifier) WorkerQueue() worker.Queue {
	queue := n.host.Dispatch()
	return worker.Queue{
		Name:  queue.Name(),
		Lease: queue.Lease(),
		Claim: func(ctx context.Context, count int, lease time.Duration) ([]worker.Message, error) {
			claims, err := queue.Claim(ctx, count, lease)
			out := make([]worker.Message, 0, len(claims))
			for _, claim := range claims {
				work := claim.Work
				out = append(out, worker.Message{ID: claim.ID, Token: claim.Token, Attempt: claim.Attempt, Handle: func(ctx context.Context) error { return n.Handle(ctx, work) }})
			}
			return out, err
		},
		Extend:   queue.Extend,
		Complete: queue.Complete,
		Fail:     queue.Fail,
		Release:  queue.Release,
		Prune: func(ctx context.Context, limit int) (int, error) {
			done, err := queue.Prune(ctx, limit)
			if err != nil {
				return done, err
			}
			pruned, err := n.Prune(ctx, limit)
			return done + pruned, err
		},
		Slot:    queue.Slot,
		NextDue: queue.NextDue,
	}
}

// Prune deletes up to limit deliveries that ended past their retention,
// and returns how many.
func (n *Notifier) Prune(ctx context.Context, limit int) (int, error) {
	t := n.deliveries
	deleted, err := n.host.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s IN (SELECT %s FROM %s WHERE %s < $1 LIMIT $2)", t.Table, t.c("id"), t.c("id"), t.Table, t.c("endedAt")), n.now().Add(-n.retain), limit)
	return int(deleted), err
}

// ---- the inbox ----

// Inbox is recipient's notices, newest first: of states, when given, and
// up to limit (50 unless set).
func (n *Notifier) Inbox(ctx context.Context, recipient RecipientRef, states []string, limit int) ([]Notice, error) {
	if limit <= 0 {
		limit = 50
	}
	if !n.inbox.holds("recipient", recipient.ID) {
		return []Notice{}, nil
	}
	w := n.inbox.where().eq("recipient", recipient.ID)
	if len(states) > 0 {
		w.in("state", states...)
	}
	rows, err := n.find(ctx, n.host, w, n.inbox.c("reference")+" DESC", limit)
	if err != nil {
		return nil, err
	}
	out := make([]Notice, len(rows))
	for i, r := range rows {
		out[i] = noticeOf(r)
	}
	return out, nil
}

func (n *Notifier) ownNotice(ctx context.Context, recipient RecipientRef, id string) (Notice, error) {
	found, err := n.byID(ctx, n.host, n.inbox, id)
	if err != nil {
		return Notice{}, err
	}
	if found == nil || SameID(found.str("recipient")) != SameID(recipient.ID) {
		return Notice{}, refused("no such notice of the caller")
	}
	return noticeOf(found), nil
}

// MarkRead marks recipient's notice read.
func (n *Notifier) MarkRead(ctx context.Context, recipient RecipientRef, id string) error {
	notice, err := n.ownNotice(ctx, recipient, id)
	if err != nil || notice.State != "unread" {
		return err
	}
	return n.update(ctx, n.host, n.inbox, notice.ID, map[string]any{"state": "read", "readAt": n.now()})
}

// Acknowledge acknowledges recipient's notice on via (inapp unless set),
// which stops its escalation.
func (n *Notifier) Acknowledge(ctx context.Context, recipient RecipientRef, id, via string) error {
	if via == "" {
		via = InApp
	}
	notice, err := n.ownNotice(ctx, recipient, id)
	if err != nil || notice.closed() {
		return err
	}
	return n.host.Transaction(ctx, func(h notifications.Host) error { return n.acknowledgeIn(ctx, h, notice, via) })
}

func (n *Notifier) acknowledgeIn(ctx context.Context, h notifications.Host, notice Notice, via string) error {
	at := n.now()
	if err := n.update(ctx, h, n.inbox, notice.ID, map[string]any{"state": "acknowledged", "acknowledgedAt": at, "acknowledgedVia": via}); err != nil {
		return err
	}
	return n.endDeliveries(ctx, h, notice.ID, func(state string) bool { return state == "queued" || state == "sent" || state == "delivered" })
}

// endDeliveries marks the deliveries of notice whose state open holds
// acknowledged.
func (n *Notifier) endDeliveries(ctx context.Context, h notifications.Host, notice string, open func(string) bool) error {
	all, err := n.find(ctx, h, n.deliveries.where().eq("notice", notice), "", 100)
	if err != nil {
		return err
	}
	at := n.now()
	for _, d := range all {
		if open(d.str("state")) {
			if err := n.update(ctx, h, n.deliveries, d.str("id"), map[string]any{"state": "acknowledged", "acknowledgedAt": at, "endedAt": at}); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- identities ----

// ChannelIdentity is a channel identity as the notifier reads it.
type ChannelIdentity struct {
	ID             string
	Kind           string
	Address        string
	PrincipalKind  string
	PrincipalTable string
	Principal      string
	Trust          string
	VerifiedAt     *time.Time
	CreatedAt      *time.Time
	OptedOutAt     *time.Time

	row row
}

func identityOf(r row) ChannelIdentity {
	return ChannelIdentity{ID: r.str("id"), Kind: r.str("kind"), Address: r.str("address"), PrincipalKind: r.str("principalKind"), PrincipalTable: r.str("principalTable"), Principal: r.str("principal"), Trust: r.str("trust"), VerifiedAt: r.time("verifiedAt"), CreatedAt: r.time("createdAt"), OptedOutAt: r.time("optedOutAt"), row: r}
}

func (n *Notifier) identitiesTable() (table, error) {
	if n.identities == nil {
		return table{}, refused("the database declares no @channelIdentities table")
	}
	return *n.identities, nil
}

// AddIdentityInput is what AddIdentity takes: an address of a principal,
// unverified until it replies with the code StartVerification sends, or
// entered by hand, which resolves at the lowest trust.
type AddIdentityInput struct {
	Kind      string
	Address   string
	Principal RecipientRef
	Trust     string
}

// AddIdentity adds an address of a principal.
func (n *Notifier) AddIdentity(ctx context.Context, input AddIdentityInput) (ChannelIdentity, error) {
	t, err := n.identitiesTable()
	if err != nil {
		return ChannelIdentity{}, err
	}
	trust := input.Trust
	if trust == "" {
		trust = "unverified"
	}
	kind := input.Principal.Kind
	if kind == "" {
		kind = "user"
	}
	values := map[string]any{"kind": input.Kind, "address": NormaliseAddress(input.Kind, input.Address), "principalKind": kind, "principalTable": nullable(input.Principal.Table), "principal": input.Principal.ID, "trust": trust}
	if trust == "entered" {
		values["verifiedAt"] = n.now()
	}
	created, err := n.insert(ctx, n.host, t, values)
	if err != nil {
		return ChannelIdentity{}, err
	}
	return identityOf(created), nil
}

// ResolveSender is the identities a sender's address resolves through:
// verified or entered, not revoked.
func (n *Notifier) ResolveSender(ctx context.Context, kind, address string) ([]ChannelIdentity, error) {
	t, err := n.identitiesTable()
	if err != nil {
		return nil, err
	}
	rows, err := n.find(ctx, n.host, t.where().eq("kind", kind).eq("address", NormaliseAddress(kind, address)).in("trust", "verified", "entered").null("revokedAt"), "", 10)
	if err != nil {
		return nil, err
	}
	out := make([]ChannelIdentity, len(rows))
	for i, r := range rows {
		out[i] = identityOf(r)
	}
	return out, nil
}

// SendDirectInput is a message to an address on a channel kind with no
// notice: a code, a reset link.
type SendDirectInput struct {
	Channel   string
	To        string
	Subject   string
	Body      string
	Recipient string
	Purpose   string
}

// SendDirect sends input and returns its delivery's id.
func (n *Notifier) SendDirect(ctx context.Context, input SendDirectInput) (string, error) {
	c, ok, err := n.channelOfKind(ctx, input.Channel, "")
	if err != nil {
		return "", err
	}
	purpose := input.Purpose
	if purpose == "" {
		purpose = "code"
	}
	created, err := n.insert(ctx, n.host, n.deliveries, map[string]any{"purpose": purpose, "recipient": nullable(input.Recipient), "channelKind": input.Channel, "address": input.To, "attempt": 1, "state": "queued"})
	if err != nil {
		return "", err
	}
	id := created.str("id")
	at := n.now()
	if !ok {
		if err := n.update(ctx, n.host, n.deliveries, id, map[string]any{"state": "failed", "detail": fmt.Sprintf("no %s channel is up", input.Channel), "failedAt": at, "endedAt": at}); err != nil {
			return "", err
		}
		return "", refused("no %s channel is up", input.Channel)
	}
	sent, err := c.adapter.Send(ctx, providers.OutboundMessage{To: input.To, Subject: input.Subject, Body: input.Body, IdempotencyKey: id})
	if err != nil {
		_ = n.update(ctx, n.host, n.deliveries, id, map[string]any{"state": "failed", "slot": c.slot, "detail": err.Error(), "failedAt": at, "endedAt": at})
		return "", err
	}
	if err := n.update(ctx, n.host, n.deliveries, id, map[string]any{"state": sentState(sent.State), "slot": c.slot, "providerMessageId": nullable(sent.ProviderMessageID), "sentAt": at, "endedAt": at}); err != nil {
		return "", err
	}
	return id, nil
}

func randomCode() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(b[:])%1_000_000), nil
}

// StartVerification sends a code to an identity's address, which a reply
// on that channel, or ConfirmIdentity, answers; purpose reset (D81) is a
// password reset's.
func (n *Notifier) StartVerification(ctx context.Context, id, purpose string) error {
	t, err := n.identitiesTable()
	if err != nil {
		return err
	}
	if purpose == "" {
		purpose = "verify"
	}
	found, err := n.byID(ctx, n.host, t, id)
	if err != nil {
		return err
	}
	if found == nil {
		return refused("no such channel identity")
	}
	code, err := randomCode()
	if err != nil {
		return err
	}
	if err := n.update(ctx, n.host, t, id, map[string]any{"codeHash": CodeHash(code), "codePurpose": purpose, "codeExpiresAt": n.now().Add(CodeTTLSeconds * time.Second), "codeAttempts": 0}); err != nil {
		return err
	}
	input := SendDirectInput{Channel: n.channelKindOfAddress(found.str("kind")), To: found.str("address"), Recipient: found.str("principal"), Purpose: "code",
		Subject: "Your verification code", Body: fmt.Sprintf("Your code is %s. Reply with it to confirm this address.", code)}
	if purpose == "reset" {
		input.Subject, input.Body = "Your password reset code", fmt.Sprintf("Your password reset code is %s. It expires in %d minutes.", code, CodeTTLSeconds/60)
	}
	_, err = n.SendDirect(ctx, input)
	return err
}

// checkCode checks code against identity's pending code of purpose; a
// wrong one counts, and the last attempt spends it. A right one verifies
// the address, unless spend is false, which leaves that to the caller.
func (n *Notifier) checkCode(ctx context.Context, identity row, code, purpose string) (bool, error) {
	t, err := n.identitiesTable()
	if err != nil {
		return false, err
	}
	expires := identity.time("codeExpiresAt")
	if identity.str("codePurpose") != purpose || identity.str("codeHash") == "" || expires == nil || expires.Before(n.now()) {
		return false, nil
	}
	id := identity.str("id")
	if identity.str("codeHash") != CodeHash(code) {
		attempts := identity.int("codeAttempts") + 1
		values := map[string]any{"codeAttempts": attempts}
		if attempts >= CodeAttempts {
			values = map[string]any{"codeHash": nil, "codePurpose": nil, "codeExpiresAt": nil, "codeAttempts": attempts}
		}
		return false, n.update(ctx, n.host, t, id, values)
	}
	// A code answered verifies the address now.
	return true, n.update(ctx, n.host, t, id, map[string]any{"codeHash": nil, "codePurpose": nil, "codeExpiresAt": nil, "codeAttempts": 0, "trust": "verified", "verifiedAt": n.now()})
}

// ConfirmIdentity checks a code given in the app, and reports whether it
// verified the identity.
func (n *Notifier) ConfirmIdentity(ctx context.Context, id, code string) (bool, error) {
	t, err := n.identitiesTable()
	if err != nil {
		return false, err
	}
	found, err := n.byID(ctx, n.host, t, id)
	if err != nil || found == nil {
		return false, err
	}
	return n.checkCode(ctx, found, code, "verify")
}
