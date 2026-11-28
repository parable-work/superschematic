// fixture-notifications-db's notifier on a SQLite file in the Go
// repositories and the HTTP runtime's notify package (#618, as D189), run
// by TestGoNotifierOnSQLite: the ORM's SQLite database's Notifications,
// over the SQLite DDL the build writes, with an SMS relay and a mail relay
// as stand-in channel adapters (D159). Each case runs on SQLite and, with
// the Postgres NOTIFY_TEST_DATABASE_URL names, on Postgres too, over the
// ORM's Postgres database's Notifications, and the two answer alike. drain
// runs what the dispatch queue's worker runs, over the queue's claims on
// each database: each due message, handed to Notifier.Handle.
package orm_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/notify"
	"github.com/parable-work/superschematic/runtime/http/go/providers"
	"github.com/parable-work/superschematic/runtime/http/go/worker"
	"github.com/parable-work/superschematic/runtime/schema/go/notifications"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
	_ "modernc.org/sqlite"

	orm "example.com/schemas/orm/fixture-notifications-db"
	types "example.com/schemas/types/go/fixture-notifications-db"
)

const relayToken = "relay-token"

const (
	phoneAnn, phoneBen, phoneCal = "+15550100001", "+15550100002", "+15550100003"
	emailAnn, emailBen, emailCal = "ann@example.test", "ben@example.test", "cal@example.test"
)

var system = orm.AsSystem(context.Background())

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

// relays are the stand-in SMS relay and mail relay and what each was sent.
type relays struct {
	mu   sync.Mutex
	sms  []sent
	mail []sent
	n    int
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

type smsRelay struct{ r *relays }

func (a smsRelay) Kind() string { return "sms" }

func (a smsRelay) Send(_ context.Context, m providers.OutboundMessage) (providers.SendResult, error) {
	a.r.mu.Lock()
	defer a.r.mu.Unlock()
	a.r.n++
	id := fmt.Sprintf("sms-%d", a.r.n)
	a.r.sms = append(a.r.sms, sent{to: m.To, text: m.Body, id: id})
	return providers.SendResult{ProviderMessageID: id, State: providers.DeliveryQueued}, nil
}

func (a smsRelay) Health(context.Context) (providers.ChannelHealth, error) {
	return providers.ChannelHealth{OK: true}, nil
}

func checked(request providers.WebhookRequest, into any) error {
	if request.Headers["x-relay-token"] != relayToken {
		return errors.New("the callback holds no relay token")
	}
	return json.Unmarshal([]byte(request.Body), into)
}

func (a smsRelay) Status(_ context.Context, request providers.WebhookRequest) ([]providers.DeliveryStatus, error) {
	return nil, checked(request, &struct{}{})
}

func (a smsRelay) Inbound(_ context.Context, request providers.WebhookRequest) ([]providers.InboundMessage, error) {
	return nil, checked(request, &struct{}{})
}

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

// database is the ORM's Postgres database or its SQLite one, and its
// notifier's host.
type database interface {
	orm.DatabaseInterface
	Notifications() notifications.Host
}

// dialect is one of the two databases a case runs on: the database, and
// its statements a case writes by hand, each in Postgres's words, which
// exec rewrites for SQLite.
type dialect struct {
	name string
	db   database
	exec func(ctx context.Context, sql string, args ...any) error
}

// sqliteWords rewrites a statement in Postgres's words into SQLite's:
// $n as ?n, now() as the clock its schema's defaults write, an array
// literal as JSON text, and no casts to jsonb.
func sqliteWords(text string) string {
	text = strings.ReplaceAll(text, "now()", "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')")
	text = strings.ReplaceAll(text, "::jsonb", "")
	text = strings.ReplaceAll(text, "'{sms,email}'", `'["sms","email"]'`)
	text = strings.ReplaceAll(text, "'{}'", `'[]'`)
	for i := 9; i >= 1; i-- {
		text = strings.ReplaceAll(text, fmt.Sprintf("$%d", i), fmt.Sprintf("?%d", i))
	}
	return text
}

func openSQLite(t *testing.T) dialect {
	t.Helper()
	ctx := context.Background()
	ddl := must(os.ReadFile("testdata/sqlite.sql"))
	raw := must(sql.Open("sqlite", orm.SQLiteDSN(filepath.Join(t.TempDir(), "notify.sqlite"))))
	if _, err := raw.ExecContext(ctx, string(ddl)); err != nil {
		t.Fatalf("apply the SQLite DDL: %v", err)
	}
	db := must(orm.OpenSQLite(ctx, raw))
	t.Cleanup(db.Close)
	return dialect{name: "sqlite", db: db, exec: func(ctx context.Context, text string, args ...any) error {
		_, err := raw.ExecContext(ctx, sqliteWords(text), args...)
		return err
	}}
}

func openPostgres(t *testing.T) dialect {
	t.Helper()
	target := os.Getenv("NOTIFY_TEST_DATABASE_URL")
	ctx := context.Background()
	admin := must(pgxpool.New(ctx, target))
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("notify_sqlite_%d", time.Now().UnixNano())
	must(admin.Exec(ctx, "CREATE SCHEMA "+schema))
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg := must(pgxpool.ParseConfig(target))
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool := must(pgxpool.NewWithConfig(ctx, cfg))
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, string(must(os.ReadFile("testdata/create.sql")))); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	db := must(orm.ConnectWithPool(pool))
	return dialect{name: "postgres", db: db, exec: func(ctx context.Context, text string, args ...any) error {
		_, err := pool.Exec(ctx, text, args...)
		return err
	}}
}

