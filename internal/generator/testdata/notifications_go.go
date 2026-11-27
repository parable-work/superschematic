// fixture-notifications-db end to end in the Go repositories and the HTTP
// runtime's notify package (D153, D190), run by TestGoNotificationsEndToEnd:
// tickets that raise events, some named to a few readers, approvals asked
// of those who hold their permission, the notifier's tables, its dispatch
// queue and the channel identities replies resolve through. The generated
// create.sql is applied in a schema of the test's own on Postgres, and the
// cases are the TypeScript repositories' (tsgen's notifications.test.ts)
// and the Rust ones' (D191), through notify over the ORM's Notifications,
// with an SMS relay and a mail relay as stand-in channel adapters (D159).
// drain runs what the dispatch queue's worker runs: each due message,
// handed to Notifier.Handle.
package orm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/parable-work/superschematic/runtime/http/go/identity"
	"github.com/parable-work/superschematic/runtime/http/go/notify"
	"github.com/parable-work/superschematic/runtime/http/go/providers"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"

	orm "example.com/schemas/orm/fixture-notifications-db"
	types "example.com/schemas/types/go/fixture-notifications-db"
)

const relayToken = "relay-token"

const (
	phoneAnn, phoneBen, phoneCal = "+15550100001", "+15550100002", "+15550100003"
	emailAnn, emailBen, emailCal = "ann@example.test", "ben@example.test", "cal@example.test"
)

var system = orm.AsSystem(context.Background())

// must is a call's value, which must succeed.
func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// sent is what a stand-in relay was sent.
type sent struct{ to, subject, text, id string }

// relays are the stand-in SMS relay and mail relay: what each was sent,
// and the switches that make them fail.
type relays struct {
	mu     sync.Mutex
	sms    []sent
	mail   []sent
	down   bool
	failTo map[string]bool
	n      int
}

func (r *relays) smsSent() []sent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sms)
}

func (r *relays) mailSent() []sent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.mail)
}

func to(list []sent) []string {
	out := make([]string, len(list))
	for i, m := range list {
		out[i] = m.to
	}
	slices.Sort(out)
	return out
}

// smsRelay sends, reports health, and reads its callbacks' statuses and
// inbound messages, holding each to the relay's token.
type smsRelay struct{ r *relays }

func (a smsRelay) Kind() string { return "sms" }

func (a smsRelay) Send(_ context.Context, m providers.OutboundMessage) (providers.SendResult, error) {
	a.r.mu.Lock()
	defer a.r.mu.Unlock()
	if a.r.failTo[m.To] {
		return providers.SendResult{}, errors.New("the relay answered 500")
	}
	a.r.n++
	id := fmt.Sprintf("sms-%d", a.r.n)
	a.r.sms = append(a.r.sms, sent{to: m.To, text: m.Body, id: id})
	return providers.SendResult{ProviderMessageID: id, State: providers.DeliveryQueued}, nil
}

func (a smsRelay) Health(context.Context) (providers.ChannelHealth, error) {
	a.r.mu.Lock()
	defer a.r.mu.Unlock()
	if a.r.down {
		return providers.ChannelHealth{OK: false, Detail: "the relay answered 503"}, nil
	}
	return providers.ChannelHealth{OK: true}, nil
}

func checked(request providers.WebhookRequest, into any) error {
	if request.Headers["x-relay-token"] != relayToken {
		return errors.New("the callback holds no relay token")
	}
	return json.Unmarshal([]byte(request.Body), into)
}

func (a smsRelay) Status(_ context.Context, request providers.WebhookRequest) ([]providers.DeliveryStatus, error) {
	var body struct {
		Events []struct {
			ID, Status string
			At         time.Time
		}
	}
	if err := checked(request, &body); err != nil {
		return nil, err
	}
	var out []providers.DeliveryStatus
	for _, e := range body.Events {
		out = append(out, providers.DeliveryStatus{ProviderMessageID: e.ID, State: providers.DeliveryState(e.Status), At: e.At})
	}
	return out, nil
}

func (a smsRelay) Inbound(_ context.Context, request providers.WebhookRequest) ([]providers.InboundMessage, error) {
	var body struct {
		Messages []struct {
			ID, From, Text string
			At             time.Time
		}
	}
	if err := checked(request, &body); err != nil {
		return nil, err
	}
	var out []providers.InboundMessage
	for _, m := range body.Messages {
		out = append(out, providers.InboundMessage{From: m.From, Body: m.Text, ReceivedAt: m.At, ProviderMessageID: m.ID})
	}
	return out, nil
}

// mailRelay sends and reports no statuses.
type mailRelay struct{ r *relays }

func (a mailRelay) Kind() string { return "email" }

func (a mailRelay) Send(_ context.Context, m providers.OutboundMessage) (providers.SendResult, error) {
	a.r.mu.Lock()
	defer a.r.mu.Unlock()
	a.r.n++
	id := fmt.Sprintf("mail-%d", a.r.n)
	a.r.mail = append(a.r.mail, sent{to: m.To, subject: m.Subject, text: m.Body, id: id})
	return providers.SendResult{ProviderMessageID: id, State: providers.DeliverySent}, nil
}

