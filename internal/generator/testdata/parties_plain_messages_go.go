// fixture-parties-plain-messages-db's inbound path in the Go repositories
// and the HTTP runtime's inbound package (D157, D192), run by
// TestGoPartiesPlainMessages: the messages keep references and hold no
// files, and the reports hold files. A party's text is filed onto a report
// and onto the granted job once, with no error: filing asks no table for
// files it does not hold, so the run request is handled the first time and
// is not run again. The case runs on a SQLite file and, with the Postgres
// PARTIES_TEST_DATABASE_URL names, on Postgres too, and the two answer
// alike.
package orm_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
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

	orm "example.com/schemas/orm/fixture-parties-plain-messages-db"
	types "example.com/schemas/types/go/fixture-parties-plain-messages-db"
)

const relayToken = "relay-token"

var (
	photo    = []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4}
	phoneDee = "+15550100011"
	system   = orm.AsSystem(context.Background())
)

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

// smsRelay reads its inbound callbacks, a text and the photo it came with,
// and sends nothing.
type smsRelay struct{}

func (smsRelay) Kind() string { return "sms" }

func (smsRelay) Send(context.Context, providers.OutboundMessage) (providers.SendResult, error) {
	return providers.SendResult{}, errors.New("the relay sends nothing here")
}

func (smsRelay) Health(context.Context) (providers.ChannelHealth, error) {
	return providers.ChannelHealth{OK: true}, nil
}

func (smsRelay) Inbound(_ context.Context, request providers.WebhookRequest) ([]providers.InboundMessage, error) {
	if request.Headers["x-relay-token"] != relayToken {
		return nil, errors.New("the callback holds no relay token")
	}
	var body struct {
		Messages []struct {
			ID, From, Text string
			At             time.Time
			Media          []struct{ URL, Type, Name string }
		}
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return nil, err
	}
	var out []providers.InboundMessage
	for _, m := range body.Messages {
		message := providers.InboundMessage{From: m.From, Body: m.Text, ReceivedAt: m.At, ProviderMessageID: m.ID}
		for _, media := range m.Media {
			message.Attachments = append(message.Attachments, providers.InboundAttachment{Name: media.Name, MediaType: media.Type, Bytes: slices.Clone(photo)})
		}
		out = append(out, message)
	}
	return out, nil
}

// noBucket is a bucket no file reaches: the messages hold none.
type noBucket struct{}

func (noBucket) SignedURL(_ context.Context, name, method string, _ time.Duration, _ string) (string, error) {
	return "https://files.test/" + name + "?method=" + method, nil
}

func (noBucket) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, rowrefs.ErrObjectNotFound
}

func (noBucket) Delete(context.Context, string) error { return nil }

func (noBucket) PutObject(context.Context, string, string, []byte) error {
	return errors.New("no file reaches the bucket")
}

// database is the ORM's Postgres database or its SQLite one, and its
// orchestration's host.
type database interface {
	orm.DatabaseInterface
	Orchestration() orchestration.Host
}

// dialect is one of the two databases the case runs on, and its
// statements the case writes by hand, in Postgres's words, which exec
// rewrites for SQLite.
type dialect struct {
	name string
	db   database
	exec func(ctx context.Context, sql string, args ...any) error
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
		for i := 9; i >= 1; i-- {
			text = strings.ReplaceAll(text, fmt.Sprintf("$%d", i), fmt.Sprintf("?%d", i))
		}
		for i, arg := range args {
			if at, ok := arg.(time.Time); ok {
				args[i] = at.UTC().Format("2006-01-02T15:04:05.000Z")
			}
		}
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
	schema := fmt.Sprintf("parties_plain_%d", time.Now().UnixNano())
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

func (d dialect) mustExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if err := d.exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s on %s: %v", sql, d.name, err)
	}
}

func (d dialect) backlinks(t *testing.T, table, key string) []string {
	t.Helper()
	links := must(d.db.Orchestration().References().ReferencedBy(system, rowrefs.RowRef{Table: table, Key: key}, rowrefs.ReferencedByOptions{}))
	var out []string
	for _, b := range links.Items {
		if b.Table == "Message" {
			out = append(out, b.Key)
		}
	}
	return out
}

