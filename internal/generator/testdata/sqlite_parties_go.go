// fixture-parties-db's inbound path on a SQLite file in the Go
// repositories and the HTTP runtime's inbound package (#618, as D189), run
// by TestGoPartiesOnSQLite: the ORM's SQLite database's Orchestration,
// over the SQLite DDL the build writes, with an SMS relay and a mail relay
// as stand-in channel adapters (D159) and a bucket in memory. Each case
// runs on SQLite and, with the Postgres PARTIES_TEST_DATABASE_URL names,
// on Postgres too, and the two answer alike. drainRuns runs what the
// @runRequests queue's worker runs, over the queue's claims on each
// database, through a stub handler that writes a report on the granted
// job as the party, as testdata/parties_go.go does on Postgres.
package orm_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/inbound"
	"github.com/parable-work/superschematic/runtime/http/go/notify"
	"github.com/parable-work/superschematic/runtime/http/go/providers"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/orchestration"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
	_ "modernc.org/sqlite"

	orm "example.com/schemas/orm/fixture-parties-db"
	types "example.com/schemas/types/go/fixture-parties-db"
)

const (
	relayToken = "relay-token"
	inbox      = "inbox@example.test"
)

var (
	photo    = []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4, 5, 6, 7, 8}
	photoSHA = func() string { sum := sha256.Sum256(photo); return hex.EncodeToString(sum[:]) }()
	phoneDee = "+15550100011"
	phoneAnn = "+15550100001"
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

// sentMessage is what a stand-in relay was sent.
type sentMessage struct {
	to, subject, text, id, replyTo, messageID string
}

// relays are the stand-in SMS relay and mail relay: what each was sent.
type relays struct {
	mu   sync.Mutex
	sms  []sentMessage
	mail []sentMessage
	n    int
}

func (r *relays) smsSent() []sentMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sms)
}

func (r *relays) mailSent() []sentMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.mail)
}

func checked(request providers.WebhookRequest, into any) error {
	if request.Headers["x-relay-token"] != relayToken {
		return errors.New("the callback holds no relay token")
	}
	return json.Unmarshal([]byte(request.Body), into)
}

// smsRelay sends, and reads its inbound callbacks: a text, the media it
// came with (fetched from the relay, the photo), and STOP and START.
type smsRelay struct{ r *relays }

func (a smsRelay) Kind() string { return "sms" }

func (a smsRelay) Send(_ context.Context, m providers.OutboundMessage) (providers.SendResult, error) {
	a.r.mu.Lock()
	defer a.r.mu.Unlock()
	a.r.n++
	id := fmt.Sprintf("sms-%d", a.r.n)
	a.r.sms = append(a.r.sms, sentMessage{to: m.To, text: m.Body, id: id})
	return providers.SendResult{ProviderMessageID: id, State: providers.DeliveryQueued}, nil
}

func (a smsRelay) Health(context.Context) (providers.ChannelHealth, error) {
	return providers.ChannelHealth{OK: true}, nil
}

func (a smsRelay) Inbound(_ context.Context, request providers.WebhookRequest) ([]providers.InboundMessage, error) {
	var body struct {
		Messages []struct {
			ID, From, Text string
			At             time.Time
			Media          []struct{ URL, Type, Name string }
		}
	}
	if err := checked(request, &body); err != nil {
		return nil, err
	}
	var out []providers.InboundMessage
	for _, m := range body.Messages {
		message := providers.InboundMessage{From: m.From, Body: m.Text, ReceivedAt: m.At, ProviderMessageID: m.ID}
		switch strings.ToUpper(strings.TrimSpace(m.Text)) {
		case "STOP", "STOPALL", "UNSUBSCRIBE", "CANCEL", "END", "QUIT":
			message.OptOut = providers.OptOut
		case "START", "UNSTOP":
			message.OptOut = providers.OptIn
		}
		for i, media := range m.Media {
			if media.URL != "https://sms.test/media/1" {
				return nil, fmt.Errorf("no media at %s", media.URL)
			}
			name := media.Name
			if name == "" {
				name = fmt.Sprintf("media-%d.jpg", i+1)
			}
			message.Attachments = append(message.Attachments, providers.InboundAttachment{Name: name, MediaType: media.Type, Bytes: slices.Clone(photo)})
		}
		out = append(out, message)
	}
	return out, nil
}