func (a mailRelay) Health(context.Context) (providers.ChannelHealth, error) {
	return providers.ChannelHealth{OK: true}, nil
}

// markDone is the reply action of done: marks the ticket done as the
// replier, through the ticket's repository in the reply's transaction, and
// puts its status back on undo.
var markDone = notify.ReplyAction{
	Permission: "tickets.work",
	Describe:   "Marked done",
	Run: func(rc notify.ReplyContext) (any, error) {
		tickets := rc.Tx.(orm.TxInterface).GetTicketRepository()
		key := must(types.ParseIdentityUUID(rc.Notice.SubjectKey))
		before, err := tickets.GetOne(rc.Ctx, key, nil)
		if err != nil {
			return nil, err
		}
		done := "done"
		if _, err := tickets.UpdateOne(rc.Ctx, key, &orm.TicketUpdate{Status: &done}); err != nil {
			return nil, err
		}
		return map[string]string{"status": before.Status}, nil
	},
	Undo: func(rc notify.ReplyContext, data json.RawMessage) error {
		var was struct{ Status string }
		if err := json.Unmarshal(data, &was); err != nil {
			return err
		}
		key := must(types.ParseIdentityUUID(rc.Notice.SubjectKey))
		_, err := rc.Tx.(orm.TxInterface).GetTicketRepository().UpdateOne(rc.Ctx, key, &orm.TicketUpdate{Status: &was.Status})
		return err
	},
}

// world is a schema of its own holding fixture-notifications-db's tables,
// the notifier over it, and the people and the ticket of every case.
type world struct {
	t             *testing.T
	pool          *pgxpool.Pool
	db            *orm.Database
	notifier      *notify.Notifier
	relays        *relays
	ann, ben, cal string
	ticket        string
}

type options struct {
	now    func() time.Time
	expand func(notify.ExpandInput) ([]time.Time, error)
}

