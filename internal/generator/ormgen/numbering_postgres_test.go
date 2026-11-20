package ormgen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// numberingORMCases are the cases the generated module's test runs over
// fixture-numbering-db, by name (D103's step 4).
var numberingORMCases = []string{
	"a create numbers the invoice in its customer's count of the year",
	"a ticket's number and reference count apart",
	"a delete leaves a gap",
	"AssignNumber numbers a row inserted past the repository in the year of its createdAt and writes nothing on a numbered one",
	"creates that number at once take one number each",
}

// TestNumberingTablesOnPostgres holds the generated Go ORM of a database
// whose tables carry Numbering on fields to the rules module's adapter on
// Postgres (D103's step 4): a create takes the next number of its count, a
// row of the use's shared table, which a delete leaves; two fields count
// apart; AssignNumber numbers a row made past the repository in the year
// of its createdAt; and creates that number at once, beside the index that
// holds a label unique within its customer, each take a number of their
// own. It loads fixture-numbering-db with the core's registry, generates
// its Go types, its ORM and its DDL into a temporary tree, and builds, vets
// and tests the ORM module with numberingORMTest, whose every case must
// pass on the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL. The
// module links the core's static archive, which scripts/rules-archive.sh
// stages; the test skips without it, and in -short mode.
func TestNumberingTablesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Numbering ORM's end-to-end check in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated Numbering ORM against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}

	ormDir := generateBehaviorORMModule(t, paths, numberingFixture)
	if _, err := os.Stat(filepath.Join(ormDir, "repository_invoice_number_counts.go")); err == nil {
		t.Error("the generated ORM holds a repository of Numbering's shared table, which has none")
	}
	if err := os.WriteFile(filepath.Join(ormDir, "numbering_test.go"), []byte(numberingORMTest), 0o644); err != nil {
		t.Fatalf("write the Numbering test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range numberingORMCases {
		if want := "--- PASS: TestNumberingTablesOnPostgres/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// numberingORMTest is the generated module's test: invoices and tickets
// through the generated repository, in a schema of its own, read back past
// it with SQL.
const numberingORMTest = `package orm

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-numbering-db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNumberingTablesOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Numbering tables against Postgres")
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
	schema := fmt.Sprintf("ormgen_numbering_%d", time.Now().UnixNano())
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
	year := time.Now().UTC().Year()

	customer := func(t *testing.T, name string) types.Customer {
		t.Helper()
		row, err := db.Customer.CreateOne(ctx, &types.Customer{Name: name})
		if err != nil {
			t.Fatalf("create a customer: %v", err)
		}
		return *row
	}
	invoice := func(t *testing.T, of types.Customer, title string) string {
		t.Helper()
		row, err := db.Invoice.CreateOne(ctx, &InvoiceCreate{Customer: of, Title: title})
		if err != nil {
			t.Fatalf("create an invoice: %v", err)
		}
		if row.Number == "" {
			t.Fatalf("the invoice %q was created with no number", title)
		}
		return row.Number
	}

	t.Run("a create numbers the invoice in its customer's count of the year", func(t *testing.T) {
		acme, initech := customer(t, "Acme"), customer(t, "Initech")
		got := []string{invoice(t, acme, "Desks"), invoice(t, acme, "Chairs"), invoice(t, initech, "Lamps")}
		want := []string{fmt.Sprintf("INV-%d-0001", year), fmt.Sprintf("INV-%d-0002", year), fmt.Sprintf("INV-%d-0001", year)}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("the labels are %v, want %v", got, want)
		}
		var last, version int64
		if err := pool.QueryRow(ctx, "SELECT last, _version FROM invoice_number_counts WHERE scope = $1", fmt.Sprintf("%s/%d", acme.Id.String(), year)).Scan(&last, &version); err != nil || last != 2 || version != 2 {
			t.Fatalf("Acme's count is %d at version %d (%v), want 2 at 2", last, version, err)
		}
	})

	t.Run("a ticket's number and reference count apart", func(t *testing.T) {
		for i, want := range [][2]string{{"T-1", "REF-00001"}, {"T-2", "REF-00002"}} {
			row, err := db.Ticket.CreateOne(ctx, &TicketCreate{Subject: fmt.Sprintf("ticket %d", i)})
			if err != nil {
				t.Fatalf("create a ticket: %v", err)
			}
			if row.Number != want[0] || row.Reference != want[1] {
				t.Fatalf("the ticket's number and reference are %v and %v, want %v", row.Number, row.Reference, want)
			}
		}
	})

	t.Run("a delete leaves a gap", func(t *testing.T) {
		globex := customer(t, "Globex")
		invoice(t, globex, "One")
		row, err := db.Invoice.CreateOne(ctx, &InvoiceCreate{Customer: globex, Title: "Two"})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Invoice.DeleteOne(ctx, *row.Id); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if got, want := invoice(t, globex, "Three"), fmt.Sprintf("INV-%d-0003", year); got != want {
			t.Fatalf("the next label is %s, want %s", got, want)
		}
	})

	t.Run("AssignNumber numbers a row inserted past the repository in the year of its createdAt and writes nothing on a numbered one", func(t *testing.T) {
		hooli := customer(t, "Hooli")
		id := uuid.New()
		if _, err := pool.Exec(ctx, "INSERT INTO invoice (id, customer_id, title, created_at) VALUES ($1, $2, 'Old', $3)", id.String(), hooli.Id.ToUUID().String(), time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
		key, err := types.ParseIdentityUUID(id.String())
		if err != nil {
			t.Fatal(err)
		}
		out, err := db.Invoice.Number().AssignNumber(ctx, key, nil)
		if err != nil || out.Seq != 1 || out.Label != "INV-2025-0001" {
			t.Fatalf("AssignNumber = %+v, %v, want 1 and INV-2025-0001", out, err)
		}
		var version int64
		if err := pool.QueryRow(ctx, "SELECT _version FROM invoice WHERE id = $1", id.String()).Scan(&version); err != nil {
			t.Fatal(err)
		}
		again, err := db.Invoice.Number().AssignNumber(ctx, key, nil)
		if err != nil || again.Label != "INV-2025-0001" {
			t.Fatalf("AssignNumber again = %+v, %v", again, err)
		}
		var after int64
		if err := pool.QueryRow(ctx, "SELECT _version FROM invoice WHERE id = $1", id.String()).Scan(&after); err != nil || after != version {
			t.Fatalf("the version moved from %d to %d (%v): AssignNumber on a numbered row writes nothing", version, after, err)
		}
	})

	t.Run("creates that number at once take one number each", func(t *testing.T) {
		umbrella := customer(t, "Umbrella")
		labels := make([]string, 4)
		var wg sync.WaitGroup
		for i := range labels {
			wg.Add(1)
			go func() {
				defer wg.Done()
				labels[i] = invoice(t, umbrella, fmt.Sprintf("at once %d", i))
			}()
		}
		wg.Wait()
		sort.Strings(labels)
		want := []string{fmt.Sprintf("INV-%d-0001", year), fmt.Sprintf("INV-%d-0002", year), fmt.Sprintf("INV-%d-0003", year), fmt.Sprintf("INV-%d-0004", year)}
		if fmt.Sprint(labels) != fmt.Sprint(want) {
			t.Fatalf("the labels are %v, want %v", labels, want)
		}
	})
}
`
