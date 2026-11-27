// fixture-parties-db end to end in the Go repositories and the HTTP
// runtime's inbound package (D157, D192), run by TestGoPartiesEndToEnd:
// contacts that are parties, the jobs a grant lets one reach and the
// reports it writes on them, the conversations and their messages, which
// hold files and point at rows, the run requests queue, and the notifier a
// user's reply runs through. The generated create.sql is applied in a
// schema of the test's own on Postgres, and the cases are the TypeScript
// repositories' (tsgen's parties.test.ts) and the Rust ones' (D193),
// through inbound over the ORM's Orchestration, with an SMS relay and a
// mail relay as stand-in channel adapters (D159) and a bucket in memory.
package orm_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
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
	phoneEve = "+15550100012"
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

// world is a schema of its own holding fixture-parties-db's tables, the
// notifier and the inbound path over it, a contact with a grant on a job,
// and someone of the staff.
type world struct {
	t        *testing.T
	pool     *pgxpool.Pool
	db       *orm.Database
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

func newWorld(t *testing.T, approve func(body string) inbound.Decision) *world {
	t.Helper()
	target := os.Getenv("PARTIES_TEST_DATABASE_URL")
	if target == "" {
		t.Skip("set PARTIES_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin := must(pgxpool.New(ctx, target))
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("parties_go_%d", time.Now().UnixNano())
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
	if approve != nil {
		options.Approve = inbound.ApprovalFunc(func(_ context.Context, input inbound.SendInput) (inbound.Decision, error) {
			return approve(input.Body), nil
		})
	}
	in := must(inbound.New(host, options))
	w := &world{t: t, pool: pool, db: db, host: host, notifier: n, inbound: in, relays: r, files: files}

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
	w.exec(`INSERT INTO channel_identity (kind, address, principal_kind, principal_table, principal, trust, verified_at) VALUES ($1, $2, 'party', 'Contact', $3, $4, now())`, kind, address, rowrefs.UUIDToDB(party), trust)
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
	if _, err := w.pool.Exec(context.Background(), sql, args...); err != nil {
		w.t.Fatalf("%s: %v", sql, err)
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

var sixDigits = regexp.MustCompile(`(\d{6})`)

func TestParties(t *testing.T) {
	ctx := context.Background()

	t.Run("a text with a photo from a party with a grant becomes a row on the granted record, with the photo attached", func(t *testing.T) {
		w := newWorld(t, nil)
		outcome := w.receive("SMS", text("+1 (555) 010-0011", "Gate fixed, photo attached", map[string]any{"media": []any{map[string]string{"url": "https://sms.test/media/1", "type": "image/jpeg", "name": "gate.jpg"}}}))
		if outcome.Outcome != inbound.Run || outcome.Party == nil || *outcome.Party != (orchestration.PartyRef{Table: "Contact", Key: w.dee}) || outcome.Grant != w.grant {
			t.Fatalf("the outcome: %+v", outcome)
		}
		// The run request, as the agent dispatcher (D158) takes it.
		pending := w.pendingRuns()
		if want := (inbound.RunRequest{Trigger: "inbound", Party: orchestration.PartyRef{Table: "Contact", Key: w.dee}, Message: outcome.Message, Grant: w.grant}); len(pending) != 1 || pending[0] != want {
			t.Fatalf("the run requests: %+v, want %+v", pending, want)
		}
		message := w.message(outcome.Message)
		if message.Str("direction") != "inbound" || message.Str("via") != "sms" || message.Str("fromAddress") != phoneDee || message.Str("disposition") != "run" || message.Str("partyTable") != "Contact" || message.Str("trust") != "verified" || !message.IsNull("triage") {
			t.Fatalf("the message: %v", message)
		}
		// The photo is the message's, in the bucket once.
		files := w.attachments("Message", outcome.Message)
		if len(files) != 1 || files[0].Name != "gate.jpg" || files[0].MediaType != "image/jpeg" || files[0].Size != int64(len(photo)) || files[0].ContentHash != photoSHA {
			t.Fatalf("the message's files: %+v", files)
		}
		if w.files.size() != 1 {
			t.Fatalf("%d objects", w.files.size())
		}
		if n := w.drainRuns(); n != 1 {
			t.Fatalf("%d run requests ran", n)
		}
		// A report on the granted job, written as the party, with the photo.
		reports := w.reportsOf(w.job)
		if len(reports) != 1 || reports[0].Body != "Gate fixed, photo attached" {
			t.Fatalf("the reports: %+v", reports)
		}
		report := reports[0].Id.String()
		filed := w.attachments("Report", report)
		if len(filed) != 1 || filed[0].Name != "gate.jpg" || filed[0].ContentHash != photoSHA || filed[0].ObjectKey != files[0].ObjectKey {
			t.Fatalf("the report's files: %+v", filed)
		}
		if w.files.size() != 1 {
			t.Fatalf("%d objects after filing", w.files.size())
		}
		// The message is on each row it references: the job, the report and
		// the party.
		for _, row := range [][2]string{{"Job", w.job}, {"Report", report}, {"Contact", w.dee}} {
			if !slices.Contains(w.backlinks(row[0], row[1]), outcome.Message) {
				t.Fatalf("%s %s does not list the message: %v", row[0], row[1], w.backlinks(row[0], row[1]))
			}
		}
		// The party reaches the job's reports through its grant, and no
		// other job's.
		other := must(w.db.GetJobRepository().CreateOne(system, &types.Job{Title: "Paint the fence", Status: "open"})).Id
		party := must(w.inbound.PrincipalOf(ctx, orchestration.PartyRef{Table: "Contact", Key: w.dee}, ""))
		if _, err := w.db.GetReportRepository().CreateOne(orm.WithPrincipal(ctx, party.Principal), &types.Report{Job: types.Job{Id: other}, Body: "not mine", CreatedAt: types.TemporalDateTime(time.Now())}); err == nil {
			t.Fatal("the party wrote a report on a job it holds no grant of")
		}
	})

	t.Run("a text from an unknown number lands in triage", func(t *testing.T) {
		w := newWorld(t, nil)
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
		// Its photo is filed to its conversation, held with it.
		if files := w.attachments("Message", outcome.Message); len(files) != 1 {
			t.Fatalf("the message's files: %+v", files)
		}
		held := must(w.inbound.Triage(ctx, w.staff, 0))
		if got := ids(held); !slices.Equal(got, []string{outcome.Message}) {
			t.Fatalf("triage: %v", got)
		}
		// Reading triage is a permission.
		var refusal *inbound.Error
		if _, err := w.inbound.Triage(ctx, orchestration.Principal{Principal: access.Principal{Subject: w.ann, Kind: access.KindUser}}, 0); !errors.As(err, &refusal) {
			t.Fatalf("triage without the permission: %v", err)
		}
		check(t, w.inbound.Settle(ctx, w.staff, outcome.Message))
		if held := must(w.inbound.Triage(ctx, w.staff, 0)); len(held) != 0 {
			t.Fatalf("triage after settling: %v", ids(held))
		}
		if reports := w.reportsOf(w.job); len(reports) != 0 {
			t.Fatalf("reports: %+v", reports)
		}
	})

	t.Run("the same text from a party whose grant has ended reaches nothing, until its identity is verified again", func(t *testing.T) {
		w := newWorld(t, nil)
		// A run request made before the grant ended runs as nothing after.
		w.receive("SMS", text(phoneDee, "Gate fixed", nil))
		check(t, w.host.RevokeGrant(system, w.grant, time.Now()))
		if n := w.drainRuns(); n != 1 {
			t.Fatalf("%d run requests ran", n)
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
		// Nothing is sent to it either.
		if sent := w.send(inbound.SendInput{As: w.staff, Party: dee, Channel: "sms", Body: "Are you there?"}); !strings.Contains(sent.Detail, "lapsed") {
			t.Fatalf("a send to a lapsed identity: %+v", sent)
		}
		// Verified again by the code sent to it, the party is itself again,
		// with no grant.
		identity := must(w.notifier.ResolveSender(ctx, "phone", phoneDee))[0]
		check(t, w.notifier.StartVerification(ctx, identity.ID, ""))
		sms := w.relays.smsSent()
		code := sixDigits.FindStringSubmatch(sms[len(sms)-1].text)
		if code == nil {
			t.Fatalf("no code in %q", sms[len(sms)-1].text)
		}
		if verified := w.receive("SMS", text(phoneDee, code[1], nil)); verified.Outcome != inbound.Verified {
			t.Fatalf("the code: %+v", verified)
		}
		if again := w.receive("SMS", text(phoneDee, "Gate fixed", nil)); again.Outcome != inbound.Timeline {
			t.Fatalf("after verifying: %+v", again)
		}
		if pending := w.pendingRuns(); len(pending) != 0 {
			t.Fatalf("run requests: %+v", pending)
		}
	})

	t.Run("email failing DMARC is treated as unknown, and an authenticated email threads to its party", func(t *testing.T) {
		w := newWorld(t, nil)
		spoofed := w.receive("MAIL", mail("Dee@Example.test", "Gate fixed", map[string]any{"auth": map[string]string{"spf": "pass", "dkim": "pass", "dmarc": "fail"}}))
		if spoofed.Outcome != inbound.Unknown || !strings.Contains(spoofed.Detail, "dmarc=fail") {
			t.Fatalf("spoofed: %+v", spoofed)
		}
		if pending := w.pendingRuns(); len(pending) != 0 {
			t.Fatalf("run requests: %+v", pending)
		}
		// No authentication reported is none passed.
		if bare := w.receive("MAIL", mail("dee@example.test", "Gate fixed", map[string]any{"auth": nil})); bare.Outcome != inbound.Unknown {
			t.Fatalf("bare: %+v", bare)
		}
		// Mail to another address is not the deployment's.
		if elsewhere := w.receive("MAIL", mail("dee@example.test", "Gate fixed", map[string]any{"to": []string{"other@example.test"}})); elsewhere.Outcome != inbound.Ignored {
			t.Fatalf("elsewhere: %+v", elsewhere)
		}
		// The staff write; the party's reply threads to the same
		// conversation.
		dee := orchestration.PartyRef{Table: "Contact", Key: w.dee}
		sent := w.send(inbound.SendInput{As: w.staff, Party: dee, Channel: "email", Subject: "The gate", Body: "Is the gate fixed?", About: []rowrefs.RowRef{{Table: "Job", Key: w.job}}})
		if sent.Outcome != inbound.Sent {
			t.Fatalf("the send: %+v", sent)
		}
		mails := w.relays.mailSent()
		out := mails[len(mails)-1]
		if out.to != "dee@example.test" || out.replyTo != inbox || !regexp.MustCompile(`^<.+@example\.test>$`).MatchString(out.messageID) {
			t.Fatalf("the mail: %+v", out)
		}
		answer := w.receive("MAIL", mail("dee@example.test", "Yes, photo attached", map[string]any{"messageId": "<r1@mail.example>", "inReplyTo": out.messageID, "references": []string{out.messageID}, "attachments": []any{map[string]string{"name": "gate.jpg", "type": "image/jpeg", "content": base64.StdEncoding.EncodeToString(photo)}}}))
		if answer.Outcome != inbound.Run {
			t.Fatalf("the answer: %+v", answer)
		}
		if outbound := w.message(sent.Message); answer.Conversation != outbound.Str("conversation") {
			t.Fatalf("the answer's conversation %s, the send's %s", answer.Conversation, outbound.Str("conversation"))
		}
		if got := w.message(answer.Message).Str("inReplyTo"); got != out.messageID {
			t.Fatalf("the answer's inReplyTo %q", got)
		}
		if n := w.drainRuns(); n != 1 {
			t.Fatalf("%d run requests ran", n)
		}
		reports := w.reportsOf(w.job)
		if len(reports) != 1 {
			t.Fatalf("reports: %+v", reports)
		}
		if files := w.attachments("Report", reports[0].Id.String()); len(files) != 1 || files[0].ContentHash != photoSHA {
			t.Fatalf("the report's files: %+v", files)
		}
		// The outbound message is on the job it was about.
		if !slices.Contains(w.backlinks("Job", w.job), sent.Message) {
			t.Fatalf("the job's backlinks: %v", w.backlinks("Job", w.job))
		}
	})

	t.Run("a party with no grant is on its timeline and in triage, and a user's text is the notifier's reply", func(t *testing.T) {
		w := newWorld(t, nil)
		eve := w.contact("Eve")
		w.identity("phone", phoneEve, eve, "entered")
		outcome := w.receive("SMS", text(phoneEve, "Can I quote for the fence?", nil))
		party := orchestration.PartyRef{Table: "Contact", Key: eve}
		if outcome.Outcome != inbound.Timeline || outcome.Party == nil || *outcome.Party != party {
			t.Fatalf("the outcome: %+v", outcome)
		}
		message := w.message(outcome.Message)
		if message.Str("disposition") != "timeline" || message.Str("triage") != "open" || message.Str("trust") != "entered" {
			t.Fatalf("the message: %v", message)
		}
		if got := ids(must(w.inbound.Timeline(ctx, w.staff, party, 0))); !slices.Equal(got, []string{outcome.Message}) {
			t.Fatalf("the timeline: %v", got)
		}
		if got := ids(must(w.inbound.Triage(ctx, w.staff, 0))); !slices.Equal(got, []string{outcome.Message}) {
			t.Fatalf("triage: %v", got)
		}
		if got := w.backlinks("Contact", eve); !slices.Equal(got, []string{outcome.Message}) {
			t.Fatalf("eve's backlinks: %v", got)
		}
		if pending := w.pendingRuns(); len(pending) != 0 {
			t.Fatalf("run requests: %+v", pending)
		}
		// A user's text is a reply, which the notifier runs as the user.
		reply := w.receive("SMS", text(phoneAnn, "ack", nil))
		if reply.Outcome != inbound.Reply || reply.Reply == nil || reply.Reply.Principal == nil || reply.Reply.Principal.Kind != "user" || reply.Reply.Principal.ID != w.ann || reply.Message != "" {
			t.Fatalf("the reply: %+v %+v", reply, reply.Reply)
		}
	})

	t.Run("two parties sharing an address are told apart by the thread, and held in triage otherwise", func(t *testing.T) {
		w := newWorld(t, nil)
		fay, gus := w.contact("Fay"), w.contact("Gus")
		fence := must(w.db.GetJobRepository().CreateOne(system, &types.Job{Title: "Paint the fence", Status: "open"})).Id.String()
		for _, party := range []string{fay, gus} {
			w.identity("email", "office@example.test", party, "verified")
		}
		w.issue(fay, w.job)
		gusGrant := w.issue(gus, fence)
		unthreaded := w.receive("MAIL", mail("office@example.test", "Done", nil))
		if unthreaded.Outcome != inbound.Unknown || !strings.Contains(unthreaded.Detail, "shared by 2 parties") {
			t.Fatalf("unthreaded: %+v", unthreaded)
		}
		sent := w.send(inbound.SendInput{As: w.staff, Party: orchestration.PartyRef{Table: "Contact", Key: gus}, Channel: "email", Subject: "The fence", Body: "How is the fence?"})
		mails := w.relays.mailSent()
		threaded := w.receive("MAIL", mail("office@example.test", "Fence painted", map[string]any{"inReplyTo": mails[len(mails)-1].messageID}))
		if threaded.Outcome != inbound.Run || threaded.Party == nil || threaded.Party.Key != gus || threaded.Grant != gusGrant {
			t.Fatalf("threaded: %+v", threaded)
		}
		if threaded.Conversation != w.message(sent.Message).Str("conversation") {
			t.Fatal("the threaded reply is not in the send's conversation")
		}
		if n := w.drainRuns(); n != 1 {
			t.Fatalf("%d run requests ran", n)
		}
		fenceReports := w.reportsOf(fence)
		if len(fenceReports) != 1 || fenceReports[0].Body != "Fence painted" {
			t.Fatalf("the fence's reports: %+v", fenceReports)
		}
		if reports := w.reportsOf(w.job); len(reports) != 0 {
			t.Fatalf("the gate's reports: %+v", reports)
		}
	})

	t.Run("sending to a party takes the permission, keeps to the rate limit, waits for approval when asked, and stops after STOP", func(t *testing.T) {
		w := newWorld(t, func(body string) inbound.Decision {
			switch {
			case strings.HasPrefix(body, "Quote"):
				return inbound.Ask
			case strings.HasPrefix(body, "Refund"):
				return inbound.Deny
			}
			return inbound.Allow
		})
		party := orchestration.PartyRef{Table: "Contact", Key: w.dee}
		var refusal *inbound.Error
		if _, err := w.inbound.Send(ctx, inbound.SendInput{As: orchestration.Principal{Principal: access.Principal{Subject: w.ann, Kind: access.KindUser}}, Party: party, Channel: "sms", Body: "Hello"}); !errors.As(err, &refusal) {
			t.Fatalf("a send without the permission: %v", err)
		}
		// An agent run sends for the person it acts for.
		agent := orchestration.Principal{Principal: access.Principal{Subject: uuid.NewString(), Kind: access.KindAgent, ActingFor: &access.ActingFor{Kind: access.KindUser, Subject: w.ann}}, Permissions: []string{"parties.message"}}
		first := w.send(inbound.SendInput{As: agent, Party: party, Channel: "sms", Body: "The gate is booked for Monday."})
		if first.Outcome != inbound.Sent {
			t.Fatalf("the first send: %+v", first)
		}
		if got := w.relays.smsSent(); len(got) != 1 || got[0].to != phoneDee {
			t.Fatalf("sms: %+v", got)
		}
		row := w.message(first.Message)
		if row.Str("direction") != "outbound" || row.Str("state") != "sent" || row.Str("author") != agent.Subject || row.Str("authorKind") != "agent" || row.Str("forSubject") != w.ann || row.Str("via") != "sms" {
			t.Fatalf("the sent message: %v", row)
		}
		// Asked: held until approved.
		asked := w.send(inbound.SendInput{As: w.staff, Party: party, Channel: "sms", Body: "Quote: 400 for the gate."})
		if asked.Outcome != inbound.AwaitingApproval || len(w.relays.smsSent()) != 1 {
			t.Fatalf("the asked send: %+v", asked)
		}
		if released := must(w.inbound.Release(ctx, asked.Message)); released.Outcome != inbound.Sent || len(w.relays.smsSent()) != 2 {
			t.Fatalf("the release: %+v", released)
		}
		if denied := w.send(inbound.SendInput{As: w.staff, Party: party, Channel: "sms", Body: "Refund issued."}); denied.Outcome != inbound.Refused || denied.Detail != "the approval policy refuses it" {
			t.Fatalf("the denied send: %+v", denied)
		}
		if third := w.send(inbound.SendInput{As: w.staff, Party: party, Channel: "sms", Body: "See you Monday."}); third.Outcome != inbound.Sent {
			t.Fatalf("the third send: %+v", third)
		}
		if limited := w.send(inbound.SendInput{As: w.staff, Party: party, Channel: "sms", Body: "One more thing."}); limited.Outcome != inbound.Refused || !strings.Contains(limited.Detail, "the most it may be") {
			t.Fatalf("the limited send: %+v", limited)
		}
		if len(w.relays.smsSent()) != 3 {
			t.Fatalf("%d texts", len(w.relays.smsSent()))
		}
		// STOP is the adapter's to read and the core's to keep.
		if stop := w.receive("SMS", text(phoneDee, "STOP", nil)); stop.Outcome != inbound.OptOut {
			t.Fatalf("STOP: %+v", stop)
		}
		identity := must(w.db.GetChannelIdentityRepository().FindOne(system, &orm.ChannelIdentityFilter{Kind: &orm.StringFilter{Eq: ptr("phone")}, Address: &orm.StringFilter{Eq: ptr(phoneDee)}}, nil))
		if identity.OptedOutAt == nil {
			t.Fatal("STOP left the identity opted in")
		}
		if pending := w.pendingRuns(); len(pending) != 0 {
			t.Fatalf("run requests: %+v", pending)
		}
		if still := w.send(inbound.SendInput{As: w.staff, Party: party, Channel: "sms", Body: "Still there?"}); still.Detail != "the address opted out" {
			t.Fatalf("a send after STOP: %+v", still)
		}
		if start := w.receive("SMS", text(phoneDee, "start", nil)); start.Outcome != inbound.OptOut {
			t.Fatalf("start: %+v", start)
		}
		if back := w.send(inbound.SendInput{As: w.staff, Party: party, Channel: "sms", Body: "Welcome back."}); !strings.Contains(back.Detail, "the most it may be") {
			t.Fatalf("a send after START: %+v", back)
		}
		if len(w.relays.smsSent()) != 3 {
			t.Fatalf("%d texts", len(w.relays.smsSent()))
		}
		if strings.Contains(fmt.Sprintf("%+v", w.relays.smsSent())+fmt.Sprintf("%+v", w.relays.mailSent()), relayToken) {
			t.Fatal("a relay was sent its token")
		}
	})
}