func newWorld(t *testing.T, opts options) *world {
	t.Helper()
	target := os.Getenv("NOTIFY_TEST_DATABASE_URL")
	if target == "" {
		t.Skip("set NOTIFY_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin := must(pgxpool.New(ctx, target))
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("notify_go_%d", time.Now().UnixNano())
	must(admin.Exec(ctx, "CREATE SCHEMA "+schema))
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg := must(pgxpool.ParseConfig(target))
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool := must(pgxpool.NewWithConfig(ctx, cfg))
	t.Cleanup(pool.Close)
	ddl := must(os.ReadFile("testdata/create.sql"))
	if _, err := pool.Exec(ctx, string(ddl)); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	db := must(orm.ConnectWithPool(pool))

	r := &relays{failTo: map[string]bool{}}
	channel := func(name string, adapter providers.ChannelAdapter) providers.Definition {
		return providers.MustDefine(providers.Definition{
			Name: name, Implements: providers.Channel, Role: []string{"messages.relay"},
			Create: func(context.Context, providers.Settings, providers.Context) (any, error) { return adapter, nil },
		})
	}
	p := must(providers.From(providers.Options{
		Slots:       map[string]providers.Interface{"SMS": providers.Channel, "MAIL": providers.Channel},
		Config:      map[string]providers.ConfigValue{"SMS": providers.Text("sms-relay"), "MAIL": providers.Text("mail-relay")},
		Definitions: []providers.Definition{channel("sms-relay", smsRelay{r}), channel("mail-relay", mailRelay{r})},
	}))
	replies := map[string]map[string]notify.ReplyAction{
		"ticket.overdue":     {"done": markDone},
		"ticket.assigned":    {"done": markDone},
		"approval.requested": notify.ApprovalReplies("Approval", "agents.approve"),
	}
	n := must(notify.New(db.Notifications(), notify.Options{Providers: p, Replies: replies, Now: opts.now, Expand: opts.expand}))
	w := &world{t: t, pool: pool, db: db, notifier: n, relays: r}

	account := func(email string) string {
		return must(db.GetAccountRepository().CreateOne(system, &types.Account{Email: types.ContactEmail(email)})).Id.String()
	}
	w.ann, w.ben, w.cal = account(emailAnn), account(emailBen), account(emailCal)
	worker := must(db.GetRoleRepository().CreateOne(system, &types.Role{Name: "worker", Permissions: []string{"tickets.work"}}))
	for _, user := range []string{w.ann, w.ben} {
		w.grant(user, *worker.Id)
	}
	// Ann owns the ticket; Ben is named on it; Cal watches it and may not
	// read it.
	ticket := must(db.GetTicketRepository().CreateOne(system, &types.Ticket{Title: "Boiler pressure low", Status: "open", Owner: w.id(w.ann), Watchers: []types.IdentityUUID{w.id(w.ben), w.id(w.cal)}, Audience: "named", AudienceNamed: []types.IdentityUUID{w.id(w.ben)}, AgentAccess: "followRole"}))
	w.ticket = ticket.Id.String()
	for _, who := range [][3]string{{w.ann, phoneAnn, emailAnn}, {w.ben, phoneBen, emailBen}, {w.cal, phoneCal, emailCal}} {
		w.exec(`INSERT INTO channel_identity (kind, address, principal, trust, verified_at) VALUES ('phone', $1, $2, 'verified', now()), ('email', $3, $2, 'verified', now())`, who[1], rowrefs.UUIDToDB(who[0]), who[2])
	}
	w.exec(`INSERT INTO notice_preference (principal, event_kind, urgency, channels) VALUES (NULL, '*', '*', '{sms,email}')`)
	w.exec(`INSERT INTO channel_trust (channel_kind, level, permissions) VALUES ('sms', 'low', '{}'), ('email', 'standard', NULL)`)
	w.route("Overdue tickets page their people", "ticket.overdue", `[{"path":"owner"},{"path":"watchers"}]`, `[{"afterSeconds":600,"channels":["email"]}]`)
	return w
}

func (w *world) id(text string) types.IdentityUUID { return must(types.ParseIdentityUUID(text)) }

func (w *world) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.pool.Exec(context.Background(), sql, args...); err != nil {
		w.t.Fatalf("%s: %v", sql, err)
	}
}

func (w *world) grant(user string, role types.IdentityUUID) {
	must(w.db.GetUserRoleGrantRepository().CreateOne(system, &types.UserRoleGrant{User: types.Account{Id: ptr(w.id(user))}, Role: types.Role{Id: &role}, GrantedAt: types.TemporalDateTime(time.Now())}))
}

func ptr[T any](v T) *T { return &v }

func (w *world) route(name, kind, recipients, escalation string) {
	w.t.Helper()
	var steps any
	if escalation != "" {
		steps = escalation
	}
	w.exec(`INSERT INTO notice_route (name, event_kind, recipients, escalation) VALUES ($1, $2, $3::jsonb, $4::jsonb)`, name, kind, recipients, steps)
}

// drain runs every message of the dispatch queue that is due, until none
// is, as its worker would.
func (w *world) drain() int {
	w.t.Helper()
	queue := w.notifier.WorkerQueue()
	ctx := context.Background()
	handled := 0
	for round := 0; round < 50; round++ {
		claims := must(queue.Claim(ctx, 20, 30*time.Second))
		if len(claims) == 0 {
			return handled
		}
		for _, claim := range claims {
			if err := claim.Handle(ctx); err != nil {
				w.t.Fatalf("the dispatch queue's message %s: %v", claim.ID, err)
			}
			check(w.t, queue.Complete(ctx, claim.ID, claim.Token))
			handled++
		}
	}
	w.t.Fatal("the dispatch queue did not drain")
	return handled
}

// advance makes the queue's waiting messages of step due now: time
// passing.
func (w *world) advance(step string) {
	w.exec(`UPDATE notice_work_queue SET due_at = now() WHERE step = $1 AND state = 'ready'`, step)
}

func (w *world) raise(input notify.RaiseInput) (string, error) {
	var id string
	err := w.db.Transaction(context.Background(), func(tx orm.TxInterface) error {
		var err error
		id, err = w.notifier.Raise(context.Background(), tx, input)
		return err
	})
	return id, err
}

func (w *world) overdue() string {
	return must(w.raise(notify.RaiseInput{Table: "Ticket", Key: w.ticket, Kind: "ticket.overdue"}))
}

func (w *world) notices(user string) []notify.Notice {
	return must(w.notifier.Inbox(context.Background(), notify.User(user), nil, 0))
}

func (w *world) reply(channel, from, body string) notify.ReplyOutcome {
	w.t.Helper()
	return must(w.notifier.Reply(context.Background(), notify.ReplyInput{Channel: channel, From: from, Body: body}))
}

func (w *world) deliveriesOf(user string) []*types.Delivery {
	id := w.id(user)
	rows, _ := must2(w.db.GetDeliveryRepository().FindMany(system, &orm.DeliveryFilter{Recipient: &orm.UUIDFilter{Eq: &id}}, &orm.DeliveryFindOptions{Fields: orm.DeliveryFields{}.All(), OrderBy: []orm.DeliveryOrderBy{{Field: orm.DeliveryOrderByQueuedAt}}, Limit: 100}))
	return rows
}

func must2[A, B any](a A, b B, err error) (A, B) {
	if err != nil {
		panic(err)
	}
	return a, b
}

func (w *world) ticketStatus() string {
	return must(w.db.GetTicketRepository().GetOne(system, w.id(w.ticket), nil)).Status
}

func find[T any](list []T, keep func(T) bool) (T, bool) {
	for _, item := range list {
		if keep(item) {
			return item, true
		}
	}
	var zero T
	return zero, false
}

var sixDigits = regexp.MustCompile(`(\d{6})`)

func TestNotifications(t *testing.T) {
	ctx := context.Background()

	t.Run("an urgent event reaches two recipients by SMS, skips one who cannot read the record, and escalates to email when not acknowledged in time", func(t *testing.T) {
		w := newWorld(t, options{})
		event := w.overdue()
		w.drain()
		if got := to(w.relays.smsSent()); !slices.Equal(got, []string{phoneAnn, phoneBen}) {
			t.Fatalf("sms to %v", got)
		}
		first := w.relays.smsSent()[0].text
		if !strings.Contains(first, "Boiler pressure low") || !strings.Contains(first, "ACK 1") {
			t.Fatalf("the sms says %q", first)
		}
		ann := w.notices(w.ann)
		if len(ann) != 1 || ann[0].EventID != event || ann[0].Urgency != "urgent" || fmt.Sprint(ann[0].Payload) != "map[title:Boiler pressure low]" {
			t.Fatalf("ann's notices: %+v", ann)
		}
		// A kind the table does not declare is refused, and its
		// transaction with it.
		if _, err := w.raise(notify.RaiseInput{Table: "Ticket", Key: w.ticket, Kind: "ticket.lost"}); err == nil || !strings.Contains(err.Error(), "declares no event ticket.lost") {
			t.Fatalf("an undeclared kind: %v", err)
		}
		// Cal cannot read the ticket: no notice, no delivery, no trace.
		if got := w.notices(w.cal); len(got) != 0 {
			t.Fatalf("cal's notices: %+v", got)
		}
		if got := w.deliveriesOf(w.cal); len(got) != 0 {
			t.Fatalf("cal's deliveries: %+v", got)
		}
		if got := w.relays.mailSent(); len(got) != 0 {
			t.Fatalf("mail before the escalation: %+v", got)
		}
		// Ten minutes on, nobody has acknowledged: email follows.
		w.advance("escalate")
		w.drain()
		if got := to(w.relays.mailSent()); !slices.Equal(got, []string{emailAnn, emailBen}) {
			t.Fatalf("mail to %v", got)
		}
		var got [][3]string
		for _, d := range w.deliveriesOf(w.ann) {
			got = append(got, [3]string{d.ChannelKind, d.State, fmt.Sprint(d.Attempt)})
		}
		if want := [][3]string{{"sms", "sent", "1"}, {"email", "sent", "2"}}; !slices.Equal(got, want) {
			t.Fatalf("ann's deliveries: %v, want %v", got, want)
		}
	})

	t.Run("a reply of ack acknowledges the notice and stops its escalation, and undo takes it back", func(t *testing.T) {
		w := newWorld(t, options{})
		w.overdue()
		w.drain()
		acked := w.reply("sms", strings.Replace("(555) 010-0001", "(", "+1", 1), "ack")
		if acked.Outcome != "done" || !strings.Contains(acked.Echo, "UNDO 1") {
			t.Fatalf("ack: %+v", acked)
		}
		if last := w.relays.smsSent(); last[len(last)-1].to != phoneAnn {
			t.Fatalf("the echo went to %s", last[len(last)-1].to)
		}
		notice := w.notices(w.ann)[0]
		if notice.State != "acknowledged" || notice.AcknowledgedVia != "sms" {
			t.Fatalf("the notice: %+v", notice)
		}
		w.advance("escalate")
		w.drain()
		// Ann acknowledged: only Ben's email went out.
		if got := to(w.relays.mailSent()); !slices.Equal(got, []string{emailBen}) {
			t.Fatalf("mail to %v", got)
		}
		if undone := w.reply("sms", phoneAnn, "undo 1"); undone.Outcome != "done" {
			t.Fatalf("undo: %+v", undone)
		}
		if state := w.notices(w.ann)[0].State; state != "unread" {
			t.Fatalf("the notice after undo: %s", state)
		}
	})

	t.Run("a reply of done 142 changes the record as its sender, and the audit names the channel and what read the text", func(t *testing.T) {
		w := newWorld(t, options{})
		// Ann has had 141 notices before this one.
		w.exec(`INSERT INTO notice (recipient, reference, event_id, event_kind, urgency, title, state) VALUES ($1, 141, $2, 'ticket.assigned', 'record', 'Earlier', 'read')`, rowrefs.UUIDToDB(w.ann), uuid.NewString())
		w.overdue()
		w.drain()
		if m, ok := find(w.relays.smsSent(), func(m sent) bool { return m.to == phoneAnn }); !ok || !strings.Contains(m.text, "#142") {
			t.Fatalf("ann's sms: %+v", m)
		}
		done := w.reply("email", "Ann@Example.test", "done 142")
		if done.Outcome != "done" || !strings.Contains(done.Echo, "Marked done 142") || !strings.Contains(done.Echo, "UNDO 142") {
			t.Fatalf("done: %+v", done)
		}
		if status := w.ticketStatus(); status != "done" {
			t.Fatalf("the ticket is %s", status)
		}
		if last := w.relays.mailSent(); last[len(last)-1].to != emailAnn {
			t.Fatalf("the echo went to %s", last[len(last)-1].to)
		}
		verb := "done"
		audit := must(w.db.GetReplyRepository().FindOne(system, &orm.ReplyFilter{Verb: &orm.StringFilter{Eq: &verb}}, nil))
		if audit.ChannelKind != "email" || audit.Address != emailAnn || audit.Interpreter != "keyword" || audit.Outcome != "done" || audit.Body != "done 142" || audit.Reference == nil || *audit.Reference != 142 || audit.Principal == nil || audit.Principal.String() != w.ann {
			t.Fatalf("the audit: %+v", audit)
		}
		notice, _ := find(w.notices(w.ann), func(n notify.Notice) bool { return n.Reference == 142 })
		if notice.State != "done" {
			t.Fatalf("notice 142: %+v", notice)
		}
		// Undo puts the ticket back as it was.
		if undone := w.reply("email", emailAnn, "undo 142"); undone.Outcome != "done" {
			t.Fatalf("undo: %+v", undone)
		}
		if status := w.ticketStatus(); status != "open" {
			t.Fatalf("the ticket after undo is %s", status)
		}
	})

	t.Run("a low-trust channel is refused an operation its trust does not allow, and an address entered by hand only acknowledges", func(t *testing.T) {
		w := newWorld(t, options{})
		w.overdue()
		w.drain()
		refused := w.reply("sms", phoneAnn, "done 1")
		if refused.Outcome != "refused" || !strings.Contains(refused.Detail, "its trust is low") {
			t.Fatalf("done by sms: %+v", refused)
		}
		if status := w.ticketStatus(); status != "open" {
			t.Fatalf("the ticket is %s", status)
		}
		outcome := "refused"
		audit := must(w.db.GetReplyRepository().FindOne(system, &orm.ReplyFilter{Outcome: &orm.StringFilter{Eq: &outcome}}, nil))
		if audit.ChannelKind != "sms" || audit.Verb != "done" {
			t.Fatalf("the audit: %+v", audit)
		}
		// Ben's email, entered by hand, is trusted only to acknowledge.
		w.exec(`UPDATE channel_identity SET trust = 'entered' WHERE principal = $1 AND kind = 'email'`, rowrefs.UUIDToDB(w.ben))
		byHand := w.reply("email", emailBen, "done 1")
		if byHand.Outcome != "refused" || !strings.Contains(byHand.Detail, "entered by hand") {
			t.Fatalf("done entered by hand: %+v", byHand)
		}
		if acked := w.reply("email", emailBen, "ack 1"); acked.Outcome != "done" {
			t.Fatalf("ack entered by hand: %+v", acked)
		}
	})

	t.Run("a reply names its notice by reference unless exactly one waits on its sender", func(t *testing.T) {
		w := newWorld(t, options{})
		w.overdue()
		must(w.raise(notify.RaiseInput{Table: "Ticket", Key: w.ticket, Kind: "ticket.overdue", ID: uuid.NewString()}))
		w.drain()
		ambiguous := w.reply("sms", phoneAnn, "ack")
		if ambiguous.Outcome != "ambiguous" || !strings.Contains(ambiguous.Detail, "#2") {
			t.Fatalf("ack of two: %+v", ambiguous)
		}
		if acked := w.reply("sms", phoneAnn, "ack 2"); acked.Outcome != "done" {
			t.Fatalf("ack 2: %+v", acked)
		}
		// One waits now: ack alone takes it.
		if one := w.reply("sms", phoneAnn, "OK"); one.Outcome != "done" {
			t.Fatalf("OK: %+v", one)
		}
		for _, n := range w.notices(w.ann) {
			if n.State != "acknowledged" {
				t.Fatalf("notice %d is %s", n.Reference, n.State)
			}
		}
		if none := w.reply("sms", phoneAnn, "ack 9"); none.Outcome != "refused" {
			t.Fatalf("ack 9: %+v", none)
		}
	})

	t.Run("an address is verified by the code sent to it and replied with, and an unknown sender reaches nothing", func(t *testing.T) {
		w := newWorld(t, options{})
		dana := must(w.db.GetAccountRepository().CreateOne(system, &types.Account{Email: "dana@example.test"})).Id.String()
		identity := must(w.notifier.AddIdentity(ctx, notify.AddIdentityInput{Kind: "phone", Address: "+1 555 010 0009", Principal: notify.User(dana)}))
		if identity.Trust != "unverified" {
			t.Fatalf("a new identity: %+v", identity)
		}
		if got := must(w.notifier.ResolveSender(ctx, "phone", "+15550100009")); len(got) != 0 {
			t.Fatalf("an unverified address resolves: %+v", got)
		}
		check(t, w.notifier.StartVerification(ctx, identity.ID, ""))
		last := w.relays.smsSent()[len(w.relays.smsSent())-1]
		if last.to != "+15550100009" {
			t.Fatalf("the code went to %s", last.to)
		}
		code := sixDigits.FindStringSubmatch(last.text)[1]
		if wrong := w.reply("sms", "+15550100009", "000000"); wrong.Outcome != "unknown" {
			t.Fatalf("a wrong code: %+v", wrong)
		}
		if verified := w.reply("sms", "+15550100009", code); verified.Outcome != "verified" {
			t.Fatalf("the code: %+v", verified)
		}
		resolved := must(w.notifier.ResolveSender(ctx, "phone", "+15550100009"))
		if len(resolved) != 1 || resolved[0].Principal != dana {
			t.Fatalf("resolved: %+v", resolved)
		}
		stranger := w.reply("sms", "+15550109999", "done 1")
		if stranger.Outcome != "unknown" || stranger.Principal != nil {
			t.Fatalf("a stranger: %+v", stranger)
		}
		if status := w.ticketStatus(); status != "open" {
			t.Fatalf("the ticket is %s", status)
		}
	})

	t.Run("a failed send falls through to the next channel, and an adapter that is down is skipped and alerted on the others", func(t *testing.T) {
		w := newWorld(t, options{})
		w.relays.mu.Lock()
		w.relays.failTo[phoneAnn] = true
		w.relays.mu.Unlock()
		w.overdue()
		w.drain()
		if got := to(w.relays.mailSent()); !slices.Equal(got, []string{emailAnn}) {
			t.Fatalf("mail to %v", got)
		}
		var got [][2]string
		for _, d := range w.deliveriesOf(w.ann) {
			got = append(got, [2]string{d.ChannelKind, d.State})
		}
		if want := [][2]string{{"sms", "failed"}, {"email", "sent"}}; !slices.Equal(got, want) {
			t.Fatalf("ann's deliveries: %v, want %v", got, want)
		}
		// The SMS relay goes down: its health check fails, channel.down is
		// raised to the people its route names, and they hear of it by
		// email.
		w.route("Channel alerts", "channel.down", fmt.Sprintf(`[{"principal":%q}]`, w.ben), "")
		w.relays.mu.Lock()
		w.relays.down = true
		w.relays.mu.Unlock()
		health := must(w.notifier.CheckHealth(ctx))
		if sms, _ := find(health, func(h notify.ChannelHealthOf) bool { return h.Slot == "SMS" }); sms.OK {
			t.Fatalf("health: %+v", health)
		}
		w.drain()
		alert, ok := find(w.notices(w.ben), func(n notify.Notice) bool { return n.EventKind == "channel.down" })
		if !ok || alert.SubjectKey != "SMS" {
			t.Fatalf("ben's alert: %+v", w.notices(w.ben))
		}
		if last := w.relays.mailSent(); last[len(last)-1].to != emailBen {
			t.Fatalf("the alert went to %s", last[len(last)-1].to)
		}
		// A second check while it is still down alerts no one again.
		must(w.notifier.CheckHealth(ctx))
		w.drain()
		alerts := 0
		for _, n := range w.notices(w.ben) {
			if n.EventKind == "channel.down" {
				alerts++
			}
		}
		if alerts != 1 {
			t.Fatalf("%d alerts", alerts)
		}
	})

	t.Run("quiet hours hold a normal notice, digests batch, and record sends nothing", func(t *testing.T) {
		fixed := time.Date(2027, 3, 10, 23, 30, 0, 0, time.UTC)
		w := newWorld(t, options{now: func() time.Time { return fixed }, expand: func(notify.ExpandInput) ([]time.Time, error) {
			return []time.Time{fixed.Add(8*time.Hour + 30*time.Minute)}, nil
		}})
		w.exec(`INSERT INTO notice_preference (principal, event_kind, urgency, channels, quiet_start, quiet_end, time_zone) VALUES ($1, '*', 'normal', '{sms}', '22:00', '07:00', 'UTC')`, rowrefs.UUIDToDB(w.ben))
		w.exec(`INSERT INTO notice_preference (principal, event_kind, urgency, channels, time_zone, digest) VALUES ($1, '*', 'digest', '{email}', 'UTC', 'FREQ=DAILY;BYHOUR=8;BYMINUTE=0')`, rowrefs.UUIDToDB(w.ann))
		w.route("Assignments", "ticket.assigned", fmt.Sprintf(`[{"principal":%q}]`, w.ben), "")
		w.route("Summaries", "ticket.summary", `[{"path":"owner"}]`, "")
		w.route("History", "reference.cleared", `[{"path":"owner"}]`, "")
		check(t, w.db.Transaction(ctx, func(tx orm.TxInterface) error {
			for _, input := range []notify.RaiseInput{
				{Table: "Ticket", Key: w.ticket, Kind: "ticket.assigned"},
				{Table: "Ticket", Key: w.ticket, Kind: "ticket.summary"},
				{Table: "Ticket", Key: w.ticket, Kind: "ticket.summary", ID: uuid.NewString()},
				{Table: "Ticket", Key: w.ticket, Kind: "reference.cleared"},
			} {
				if _, err := w.notifier.Raise(ctx, tx, input); err != nil {
					return err
				}
			}
			return nil
		}))
		w.drain()
		if len(w.relays.smsSent()) != 0 || len(w.relays.mailSent()) != 0 {
			t.Fatalf("sent in quiet hours: %+v %+v", w.relays.smsSent(), w.relays.mailSent())
		}
		held := w.deliveriesOf(w.ben)
		if len(held) != 1 || held[0].State != "queued" || held[0].DueAt == nil || time.Time(*held[0].DueAt).UTC().Format(time.RFC3339) != "2027-03-11T07:00:00Z" {
			t.Fatalf("ben's held delivery: %+v", held)
		}
		if payload := fmt.Sprint(w.notices(w.ben)[0].Payload); payload != "map[status:open title:Boiler pressure low]" {
			t.Fatalf("ben's payload: %s", payload)
		}
		record, _ := find(w.notices(w.ann), func(n notify.Notice) bool { return n.EventKind == "reference.cleared" })
		if record.State != "read" {
			t.Fatalf("the record notice: %+v", record)
		}
		// One digest is waiting for Ann's two summaries.
		var steps []string
		rows := must(w.pool.Query(ctx, `SELECT step FROM notice_work_queue WHERE state = 'ready' ORDER BY step`))
		for rows.Next() {
			var step string
			check(t, rows.Scan(&step))
			steps = append(steps, step)
		}
		rows.Close()
		if !slices.Equal(steps, []string{"deliver", "digest"}) {
			t.Fatalf("waiting: %v", steps)
		}
		w.advance("digest")
		w.advance("deliver")
		w.drain()
		if got := to(w.relays.smsSent()); !slices.Equal(got, []string{phoneBen}) {
			t.Fatalf("sms to %v", got)
		}
		mail := w.relays.mailSent()
		if len(mail) != 1 || mail[0].to != emailAnn || len(strings.Split(mail[0].text, "\n")) != 2 {
			t.Fatalf("the digest: %+v", mail)
		}
	})

	t.Run("a delivery's statuses move it on and a cleared reference is recorded on its holder", func(t *testing.T) {
		w := newWorld(t, options{})
		w.overdue()
		w.drain()
		sms, _ := find(w.relays.smsSent(), func(m sent) bool { return m.to == phoneAnn })
		signed := func(body any) providers.WebhookRequest {
			return providers.WebhookRequest{Headers: map[string]string{"x-relay-token": relayToken}, Body: string(must(json.Marshal(body)))}
		}
		moved := must(w.notifier.Statuses(ctx, "SMS", signed(map[string]any{"events": []map[string]any{{"id": sms.id, "status": "delivered", "at": time.Now()}}})))
		if moved != 1 {
			t.Fatalf("%d moved", moved)
		}
		state := func() string {
			id := sms.id
			return must(w.db.GetDeliveryRepository().FindOne(system, &orm.DeliveryFilter{ProviderMessageId: &orm.StringFilter{Eq: &id}}, nil)).State
		}
		if got := state(); got != "delivered" {
			t.Fatalf("the delivery is %s", got)
		}
		// Replies come in through the relay's own callback.
		outcomes := must(w.notifier.Inbound(ctx, "SMS", signed(map[string]any{"messages": []map[string]any{{"id": "in-1", "from": phoneAnn, "text": "ack 1", "at": time.Now()}}})))
		if len(outcomes) != 1 || outcomes[0].Outcome != "done" {
			t.Fatalf("inbound: %+v", outcomes)
		}
		if got := state(); got != "acknowledged" {
			t.Fatalf("the delivery after the reply is %s", got)
		}
		// A ticket another refers to is deleted: its holder records it.
		w.route("History", "reference.cleared", `[{"path":"owner"}]`, "")
		other := must(w.db.GetTicketRepository().CreateOne(system, &types.Ticket{Title: "Parts order", Status: "open", Owner: w.id(w.ann), Audience: "everyone", AgentAccess: "followRole"})).Id
		must(w.db.References().Add(system, rowrefs.AddInput{From: rowrefs.RowRef{Table: "Ticket", Key: w.ticket}, To: rowrefs.RowRef{Table: "Ticket", Key: other.String()}}))
		check(t, w.db.GetTicketRepository().DeleteOne(system, *other))
		cleared := w.db.ReferenceClearedQueue()
		claims := must(cleared.Claim(ctx, 10, 30*time.Second))
		if len(claims) != 1 {
			t.Fatalf("%d cleared messages", len(claims))
		}
		m := claims[0].Message
		check(t, w.db.Transaction(ctx, func(tx orm.TxInterface) error {
			_, err := w.notifier.Cleared(ctx, tx, notify.ClearedReference{HolderTable: m.HolderTable, HolderKey: m.HolderKey, TargetTable: m.TargetTable, TargetKey: m.TargetKey, Role: m.Role})
			return err
		}))
		check(t, cleared.Complete(ctx, claims[0].ID, claims[0].Token))
		w.drain()
		record, ok := find(w.notices(w.ann), func(n notify.Notice) bool { return n.EventKind == "reference.cleared" })
		if !ok || record.Payload["targetTable"] != "Ticket" || record.Payload["role"] != "" || record.Title != "Boiler pressure low" {
			t.Fatalf("the cleared reference's notice: %+v", record)
		}
		// A behavior's event, as its behavior worker's Workers method
		// hands it.
		w.route("Missed", "recurrence.missed", `[{"path":"owner"}]`, "")
		check(t, w.db.Transaction(ctx, func(tx orm.TxInterface) error {
			_, err := w.notifier.FromBehaviorEvent(ctx, tx, "Ticket", "recurrence", notify.BehaviorEvent{Kind: "missed", Key: w.ticket, Payload: map[string]string{"key": "2027-01-01T09:00"}})
			return err
		}))
		w.drain()
		missed, ok := find(w.notices(w.ann), func(n notify.Notice) bool { return n.EventKind == "recurrence.missed" })
		if !ok || fmt.Sprint(missed.Payload) != "map[key:2027-01-01T09:00]" {
			t.Fatalf("the missed occurrence's notice: %+v", missed)
		}
	})

	t.Run("password reset sends a code through the email adapter and the code sets the password", func(t *testing.T) {
		w := newWorld(t, options{})
		sqlDB := stdlib.OpenDBFromPool(w.pool)
		t.Cleanup(func() { _ = sqlDB.Close() })
		store := must(identity.NewSQLStore(sqlDB, identity.Postgres, []byte(types.IdentityDescriptor)))
		memory, iterations, lanes := uint32(64), uint32(1), uint32(1)
		params := identity.Argon2Params{MemoryKiB: memory, Iterations: iterations, Parallelism: lanes}
		service := must(identity.New(store, identity.Config{Password: identity.PasswordConfig{Argon2: identity.Argon2Config{MemoryKiB: &memory, Iterations: &iterations, Parallelism: &lanes}}}))
		must(store.CreateUser(ctx, identity.NewUser{Login: "dana@example.test", PasswordHash: must(identity.HashPassword("first-password-1", params)), At: time.Now()}))
		reset := notify.PasswordReset{Notifier: w.notifier, Identity: service}
		check(t, reset.Request(ctx, "nobody@example.test"))
		if len(w.relays.mailSent()) != 0 {
			t.Fatalf("mail for a login of no one: %+v", w.relays.mailSent())
		}
		check(t, reset.Request(ctx, "Dana@example.test"))
		mail := w.relays.mailSent()
		last := mail[len(mail)-1]
		if last.to != "dana@example.test" || !strings.Contains(last.subject, "password reset") {
			t.Fatalf("the reset mail: %+v", last)
		}
		code := sixDigits.FindStringSubmatch(last.text)[1]
		if ok := must(reset.Complete(ctx, "dana@example.test", "999999", "second-password-2")); ok {
			t.Fatal("a wrong code reset the password")
		}
		if ok := must(reset.Complete(ctx, "dana@example.test", code, "second-password-2")); !ok {
			t.Fatal("the code did not reset the password")
		}
		login := must(store.FindLogin(ctx, "dana@example.test"))
		if ok, _, err := identity.VerifyPassword(login.PasswordHash, "second-password-2", params); err != nil || !ok {
			t.Fatalf("the new password: %v %v", ok, err)
		}
		// The code is spent, and the address it reached is verified.
		if ok := must(reset.Complete(ctx, "dana@example.test", code, "third-password-3")); ok {
			t.Fatal("a spent code reset the password")
		}
		if got := must(w.notifier.ResolveSender(ctx, "email", "dana@example.test")); len(got) != 1 {
			t.Fatalf("the reset address resolves to %+v", got)
		}
		if strings.Contains(fmt.Sprint(w.relays.mailSent()), relayToken) {
			t.Fatal("the relay's token reached a mail")
		}
	})

	t.Run("an approval is asked of the principals who hold its permission, and approve and deny by reply decide it within the channel's trust", func(t *testing.T) {
		w := newWorld(t, options{})
		approver := must(w.db.GetRoleRepository().CreateOne(system, &types.Role{Name: "approver", Permissions: []string{"agents.approve"}}))
		w.grant(w.ben, *approver.Id)
		approval := func(summary string) string {
			return must(w.db.GetApprovalRepository().CreateOne(system, &types.Approval{Operation: "pay_invoice", Summary: summary, Permission: "agents.approve", State: "pending"})).Id.String()
		}
		first, second := approval("Pay invoice 7 for 120"), approval("Pay invoice 8 for 300")
		w.route("Approvals go to approvers", "approval.requested", `[{"role":"approver"}]`, "")
		for _, key := range []string{first, second} {
			must(w.raise(notify.RaiseInput{Table: "Approval", Key: key, Kind: "approval.requested"}))
		}
		w.drain()
		// Ben holds the approval's permission and hears of both; Ann does
		// not.
		if got := to(w.relays.smsSent()); !slices.Equal(got, []string{phoneBen, phoneBen}) {
			t.Fatalf("sms to %v", got)
		}
		if text := w.relays.smsSent()[0].text; !strings.Contains(text, "Pay invoice 7 for 120") {
			t.Fatalf("the sms says %q", text)
		}
		if got := w.notices(w.ann); len(got) != 0 {
			t.Fatalf("ann's notices: %+v", got)
		}
		state := func(key string) *types.Approval {
			return must(w.db.GetApprovalRepository().GetOne(system, w.id(key), nil))
		}
		// SMS is low: it may not approve.
		if low := w.reply("sms", phoneBen, "approve 1"); low.Outcome != "refused" || !strings.Contains(low.Detail, "its trust is low") {
			t.Fatalf("approve by sms: %+v", low)
		}
		if got := state(first).State; got != "pending" {
			t.Fatalf("the approval is %s", got)
		}
		approved := w.reply("email", emailBen, "approve 1")
		if approved.Outcome != "done" || !strings.HasPrefix(approved.Echo, "Approved 1: Pay invoice 7 for 120.") {
			t.Fatalf("approve by email: %+v", approved)
		}
		if decided := state(first); decided.State != "approved" || decided.DecidedBy != w.ben || decided.DecidedVia != "email" || decided.DecidedAt == nil {
			t.Fatalf("the approval: %+v", decided)
		}
		if denied := w.reply("email", emailBen, "deny 2"); denied.Outcome != "done" {
			t.Fatalf("deny: %+v", denied)
		}
		if got := state(second).State; got != "denied" {
			t.Fatalf("the second approval is %s", got)
		}
		// A decided approval is decided once.
		again := w.reply("email", emailBen, "approve 2")
		if again.Outcome != "failed" || !strings.Contains(again.Detail, "the approval is denied") {
			t.Fatalf("approve a denied one: %+v", again)
		}
		if got := state(second).State; got != "denied" {
			t.Fatalf("the second approval is %s", got)
		}
	})
}