// mailRelay sends, threading by the headers it is given, and reads its
// inbound mail: its authentication, threading and attachments.
type mailRelay struct{ r *relays }

func (a mailRelay) Kind() string { return "email" }

func (a mailRelay) Send(_ context.Context, m providers.OutboundMessage) (providers.SendResult, error) {
	a.r.mu.Lock()
	defer a.r.mu.Unlock()
	a.r.n++
	id := fmt.Sprintf("mail-%d", a.r.n)
	a.r.mail = append(a.r.mail, sentMessage{to: m.To, subject: m.Subject, text: m.Body, id: id, replyTo: m.ReplyTo, messageID: m.MessageID})
	return providers.SendResult{ProviderMessageID: id, State: providers.DeliverySent}, nil
}

func (a mailRelay) Health(context.Context) (providers.ChannelHealth, error) {
	return providers.ChannelHealth{OK: true}, nil
}

func (a mailRelay) Inbound(_ context.Context, request providers.WebhookRequest) ([]providers.InboundMessage, error) {
	var body struct {
		Mail []struct {
			ID, From, Subject, Text, MessageID, InReplyTo string
			To, References                                []string
			At                                            time.Time
			Auth                                          *providers.InboundAuthentication
			Attachments                                   []struct{ Name, Type, Content string }
		}
	}
	if err := checked(request, &body); err != nil {
		return nil, err
	}
	var out []providers.InboundMessage
	for _, m := range body.Mail {
		message := providers.InboundMessage{From: m.From, To: m.To, Subject: m.Subject, Body: m.Text, ReceivedAt: m.At, ProviderMessageID: m.ID, MessageID: m.MessageID, InReplyTo: m.InReplyTo, References: m.References, Authentication: m.Auth}
		for _, file := range m.Attachments {
			data, err := base64.StdEncoding.DecodeString(file.Content)
			if err != nil {
				return nil, err
			}
			message.Attachments = append(message.Attachments, providers.InboundAttachment{Name: file.Name, MediaType: file.Type, Bytes: data})
		}
		out = append(out, message)
	}
	return out, nil
}

// memoryBucket is a bucket over a map: the objects a test reads, and a
// signed URL that names the object.
type memoryBucket struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (b *memoryBucket) SignedURL(_ context.Context, name, method string, _ time.Duration, _ string) (string, error) {
	return "https://files.test/" + name + "?method=" + method, nil
}

func (b *memoryBucket) Open(_ context.Context, name string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.objects[name]
	if !ok {
		return nil, rowrefs.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *memoryBucket) Delete(_ context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.objects, name)
	return nil
}

func (b *memoryBucket) PutObject(_ context.Context, name, _ string, body []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[name] = slices.Clone(body)
	return nil
}

func (b *memoryBucket) size() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.objects)
}

// database is the ORM's Postgres database or its SQLite one, and its
// orchestration's host.
type database interface {
	orm.DatabaseInterface
	Orchestration() orchestration.Host
}

// dialect is one of the two databases a case runs on: the database, and
// its statements a case writes by hand, each in Postgres's words, which
// exec rewrites for SQLite.
type dialect struct {
	name string
	db   database
	exec func(ctx context.Context, sql string, args ...any) error
}

