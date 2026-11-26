package ormgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// derivedFixture is the DB service whose tables Ticket and Article
// implement Derived and Provenance (D73, D103's step 4).
const derivedFixture = "fixture-derived-provenance-db"

// derivedORMCases are the cases the generated module's test runs over
// fixture-derived-provenance-db, by name (D146).
var derivedORMCases = []string{
	"a deriver's loop hands each stale field to its method and settles its answers as own fields",
	"a value the field's enum refuses counts an attempt and writes no own field",
	"a caller without the config's permission ends the loop with the table named",
}

// TestDeriverLoopOverTablesOnPostgres holds the HTTP runtime's Go deriver
// loop (runtime/http/go/deriver, D146) to the generated Go ORM of a
// database whose tables implement Derived, on Postgres, as
// TestDerivedTablesEndToEnd holds the TypeScript runtime's runDeriver to
// the TypeScript repositories: the loop pulls each table's stale fields
// through its repository's StaleDerivations, opened as a behavior caller
// holding the Derived configs' permissions, hands each to the typed method
// of its table and field, and settles the answers through
// SettleDerivations, so each derived field holds its value and nothing is
// stale after; a value the field's enum refuses counts an attempt and
// writes nothing; and a caller without the permission ends the loop. It
// generates the ORM module, adds the HTTP runtime's module to it, and runs
// derivedORMTest in it against the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL. The module links the core's
// static archive, which scripts/rules-archive.sh stages; the test skips
// without it, and in -short mode.
func TestDeriverLoopOverTablesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go deriver's end-to-end check in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Go deriver against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}

	ormDir := generateBehaviorORMModule(t, paths, derivedFixture)
	// The test runs the HTTP runtime's loop, whose module and the modules
	// it reaches by a replace of its own the ORM's go.mod names too: Go
	// takes no replace from a dependency's go.mod.
	edit := exec.Command("go", "mod", "edit",
		"-require=github.com/parable-work/superschematic/runtime/http/go@v0.0.0-00010101000000-000000000000",
		"-replace=github.com/parable-work/superschematic/runtime/http/go="+paths.HTTPRuntimeGo,
		"-replace=github.com/parable-work/superschematic/runtime/schema/go="+paths.SchemaRuntimeGo,
		"-replace=github.com/parable-work/superschematic/ir="+paths.SchemaIR,
	)
	edit.Dir = ormDir
	if out, err := edit.CombinedOutput(); err != nil {
		t.Fatalf("go mod edit: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "deriver_test.go"), []byte(derivedORMTest), 0o644); err != nil {
		t.Fatalf("write the deriver's test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range derivedORMCases {
		if want := "--- PASS: TestDeriverLoopOverTablesOnPostgres/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// derivedORMTest is the generated module's test: tickets and articles
// through the generated repositories, in a schema of their own, and the
// HTTP runtime's deriver loop over their derived operations, with a model
// that is not one.
const derivedORMTest = `package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-derived-provenance-db"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/deriver"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
)

// derivedInputs are the inputs of a ticket's derived fields.
type derivedInputs struct {
	Subject *string ` + "`json:\"subject,omitempty\"`" + `
	Body    *string ` + "`json:\"body,omitempty\"`" + `
}

// articleInputs are the inputs of an article's summary.
type articleInputs struct {
	Body *string ` + "`json:\"body,omitempty\"`" + `
}

func TestDeriverLoopOverTablesOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the deriver against Postgres")
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer base.Close()
	schema := fmt.Sprintf("ormgen_deriver_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("connect generated orm: %v", err)
	}
	defer db.Close()

	// The deriver acts as itself, holding each Derived config's permission,
	// as a deriver's generated main.go opens the repositories.
	deriving := WithBehaviorCaller(ctx, rules.Caller{Subject: "derived-go-api-fill-all", Permissions: []string{"articles.derive", "tickets.derive"}})
	tables := []deriver.Table{
		deriver.TableOf("Ticket", db.GetTicketRepository().Derived().StaleDerivations, db.GetTicketRepository().Derived().SettleDerivations),
		deriver.TableOf("Article", db.GetArticleRepository().Derived().StaleDerivations, db.GetArticleRepository().Derived().SettleDerivations),
	}
	refusedForGood := func(err error) bool {
		refusal, ok := rules.AsRefusal(err)
		return ok && refusal.Code != rules.CodeBusy
	}
	// row is a row's own fields as JSON reads them.
	row := func(t *testing.T, value any) map[string]any {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// run runs the loop until done holds, then stops it, and returns what it
	// returned.
	run := func(t *testing.T, ctx context.Context, answer func(context.Context, string, deriver.Item) (deriver.Answer, error), done func() bool) error {
		t.Helper()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stopped := make(chan error, 1)
		go func() {
			stopped <- deriver.Run(ctx, deriver.Options{Tables: tables, Answer: answer, Idle: 50 * time.Millisecond, Permanent: refusedForGood})
		}()
		deadline := time.Now().Add(30 * time.Second)
		for !done() {
			select {
			case err := <-stopped:
				return err
			case <-time.After(20 * time.Millisecond):
			}
			if time.Now().After(deadline) {
				t.Fatal("the deriver did not settle in thirty seconds")
			}
		}
		cancel()
		return <-stopped
	}

	t.Run("a deriver's loop hands each stale field to its method and settles its answers as own fields", func(t *testing.T) {
		ticket, err := db.Ticket.CreateOne(ctx, &TicketCreate{Subject: "I was charged twice", Body: "Please refund the second charge."})
		if err != nil {
			t.Fatalf("create a ticket: %v", err)
		}
		article, err := db.Article.CreateOne(ctx, &ArticleCreate{Title: "Refunds", Body: "A refund reaches you in five days."})
		if err != nil {
			t.Fatalf("create an article: %v", err)
		}
		sure := 0.95
		var asked []string
		answer := deriver.Methods("FillAll", map[string]func(context.Context, deriver.Item) (deriver.Answer, error){
			"Ticket.category": deriver.Method(func(_ context.Context, item deriver.DerivedItem[derivedInputs]) (deriver.DerivedAnswer[types.Category], error) {
				if item.Inputs.Subject == nil || !strings.Contains(*item.Inputs.Subject, "charged") {
					return deriver.Failed[types.Category]("inputs", "no subject"), nil
				}
				asked = append(asked, "category:"+item.Model)
				return deriver.DerivedAnswer[types.Category]{Value: types.Category("billing"), Confidence: &sure}, nil
			}),
			"Ticket.priority": deriver.Method(func(_ context.Context, item deriver.DerivedItem[derivedInputs]) (deriver.DerivedAnswer[string], error) {
				return deriver.DerivedAnswer[string]{Value: "high", Confidence: &sure}, nil
			}),
			"Article.summary": deriver.Method(func(_ context.Context, item deriver.DerivedItem[articleInputs]) (deriver.DerivedAnswer[string], error) {
				return deriver.Value("Refunds take five days."), nil
			}),
		})
		ticketRow := func() map[string]any {
			got, err := db.Ticket.GetOne(ctx, *ticket.Id, nil)
			if err != nil {
				t.Fatalf("read the ticket: %v", err)
			}
			return row(t, got)
		}
		articleRow := func() map[string]any {
			got, err := db.Article.GetOne(ctx, *article.Id, nil)
			if err != nil {
				t.Fatalf("read the article: %v", err)
			}
			return row(t, got)
		}
		done := func() bool {
			ticket, article := ticketRow(), articleRow()
			return ticket["category"] == "billing" && ticket["priority"] == "high" && article["summary"] == "Refunds take five days."
		}
		if err := run(t, deriving, answer, done); err != nil {
			t.Fatalf("Run = %v", err)
		}
		if len(asked) != 1 || asked[0] != "category:standard" {
			t.Errorf("the category's method was asked %v, want once with the model standard", asked)
		}
		page, err := db.GetTicketRepository().Derived().StaleDerivations(deriving, TicketDerivedStaleDerivationsParams{})
		if err != nil {
			t.Fatalf("pull: %v", err)
		}
		for _, item := range page.Items {
			if item.Id == ticket.Id.String() {
				t.Errorf("the ticket's %s is stale after it settled", item.Field)
			}
		}
	})

	t.Run("a value the field's enum refuses counts an attempt and writes no own field", func(t *testing.T) {
		ticket, err := db.Ticket.CreateOne(ctx, &TicketCreate{Subject: "The page is blank", Body: "Nothing loads."})
		if err != nil {
			t.Fatalf("create a ticket: %v", err)
		}
		answer := deriver.Methods("FillAll", map[string]func(context.Context, deriver.Item) (deriver.Answer, error){
			"Ticket.category": deriver.Method(func(context.Context, deriver.DerivedItem[derivedInputs]) (deriver.DerivedAnswer[types.Category], error) {
				return deriver.Value(types.Category("weather")), nil
			}),
			"Ticket.priority": deriver.Method(func(context.Context, deriver.DerivedItem[derivedInputs]) (deriver.DerivedAnswer[string], error) {
				return deriver.Failed[string]("refused", "the model refused"), nil
			}),
			"Article.summary": deriver.Method(func(context.Context, deriver.DerivedItem[articleInputs]) (deriver.DerivedAnswer[string], error) {
				return deriver.Value("A summary."), nil
			}),
		})
		failed := func() bool {
			page, err := db.GetTicketRepository().Derived().FailedDerivations(deriving, TicketDerivedFailedDerivationsParams{})
			if err != nil {
				t.Fatalf("failed derivations: %v", err)
			}
			n := 0
			for _, item := range page.Items {
				if item.Id == ticket.Id.String() {
					n++
				}
			}
			return n == 2
		}
		if err := run(t, deriving, answer, failed); err != nil {
			t.Fatalf("Run = %v", err)
		}
		got, err := db.Ticket.GetOne(ctx, *ticket.Id, nil)
		if err != nil {
			t.Fatalf("read the ticket: %v", err)
		}
		if fields := row(t, got); fields["category"] != nil || fields["priority"] != nil {
			t.Errorf("the ticket holds %v and %v, want no own field written", fields["category"], fields["priority"])
		}
	})

	t.Run("a caller without the config's permission ends the loop with the table named", func(t *testing.T) {
		if _, err := db.Ticket.CreateOne(ctx, &TicketCreate{Subject: "Another", Body: "One more."}); err != nil {
			t.Fatalf("create a ticket: %v", err)
		}
		nobody := WithBehaviorCaller(ctx, rules.Caller{Subject: "nobody"})
		answer := func(context.Context, string, deriver.Item) (deriver.Answer, error) {
			return deriver.Answer{Value: json.RawMessage(` + "`\"x\"`" + `)}, nil
		}
		err := run(t, nobody, answer, func() bool { return false })
		refusal, ok := rules.AsRefusal(err)
		if !ok || refusal.Code != rules.CodeForbidden || !strings.Contains(err.Error(), "table Ticket") {
			t.Fatalf("Run = %v, want the core's forbidden with the table named", err)
		}
		if errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want it ended by the refusal, not stopped", err)
		}
	})
}
`