// world is fixture-notifications-db's tables on one database, the
// notifier over it, and the people and the ticket of every case.
type world struct {
	t             *testing.T
	d             dialect
	db            database
	notifier      *notify.Notifier
	relays        *relays
	ann, ben, cal string
	ticket        string
}

func newWorld(t *testing.T, open func(*testing.T) dialect) *world {
	t.Helper()
	d := open(t)
	db := d.db
	r := &relays{}
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
	replies := map[string]map[string]notify.ReplyAction{"ticket.overdue": {"done": markDone}}
	n := must(notify.New(db.Notifications(), notify.Options{Providers: p, Replies: replies}))
	w := &world{t: t, d: d, db: db, notifier: n, relays: r}

	account := func(email string) string {
		return must(db.GetAccountRepository().CreateOne(system, &types.Account{Email: types.ContactEmail(email)})).Id.String()
	}
	w.ann, w.ben, w.cal = account(emailAnn), account(emailBen), account(emailCal)
	worker := must(db.GetRoleRepository().CreateOne(system, &types.Role{Name: "worker", Permissions: []string{"tickets.work"}}))
	for _, user := range []string{w.ann, w.ben} {
		must(db.GetUserRoleGrantRepository().CreateOne(system, &types.UserRoleGrant{User: types.Account{Id: ptr(w.id(user))}, Role: types.Role{Id: worker.Id}, GrantedAt: types.TemporalDateTime(time.Now())}))
	}
	// Ann owns the ticket; Ben is named on it; Cal watches it and may not
	// read it.
	ticket := must(db.GetTicketRepository().CreateOne(system, &types.Ticket{Title: "Boiler pressure low", Status: "open", Owner: w.id(w.ann), Watchers: []types.IdentityUUID{w.id(w.ben), w.id(w.cal)}, Audience: "named", AudienceNamed: []types.IdentityUUID{w.id(w.ben)}, AgentAccess: "followRole"}))
	w.ticket = ticket.Id.String()
	for _, who := range [][3]string{{w.ann, phoneAnn, emailAnn}, {w.ben, phoneBen, emailBen}, {w.cal, phoneCal, emailCal}} {
		w.exec(`INSERT INTO channel_identity (kind, address, principal, trust, verified_at) VALUES ('phone', $1, $2, 'verified', now()), ('email', $3, $2, 'verified', now())`, who[1], rowrefs.UUIDToDB(who[0]), who[2])
	}
	// Everyone's preference (SMS, then email) and each channel kind's trust
	// are the schema's seed rows (#625), which create.sql wrote.
	w.exec(`INSERT INTO notice_route (name, event_kind, recipients, escalation) VALUES ($1, $2, $3::jsonb, $4::jsonb)`, "Overdue tickets page their people", "ticket.overdue", `[{"path":"owner"},{"path":"watchers"}]`, `[{"afterSeconds":600,"channels":["email"]}]`)
	return w
}

func ptr[T any](v T) *T { return &v }

func (w *world) id(text string) types.IdentityUUID { return must(types.ParseIdentityUUID(text)) }