// sqliteWords rewrites a statement in Postgres's words into SQLite's: $n
// as ?n, now() as the clock its schema's defaults write, and a time as the
// text SQLite holds.
func sqliteWords(text string, args []any) (string, []any) {
	text = strings.ReplaceAll(text, "now()", "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')")
	for i := 9; i >= 1; i-- {
		text = strings.ReplaceAll(text, fmt.Sprintf("$%d", i), fmt.Sprintf("?%d", i))
	}
	out := make([]any, len(args))
	for i, arg := range args {
		if at, ok := arg.(time.Time); ok {
			out[i] = at.UTC().Format("2006-01-02T15:04:05.000Z")
			continue
		}
		out[i] = arg
	}
	return text, out
}

func openSQLite(t *testing.T) dialect {
	t.Helper()
	ctx := context.Background()
	raw := must(sql.Open("sqlite", orm.SQLiteDSN(filepath.Join(t.TempDir(), "parties.sqlite"))))
	if _, err := raw.ExecContext(ctx, string(must(os.ReadFile("testdata/sqlite.sql")))); err != nil {
		t.Fatalf("apply the SQLite DDL: %v", err)
	}
	db := must(orm.OpenSQLite(ctx, raw))
	t.Cleanup(db.Close)
	return dialect{name: "sqlite", db: db, exec: func(ctx context.Context, text string, args ...any) error {
		text, args = sqliteWords(text, args)
		_, err := raw.ExecContext(ctx, text, args...)
		return err
	}}
}