// plainMessagesCase is the case on one database, and its answers.
func plainMessagesCase(t *testing.T, open func(*testing.T) dialect) map[string]any {
	ctx := context.Background()
	answers := map[string]any{}
	t.Run("a text from a party with a grant is filed onto the granted record once, as references alone, when the messages hold no files", func(t *testing.T) {
		d := open(t)
		db := d.db
		host := db.Orchestration()
		relay := providers.MustDefine(providers.Definition{
			Name: "sms-relay", Implements: providers.Channel, Role: []string{"messages.relay"},
			Create: func(context.Context, providers.Settings, providers.Context) (any, error) { return smsRelay{}, nil },
		})
		p := must(providers.From(providers.Options{
			Slots:       map[string]providers.Interface{"SMS": providers.Channel},
			Config:      map[string]providers.ConfigValue{"SMS": providers.Text("sms-relay")},
			Definitions: []providers.Definition{relay},
		}))
		n := must(notify.New(host, notify.Options{Providers: p}))
		in := must(inbound.New(host, inbound.Options{Notifier: n, Providers: p, Bucket: noBucket{}}))

		must(db.GetRoleRepository().CreateOne(system, &types.Role{Name: "contractor", Permissions: []string{"reports.write"}, Reaches: []string{"Report:job"}}))
		dee := must(db.GetContactRepository().CreateOne(system, &types.Contact{Name: "Dee"})).Id.String()
		job := must(db.GetJobRepository().CreateOne(system, &types.Job{Title: "Fix the gate", Status: "open"})).Id.String()
		d.mustExec(t, `INSERT INTO channel_identity (kind, address, principal_kind, principal_table, principal, trust, verified_at) VALUES ('phone', $1, 'party', 'Contact', $2, 'verified', $3)`, phoneDee, rowrefs.UUIDToDB(dee), time.Now())
		grant, _, err := host.IssueGrant(system, access.IssueGrantInput{Holder: dee, HolderKind: "party", Role: "contractor", TargetType: "Job", TargetID: job})
		check(t, err)

		message := map[string]any{"id": "in-" + uuid.NewString(), "from": phoneDee, "text": "Gate fixed", "at": time.Now(), "media": []any{map[string]string{"url": "https://sms.test/media/1", "type": "image/jpeg", "name": "gate.jpg"}}}
		request := providers.WebhookRequest{Headers: map[string]string{"x-relay-token": relayToken}, Body: string(must(json.Marshal(map[string]any{"messages": []any{message}})))}
		outcomes := must(in.Inbound(ctx, "SMS", request))
		if len(outcomes) != 1 || outcomes[0].Outcome != inbound.Run || outcomes[0].Grant != grant.ID {
			t.Fatalf("the outcomes: %+v", outcomes)
		}
		outcome := outcomes[0]
		// The photo is dropped, and the message says why: its table holds no
		// files.
		row := must(in.Message(ctx, outcome.Message))
		if !strings.Contains(row.Str("detail"), "no @attachments") {
			t.Fatalf("the message's detail: %q", row.Str("detail"))
		}

		// The run request's handler, as the party through its grant: a
		// report on the granted job, the message filed onto it and onto
		// the job. A handler that fails is a run request run again.
		runs := 0
		report := ""
		handle := func(ctx context.Context, request inbound.RunRequest) error {
			runs++
			party, err := in.PrincipalOf(ctx, request.Party, request.Grant)
			if err != nil {
				return err
			}
			granted := party.Grants[0]
			jobID := must(types.ParseIdentityUUID(granted.TargetID))
			created, err := db.GetReportRepository().CreateOne(orm.WithPrincipal(ctx, party.Principal), &types.Report{Job: types.Job{Id: &jobID}, Body: "Gate fixed", CreatedAt: types.TemporalDateTime(time.Now())})
			if err != nil {
				return err
			}
			report = created.Id.String()
			if err := in.File(ctx, inbound.FileInput{Message: request.Message, To: rowrefs.RowRef{Table: "Report", Key: report}, As: party}); err != nil {
				return err
			}
			return in.File(ctx, inbound.FileInput{Message: request.Message, To: rowrefs.RowRef{Table: granted.TargetType, Key: granted.TargetID}, As: party})
		}
		queue := must(in.RunQueue(handle))
		claims := must(queue.Claim(ctx, 10, 30*time.Second))
		if len(claims) != 1 {
			t.Fatalf("%d run requests claimed", len(claims))
		}
		if err := claims[0].Handle(ctx); err != nil {
			t.Fatalf("the run request: %v", err)
		}
		check(t, queue.Complete(ctx, claims[0].ID, claims[0].Token))
		// Nothing is left to run again.
		if left := must(queue.Claim(ctx, 10, 30*time.Second)); len(left) != 0 || runs != 1 {
			t.Fatalf("%d runs, and %d run requests left", runs, len(left))
		}

		// The message is on the report, the job and the party, once each,
		// and the report holds no file of it.
		for _, ref := range [][2]string{{"Report", report}, {"Job", job}, {"Contact", dee}} {
			if links := d.backlinks(t, ref[0], ref[1]); len(links) != 1 || links[0] != outcome.Message {
				t.Fatalf("%s %s lists %v", ref[0], ref[1], links)
			}
		}
		if files := must(host.Attachments().List(system, rowrefs.RowRef{Table: "Report", Key: report})); len(files) != 0 {
			t.Fatalf("the report's files: %+v", files)
		}
		// Filing it again changes nothing.
		party := must(in.PrincipalOf(ctx, orchestration.PartyRef{Table: "Contact", Key: dee}, ""))
		check(t, in.File(ctx, inbound.FileInput{Message: outcome.Message, To: rowrefs.RowRef{Table: "Report", Key: report}, As: party}))
		if links := d.backlinks(t, "Report", report); len(links) != 1 {
			t.Fatalf("the report lists %v after filing again", links)
		}
		answers["plain"] = map[string]any{"outcome": outcome.Outcome, "runs": runs, "detail": row.Str("detail")}
	})
	return answers
}

// TestPartiesPlainMessages runs the case on SQLite and, with a Postgres,
// on Postgres, and holds its answers on the two equal.
func TestPartiesPlainMessages(t *testing.T) {
	var answers [2]map[string]any
	t.Run("sqlite", func(t *testing.T) { answers[0] = plainMessagesCase(t, openSQLite) })
	if os.Getenv("PARTIES_TEST_DATABASE_URL") == "" {
		t.Log("set PARTIES_TEST_DATABASE_URL to run the same case on Postgres")
		return
	}
	t.Run("postgres", func(t *testing.T) { answers[1] = plainMessagesCase(t, openPostgres) })
	names := make([]string, 0, len(answers[0]))
	for name := range answers[0] {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 || len(answers[1]) != len(names) {
		t.Fatalf("the case answered %d times on SQLite and %d on Postgres", len(names), len(answers[1]))
	}
	for _, name := range names {
		sqlite, _ := json.Marshal(answers[0][name])
		postgres, _ := json.Marshal(answers[1][name])
		if string(sqlite) != string(postgres) {
			t.Errorf("%s: SQLite and Postgres differ\nsqlite:   %s\npostgres: %s", name, sqlite, postgres)
		}
	}
	if !t.Failed() {
		t.Logf("SQLite and Postgres answered %d cases alike", len(names))
	}
}