func (w *world) exec(text string, args ...any) {
	w.t.Helper()
	if err := w.d.exec(context.Background(), text, args...); err != nil {
		w.t.Fatalf("%s on %s: %v", text, w.d.name, err)
	}
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

// deliveries are user's deliveries, each its channel, state and attempt,
// in the order they were queued.
func (w *world) deliveries(user string) [][3]string {
	id := w.id(user)
	rows, _, err := w.db.GetDeliveryRepository().FindMany(system, &orm.DeliveryFilter{Recipient: &orm.UUIDFilter{Eq: &id}}, &orm.DeliveryFindOptions{Fields: orm.DeliveryFields{}.All(), OrderBy: []orm.DeliveryOrderBy{{Field: orm.DeliveryOrderByQueuedAt}, {Field: orm.DeliveryOrderByAttempt}}, Limit: 100})
	check(w.t, err)
	var out [][3]string
	for _, d := range rows {
		out = append(out, [3]string{d.ChannelKind, d.State, fmt.Sprint(d.Attempt)})
	}
	return out
}

func (w *world) ticketStatus() string {
	return must(w.db.GetTicketRepository().GetOne(system, w.id(w.ticket), nil)).Status
}

// notifierCases are the cases on one database, each case's answers by its
// name, which TestGoNotifierOnSQLite holds equal across the two.
func notifierCases(t *testing.T, open func(*testing.T) dialect) map[string]any {
	answers := map[string]any{}

	t.Run("an urgent event reaches two recipients, skips one who cannot read the record, and escalates to a second channel when not acknowledged", func(t *testing.T) {
		w := newWorld(t, open)
		event := w.overdue()
		w.drain()
		sms := to(w.relays.smsSent())
		if !slices.Equal(sms, []string{phoneAnn, phoneBen}) {
			t.Fatalf("sms to %v", sms)
		}
		first := w.relays.smsSent()[0].text
		if !strings.Contains(first, "Boiler pressure low") || !strings.Contains(first, "ACK 1") {
			t.Fatalf("the sms says %q", first)
		}
		ann := w.notices(w.ann)
		if len(ann) != 1 || ann[0].EventID != event || ann[0].Urgency != "urgent" || fmt.Sprint(ann[0].Payload) != "map[title:Boiler pressure low]" {
			t.Fatalf("ann's notices: %+v", ann)
		}
		// Cal cannot read the ticket: no notice, no delivery.
		if got := w.notices(w.cal); len(got) != 0 {
			t.Fatalf("cal's notices: %+v", got)
		}
		if got := w.deliveries(w.cal); len(got) != 0 {
			t.Fatalf("cal's deliveries: %v", got)
		}
		if got := w.relays.mailSent(); len(got) != 0 {
			t.Fatalf("mail before the escalation: %+v", got)
		}
		// Ten minutes on, nobody has acknowledged: email follows.
		w.advance("escalate")
		w.drain()
		mail := to(w.relays.mailSent())
		if !slices.Equal(mail, []string{emailAnn, emailBen}) {
			t.Fatalf("mail to %v", mail)
		}
		deliveries := w.deliveries(w.ann)
		if want := [][3]string{{"sms", "sent", "1"}, {"email", "sent", "2"}}; !slices.Equal(deliveries, want) {
			t.Fatalf("ann's deliveries: %v, want %v", deliveries, want)
		}
		answers["urgent"] = map[string]any{"sms": sms, "mail": mail, "ann": deliveries, "ben": w.deliveries(w.ben), "cal": w.deliveries(w.cal), "notice": []any{ann[0].Urgency, ann[0].State, ann[0].Reference, ann[0].Title}}
	})

	t.Run("a reply of ack acknowledges the notice and stops its escalation, and a reply of done changes the record as its sender", func(t *testing.T) {
		w := newWorld(t, open)
		w.overdue()
		w.drain()
		acked := w.reply("sms", phoneAnn, "ack")
		if acked.Outcome != "done" || !strings.Contains(acked.Echo, "UNDO 1") {
			t.Fatalf("ack: %+v", acked)
		}
		notice := w.notices(w.ann)[0]
		if notice.State != "acknowledged" || notice.AcknowledgedVia != "sms" {
			t.Fatalf("the notice: %+v", notice)
		}
		w.advance("escalate")
		w.drain()
		// Ann acknowledged: only Ben's email went out.
		mail := to(w.relays.mailSent())
		if !slices.Equal(mail, []string{emailBen}) {
			t.Fatalf("mail to %v", mail)
		}
		// Ben's reply of done marks the ticket done, as Ben.
		done := w.reply("email", emailBen, "done 1")
		if done.Outcome != "done" || !strings.Contains(done.Echo, "Marked done 1") {
			t.Fatalf("done: %+v", done)
		}
		if status := w.ticketStatus(); status != "done" {
			t.Fatalf("the ticket is %s", status)
		}
		verb := "done"
		audit := must(w.db.GetReplyRepository().FindOne(system, &orm.ReplyFilter{Verb: &orm.StringFilter{Eq: &verb}}, nil))
		if audit.ChannelKind != "email" || audit.Interpreter != "keyword" || audit.Outcome != "done" || audit.Principal == nil || audit.Principal.String() != w.ben {
			t.Fatalf("the audit: %+v", audit)
		}
		if undone := w.reply("email", emailBen, "undo 1"); undone.Outcome != "done" {
			t.Fatalf("undo: %+v", undone)
		}
		answers["replies"] = map[string]any{"ack": []any{acked.Outcome, notice.State, notice.AcknowledgedVia}, "mail": mail, "done": []any{done.Outcome, done.Echo}, "audit": []any{audit.ChannelKind, audit.Interpreter, audit.Outcome, audit.Body}, "status": w.ticketStatus()}
	})

	t.Run("the dispatch queue's worker claims its messages over the database and drains them", func(t *testing.T) {
		w := newWorld(t, open)
		w.overdue()
		// The runtime's worker, in drain mode, over the queue the notifier
		// claims on the database: on SQLite a queue has no slots and no
		// next due time (D145), so it takes its slot and stops once idle;
		// on Postgres it looks no further ahead than SQLite does.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		check(t, worker.Run(ctx, w.notifier.WorkerQueue(), worker.Options{Concurrency: 2, Idle: 10 * time.Millisecond, Drain: &worker.Drain{Idle: 300 * time.Millisecond, Horizon: time.Millisecond}}))
		sms := to(w.relays.smsSent())
		if !slices.Equal(sms, []string{phoneAnn, phoneBen}) {
			t.Fatalf("sms to %v", sms)
		}
		// The escalation waits ten minutes: the worker left it ready.
		if left := w.drain(); left != 0 {
			t.Fatalf("%d messages were due after the worker drained", left)
		}
		answers["worker"] = map[string]any{"sms": sms, "ann": w.deliveries(w.ann), "ben": w.deliveries(w.ben)}
	})

	return answers
}

// TestNotifierOnSQLite runs the cases on SQLite and, with a Postgres, on
// Postgres, and holds each case's answers on the two equal.
func TestNotifierOnSQLite(t *testing.T) {
	var answers [2]map[string]any
	t.Run("sqlite", func(t *testing.T) { answers[0] = notifierCases(t, openSQLite) })
	if os.Getenv("NOTIFY_TEST_DATABASE_URL") == "" {
		t.Log("set NOTIFY_TEST_DATABASE_URL to run the same cases on Postgres")
		return
	}
	t.Run("postgres", func(t *testing.T) { answers[1] = notifierCases(t, openPostgres) })
	compareAnswers(t, answers)
}

// compareAnswers holds the answers of every case on SQLite equal to those
// on Postgres.
func compareAnswers(t *testing.T, answers [2]map[string]any) {
	t.Helper()
	names := make([]string, 0, len(answers[0]))
	for name := range answers[0] {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 || len(answers[1]) != len(names) {
		t.Fatalf("the cases answered %d times on SQLite and %d on Postgres", len(names), len(answers[1]))
	}
	for _, name := range names {
		sqlite, _ := json.MarshalIndent(answers[0][name], "", " ")
		postgres, _ := json.MarshalIndent(answers[1][name], "", " ")
		if string(sqlite) != string(postgres) {
			t.Errorf("%s: SQLite and Postgres differ\nsqlite:   %s\npostgres: %s", name, sqlite, postgres)
		}
	}
	if !t.Failed() {
		t.Logf("SQLite and Postgres answered %d cases alike", len(names))
	}
}