func openPostgres(t *testing.T) dialect {
	t.Helper()
	target := os.Getenv("PARTIES_TEST_DATABASE_URL")
	ctx := context.Background()
	admin := must(pgxpool.New(ctx, target))
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("parties_sqlite_%d", time.Now().UnixNano())
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

// world is fixture-parties-db's tables on one database, the notifier and
// the inbound path over it, a contact with a grant on a job, and someone
// of the staff.
type world struct {
	t        *testing.T
	d        dialect
	db       database
	host     orchestration.Host
	notifier *notify.Notifier
	inbound  *inbound.Inbound
	relays   *relays
	files    *memoryBucket
	dee, job string
	grant    string
	ann      string
	staff    orchestration.Principal
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
	host := db.Orchestration()
	n := must(notify.New(host, notify.Options{Providers: p}))
	files := &memoryBucket{objects: map[string][]byte{}}
	options := inbound.Options{Notifier: n, Providers: p, Bucket: files, InboundAddress: inbox, RateLimit: inbound.RateLimit{Messages: 3, Seconds: 3600}}
	in := must(inbound.New(host, options))
	w := &world{t: t, d: d, db: db, host: host, notifier: n, inbound: in, relays: r, files: files}

	must(db.GetRoleRepository().CreateOne(system, &types.Role{Name: "contractor", Permissions: []string{"reports.write"}, Reaches: []string{"Report:job"}}))
	w.ann = must(db.GetAccountRepository().CreateOne(system, &types.Account{Email: "ann@example.test"})).Id.String()
	w.dee = w.contact("Dee")
	w.job = must(db.GetJobRepository().CreateOne(system, &types.Job{Title: "Fix the gate", Status: "open"})).Id.String()
	w.identity("phone", phoneDee, w.dee, "verified")
	w.identity("email", "dee@example.test", w.dee, "verified")
	w.exec(`INSERT INTO channel_identity (kind, address, principal_kind, principal, trust, verified_at) VALUES ('phone', $1, 'user', $2, 'verified', now())`, phoneAnn, rowrefs.UUIDToDB(w.ann))
	w.grant = w.issue(w.dee, w.job)
	w.staff = orchestration.Principal{Principal: access.Principal{Subject: w.ann, Kind: access.KindUser}, Permissions: []string{"parties.message", "parties.triage"}}
	return w
}

func (w *world) contact(name string) string {
	return must(w.db.GetContactRepository().CreateOne(system, &types.Contact{Name: name})).Id.String()
}

func (w *world) identity(kind, address, party, trust string) {
	w.exec(`INSERT INTO channel_identity (kind, address, principal_kind, principal_table, principal, trust, verified_at) VALUES ($1, $2, 'party', 'Contact', $3, $4, $5)`, kind, address, rowrefs.UUIDToDB(party), trust, time.Now())
}

func (w *world) issue(holder, job string) string {
	grant, _ := must2(w.host.IssueGrant(system, access.IssueGrantInput{Holder: holder, HolderKind: "party", Role: "contractor", TargetType: "Job", TargetID: job}))
	return grant.ID
}

func must2[A, B any](a A, b B, err error) (A, B) {
	if err != nil {
		panic(err)
	}
	return a, b
}

func (w *world) exec(sql string, args ...any) {
	w.t.Helper()
	if err := w.d.exec(context.Background(), sql, args...); err != nil {
		w.t.Fatalf("%s on %s: %v", sql, w.d.name, err)
	}
}

func (w *world) id(text string) types.IdentityUUID { return must(types.ParseIdentityUUID(text)) }

func ptr[T any](v T) *T { return &v }

// fileOnGrantedRecord is the stub handler of a run request: as the party,
// through the grant the request names, a report on the granted job, the
// message filed onto both.
func (w *world) fileOnGrantedRecord(ctx context.Context, request inbound.RunRequest) error {
	party, err := w.inbound.PrincipalOf(ctx, request.Party, request.Grant)
	if err != nil {
		return err
	}
	// The grant ended since the message came: the run reaches nothing.
	if len(party.Grants) == 0 {
		return nil
	}
	grant := party.Grants[0]
	message, err := w.inbound.Message(ctx, request.Message)
	if err != nil || message == nil {
		return fmt.Errorf("the run's message %s: %v", request.Message, err)
	}
	as := orm.WithPrincipal(ctx, party.Principal)
	report, err := w.db.GetReportRepository().CreateOne(as, &types.Report{Job: types.Job{Id: ptr(w.id(grant.TargetID))}, Body: message.Str("body"), CreatedAt: types.TemporalDateTime(time.Now())})
	if err != nil {
		return err
	}
	if err := w.inbound.File(ctx, inbound.FileInput{Message: request.Message, To: rowrefs.RowRef{Table: "Report", Key: report.Id.String()}, As: party}); err != nil {
		return err
	}
	return w.inbound.File(ctx, inbound.FileInput{Message: request.Message, To: rowrefs.RowRef{Table: grant.TargetType, Key: grant.TargetID}, As: party})
}

// drainRuns runs every run request that is waiting through the stub
// handler, and answers how many.
func (w *world) drainRuns() int {
	w.t.Helper()
	ctx := context.Background()
	queue := must(w.inbound.RunQueue(w.fileOnGrantedRecord))
	handled := 0
	for round := 0; round < 20; round++ {
		claims := must(queue.Claim(ctx, 10, 30*time.Second))
		if len(claims) == 0 {
			return handled
		}
		for _, claim := range claims {
			if err := claim.Handle(ctx); err != nil {
				w.t.Fatalf("a run request: %v", err)
			}
			check(w.t, queue.Complete(ctx, claim.ID, claim.Token))
			handled++
		}
	}
	w.t.Fatal("the run requests did not drain")
	return handled
}

// pendingRuns are the run requests waiting, read and put back.
func (w *world) pendingRuns() []inbound.RunRequest {
	ctx := context.Background()
	queue := w.host.QueueOf(orchestration.RunRequests)
	claims := must(queue.Claim(ctx, 50, 30*time.Second))
	out := []inbound.RunRequest{}
	for _, claim := range claims {
		check(w.t, queue.Release(ctx, claim.ID, claim.Token))
		var request inbound.RunRequest
		check(w.t, json.Unmarshal(claim.Message, &request))
		out = append(out, request)
	}
	return out
}

func signed(body any) providers.WebhookRequest {
	return providers.WebhookRequest{Headers: map[string]string{"x-relay-token": relayToken}, Body: string(must(json.Marshal(body)))}
}

func text(from, body string, extra map[string]any) providers.WebhookRequest {
	message := map[string]any{"id": "in-" + uuid.NewString(), "from": from, "text": body, "at": time.Now()}
	for k, v := range extra {
		message[k] = v
	}
	return signed(map[string]any{"messages": []any{message}})
}

func mail(from, body string, extra map[string]any) providers.WebhookRequest {
	message := map[string]any{"id": "mail-in-" + uuid.NewString(), "from": from, "to": []string{inbox}, "subject": "The gate", "text": body, "at": time.Now(), "auth": map[string]string{"spf": "pass", "dkim": "pass", "dmarc": "pass"}}
	for k, v := range extra {
		if v == nil {
			delete(message, k)
			continue
		}
		message[k] = v
	}
	return signed(map[string]any{"mail": []any{message}})
}

func (w *world) receive(slot string, request providers.WebhookRequest) inbound.Outcome {
	w.t.Helper()
	outcomes := must(w.inbound.Inbound(context.Background(), slot, request))
	if len(outcomes) != 1 {
		w.t.Fatalf("%d outcomes of one message", len(outcomes))
	}
	return outcomes[0]
}

func (w *world) reportsOf(job string) []*types.Report {
	id := w.id(job)
	rows, _ := must2(w.db.GetReportRepository().FindMany(system, &orm.ReportFilter{JobID: &orm.UUIDFilter{Eq: &id}}, &orm.ReportFindOptions{Fields: orm.ReportFields{}.All(), Limit: 100}))
	return rows
}

func (w *world) attachments(table, key string) []rowrefs.Attachment {
	return must(w.host.Attachments().List(system, rowrefs.RowRef{Table: table, Key: key}))
}

func (w *world) backlinks(table, key string) []string {
	links := must(w.host.References().ReferencedBy(system, rowrefs.RowRef{Table: table, Key: key}, rowrefs.ReferencedByOptions{}))
	var out []string
	for _, b := range links.Items {
		if b.Table == "Message" {
			out = append(out, b.Key)
		}
	}
	return out
}

func (w *world) message(id string) orchestration.Row {
	w.t.Helper()
	row := must(w.inbound.Message(context.Background(), id))
	if row == nil {
		w.t.Fatalf("no message %s", id)
	}
	return row
}

func (w *world) send(input inbound.SendInput) inbound.SendOutcome {
	w.t.Helper()
	return must(w.inbound.Send(context.Background(), input))
}

func ids(rows []orchestration.Row) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.Str("id"))
	}
	return out
}

// partiesCases are the cases on one database, each case's answers by its
// name, which TestPartiesOnSQLite holds equal across the two.
func partiesCases(t *testing.T, open func(*testing.T) dialect) map[string]any {
	ctx := context.Background()
	answers := map[string]any{}

	t.Run("a text with a photo from a party with a grant becomes a row on the granted record, with the photo attached", func(t *testing.T) {
		w := newWorld(t, open)
		outcome := w.receive("SMS", text("+1 (555) 010-0011", "Gate fixed, photo attached", map[string]any{"media": []any{map[string]string{"url": "https://sms.test/media/1", "type": "image/jpeg", "name": "gate.jpg"}}}))
		if outcome.Outcome != inbound.Run || outcome.Party == nil || *outcome.Party != (orchestration.PartyRef{Table: "Contact", Key: w.dee}) || outcome.Grant != w.grant {
			t.Fatalf("the outcome: %+v", outcome)
		}
		pending := w.pendingRuns()
		if want := (inbound.RunRequest{Trigger: "inbound", Party: orchestration.PartyRef{Table: "Contact", Key: w.dee}, Message: outcome.Message, Grant: w.grant}); len(pending) != 1 || pending[0] != want {
			t.Fatalf("the run requests: %+v, want %+v", pending, want)
		}
		message := w.message(outcome.Message)
		if message.Str("direction") != "inbound" || message.Str("via") != "sms" || message.Str("fromAddress") != phoneDee || message.Str("disposition") != "run" || message.Str("partyTable") != "Contact" || message.Str("trust") != "verified" || !message.IsNull("triage") {
			t.Fatalf("the message: %v", message)
		}
		files := w.attachments("Message", outcome.Message)
		if len(files) != 1 || files[0].Name != "gate.jpg" || files[0].MediaType != "image/jpeg" || files[0].Size != int64(len(photo)) || files[0].ContentHash != photoSHA {
			t.Fatalf("the message's files: %+v", files)
		}
		if n := w.drainRuns(); n != 1 {
			t.Fatalf("%d run requests ran", n)
		}
		reports := w.reportsOf(w.job)
		if len(reports) != 1 || reports[0].Body != "Gate fixed, photo attached" {
			t.Fatalf("the reports: %+v", reports)
		}
		report := reports[0].Id.String()
		filed := w.attachments("Report", report)
		if len(filed) != 1 || filed[0].Name != "gate.jpg" || filed[0].ContentHash != photoSHA || filed[0].ObjectKey != files[0].ObjectKey {
			t.Fatalf("the report's files: %+v", filed)
		}
		for _, row := range [][2]string{{"Job", w.job}, {"Report", report}, {"Contact", w.dee}} {
			if !slices.Contains(w.backlinks(row[0], row[1]), outcome.Message) {
				t.Fatalf("%s %s does not list the message: %v", row[0], row[1], w.backlinks(row[0], row[1]))
			}
		}
		other := must(w.db.GetJobRepository().CreateOne(system, &types.Job{Title: "Paint the fence", Status: "open"})).Id
		party := must(w.inbound.PrincipalOf(ctx, orchestration.PartyRef{Table: "Contact", Key: w.dee}, ""))
		_, refused := w.db.GetReportRepository().CreateOne(orm.WithPrincipal(ctx, party.Principal), &types.Report{Job: types.Job{Id: other}, Body: "not mine", CreatedAt: types.TemporalDateTime(time.Now())})
		if refused == nil {
			t.Fatal("the party wrote a report on a job it holds no grant of")
		}
		answers["granted"] = map[string]any{
			"outcome": outcome.Outcome, "pending": len(pending),
			"message": []string{message.Str("direction"), message.Str("via"), message.Str("disposition"), message.Str("trust"), message.Str("body")},
			"files":   []any{files[0].Name, files[0].MediaType, files[0].Size, files[0].ContentHash},
			"reports": []any{len(reports), reports[0].Body, len(filed), w.files.size()},
			"refused": refused != nil,
		}
	})

	t.Run("a text from an unknown number lands in triage", func(t *testing.T) {
		w := newWorld(t, open)
		outcome := w.receive("SMS", text("+15550109999", "Gate fixed", map[string]any{"media": []any{map[string]string{"url": "https://sms.test/media/1", "type": "image/jpeg"}}}))
		if outcome.Outcome != inbound.Unknown || outcome.Party != nil {
			t.Fatalf("the outcome: %+v", outcome)
		}
		if pending := w.pendingRuns(); len(pending) != 0 {
			t.Fatalf("run requests: %+v", pending)
		}
		message := w.message(outcome.Message)
		if message.Str("disposition") != "unknown" || message.Str("triage") != "open" || message.Str("fromAddress") != "+15550109999" || !message.IsNull("party") {
			t.Fatalf("the message: %v", message)
		}
		files := w.attachments("Message", outcome.Message)
		if len(files) != 1 {
			t.Fatalf("the message's files: %+v", files)
		}
		held := must(w.inbound.Triage(ctx, w.staff, 0))
		if got := ids(held); !slices.Equal(got, []string{outcome.Message}) {
			t.Fatalf("triage: %v", got)
		}
		var refusal *inbound.Error
		if _, err := w.inbound.Triage(ctx, orchestration.Principal{Principal: access.Principal{Subject: w.ann, Kind: access.KindUser}}, 0); !errors.As(err, &refusal) {
			t.Fatalf("triage without the permission: %v", err)
		}
		check(t, w.inbound.Settle(ctx, w.staff, outcome.Message))
		settled := must(w.inbound.Triage(ctx, w.staff, 0))
		if len(settled) != 0 {
			t.Fatalf("triage after settling: %v", ids(settled))
		}
		if reports := w.reportsOf(w.job); len(reports) != 0 {
			t.Fatalf("reports: %+v", reports)
		}
		answers["unknown"] = map[string]any{
			"outcome": outcome.Outcome, "message": []string{message.Str("disposition"), message.Str("triage"), message.Str("fromAddress")},
			"files": len(files), "held": len(held), "settled": len(settled), "refused": strings.ReplaceAll(refusal.Message, w.ann, "ann"),
		}
	})

	t.Run("the same text from a party whose grant has ended reaches nothing", func(t *testing.T) {
		w := newWorld(t, open)
		// A run request made before the grant ended runs as nothing after.
		w.receive("SMS", text(phoneDee, "Gate fixed", nil))
		check(t, w.host.RevokeGrant(system, w.grant, time.Now()))
		ran := w.drainRuns()
		if ran != 1 {
			t.Fatalf("%d run requests ran", ran)
		}
		if reports := w.reportsOf(w.job); len(reports) != 0 {
			t.Fatalf("reports: %+v", reports)
		}
		// The number may be someone else's now: held as no one's.
		outcome := w.receive("SMS", text(phoneDee, "Gate fixed", nil))
		if outcome.Outcome != inbound.Lapsed || outcome.Party != nil {
			t.Fatalf("the outcome: %+v", outcome)
		}
		if pending := w.pendingRuns(); len(pending) != 0 {
			t.Fatalf("run requests: %+v", pending)
		}
		message := w.message(outcome.Message)
		if message.Str("disposition") != "lapsed" || message.Str("triage") != "open" || !message.IsNull("party") {
			t.Fatalf("the message: %v", message)
		}
		dee := orchestration.PartyRef{Table: "Contact", Key: w.dee}
		if slices.Contains(ids(must(w.inbound.Timeline(ctx, w.staff, dee, 0))), outcome.Message) {
			t.Fatal("the lapsed message is on the party's timeline")
		}
		if slices.Contains(w.backlinks("Contact", w.dee), outcome.Message) {
			t.Fatal("the lapsed message is on the party's backlinks")
		}
		sent := w.send(inbound.SendInput{As: w.staff, Party: dee, Channel: "sms", Body: "Are you there?"})
		if !strings.Contains(sent.Detail, "lapsed") {
			t.Fatalf("a send to a lapsed identity: %+v", sent)
		}
		answers["lapsed"] = map[string]any{
			"ran": ran, "reports": len(w.reportsOf(w.job)), "outcome": outcome.Outcome,
			"message": []string{message.Str("disposition"), message.Str("triage")}, "send": []any{sent.Outcome, sent.Detail},
		}
	})

	return answers
}

// TestPartiesOnSQLite runs the cases on SQLite and, with a Postgres, on
// Postgres, and holds each case's answers on the two equal.
func TestPartiesOnSQLite(t *testing.T) {
	var answers [2]map[string]any
	t.Run("sqlite", func(t *testing.T) { answers[0] = partiesCases(t, openSQLite) })
	if os.Getenv("PARTIES_TEST_DATABASE_URL") == "" {
		t.Log("set PARTIES_TEST_DATABASE_URL to run the same cases on Postgres")
		return
	}
	t.Run("postgres", func(t *testing.T) { answers[1] = partiesCases(t, openPostgres) })
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
