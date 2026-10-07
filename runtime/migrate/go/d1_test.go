package migrate_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/d1"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
)

// openD1 opens a D1 driver on the fake server of url with opts over
// testdb's.
func openD1(t *testing.T, url string, edit func(*d1.Options)) *d1.Driver {
	t.Helper()
	opts := testdb.D1Options(t, url)
	if edit != nil {
		edit(&opts)
	}
	d, err := d1.Open(context.Background(), url, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close(context.Background()) })
	return d
}

// TestD1Lease: a runner waits for the lease another holds; once that
// lease expires, it takes it over and applies the plan, and a batch of the
// runner that held it fails without running.
func TestD1Lease(t *testing.T) {
	ctx := context.Background()
	url := testdb.NewD1(t)
	dead := openD1(t, url, func(o *d1.Options) { o.Holder, o.Lease = "dead-runner", 400*time.Millisecond })
	// The lease starts while Lock runs, so the wait is measured from before
	// it: a lease can then end no sooner than 400ms after began, however
	// long Lock takes to return.
	began := time.Now()
	if _, err := dead.Lock(ctx, "shop"); err != nil {
		t.Fatal(err)
	}

	impatient := &migrate.Runner{Driver: openD1(t, url, func(o *d1.Options) { o.LockWait = 50 * time.Millisecond }), Log: &testLog{t: t}}
	_, err := impatient.Apply(ctx, plan(t, migrate.SQLite, "01-create"), migrate.All)
	if err == nil || !strings.Contains(err.Error(), "dead-runner holds the lease of service shop until ") {
		t.Fatalf("apply while another runner holds the lease = %v", err)
	}

	r := &migrate.Runner{Driver: openD1(t, url, func(o *d1.Options) { o.Holder = "next-runner" }), Log: &testLog{t: t}}
	result := apply(t, r, plan(t, migrate.SQLite, "01-create"), migrate.All)
	if !result.Finished || time.Since(began) < 400*time.Millisecond {
		t.Fatalf("apply took the lease after %s: %+v", time.Since(began), result)
	}
	if got := testdb.Strings(t, url, `SELECT holder FROM superschematic_lock WHERE service = 'shop'`); !slices.Equal(got, []string{"next-runner"}) {
		t.Fatalf("the lease's holder: %v", got)
	}

	err = dead.Transact(ctx, migrate.TxOptions{}, func(ctx context.Context, conn migrate.Conn) error {
		return conn.Exec(ctx, `CREATE TABLE late (id INTEGER)`)
	})
	if err == nil || !strings.Contains(err.Error(), "another runner took over the lease of service shop") {
		t.Fatalf("a batch after the lease passed = %v", err)
	}
	if got := testdb.Strings(t, url, `SELECT name FROM sqlite_master WHERE name = 'late'`); len(got) != 0 {
		t.Fatal("the batch ran")
	}
}

// TestD1AFailedBatchRollsBack: a step whose batch fails, on a statement or
// on a foreign key broken at its end, leaves nothing: neither the
// statements before the one that failed nor its log row.
func TestD1AFailedBatchRollsBack(t *testing.T) {
	for _, c := range []struct {
		name, statement, says string
	}{
		{"a failed statement", `INSERT INTO missing (id) VALUES (1)`, "no such table: missing"},
		{"a foreign key broken at the end", `INSERT INTO "order" (id, customer_id, total) VALUES (9, 99, 1)`, "FOREIGN KEY constraint failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			url := testdb.NewD1(t)
			r := newRunner(t, url)
			broken := edited(t, migrate.SQLite, "01-create", func(p map[string]any) {
				p["steps"] = append(steps(p), map[string]any{
					"index": len(steps(p)) + 1, "phase": "expand", "op": "createTable", "subject": "table/extra",
					"statements": []any{`CREATE TABLE extra (id INTEGER)`, c.statement}, "transactional": true,
				})
			})
			_, err := r.Apply(context.Background(), broken, migrate.All)
			var stepErr *migrate.StepError
			if !errors.As(err, &stepErr) || stepErr.Index != 4 || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("apply = %v, want step 4 to fail with %q", err, c.says)
			}
			if got := testdb.Strings(t, url, `SELECT name FROM sqlite_master WHERE name = 'extra'`); len(got) != 0 {
				t.Fatal("the failed batch created table extra")
			}
			st := status(t, r, "shop")
			if st.PlanHash != broken.Hash || len(st.Steps) != 3 {
				t.Fatalf("status after the failed batch: %+v", st)
			}
		})
	}
}

// TestD1RefusesForeignKeysOff: a plan with a step that turns foreign keys
// off is refused before anything runs, naming the step.
func TestD1RefusesForeignKeysOff(t *testing.T) {
	url := testdb.NewD1(t)
	r := newRunner(t, url)
	create := plan(t, migrate.SQLite, "01-create")
	apply(t, r, create, migrate.All)
	seed(t, url)
	refused(t, r, plan(t, migrate.SQLite, "02-evolve"), migrate.All, "step 2 (table/customer) turns foreign keys off", "D1 keeps foreign key enforcement on")
	refused(t, r, plan(t, migrate.SQLite, "fk-violation"), migrate.All, "step 1 (table/customer) turns foreign keys off")
	if st := status(t, r, "shop"); st.ModelHash != create.To || st.PlanHash != "" {
		t.Fatalf("a refusal changed the state: %+v", st)
	}
	if got := count(t, url, `"order"`); got != "3" {
		t.Fatalf("orders after the refusals: %s", got)
	}
}

// TestD1Token: a driver without a token is refused, naming the variable,
// and a wrong token fails the first request with the API's error.
func TestD1Token(t *testing.T) {
	url := testdb.NewD1(t)
	opts := testdb.D1Options(t, url)
	opts.Token = ""
	if _, err := d1.Open(context.Background(), url, opts); err == nil || !strings.Contains(err.Error(), "set CLOUDFLARE_API_TOKEN") {
		t.Fatalf("Open without a token = %v", err)
	}
	r := &migrate.Runner{Driver: openD1(t, url, func(o *d1.Options) { o.Token = "wrong" })}
	_, err := r.Apply(context.Background(), plan(t, migrate.SQLite, "01-create"), migrate.All)
	if err == nil || !strings.Contains(err.Error(), "HTTP 403: Authentication error") {
		t.Fatalf("apply with a wrong token = %v", err)
	}
}

// TestD1RebuildKeepsChildren: the rebuild of D27's amendment, of customer
// together with order, which references it ON DELETE CASCADE, keeps every
// order on D1 and on a SQLite file, and the two end with one schema. The
// rebuild of 02-evolve, of customer alone, deletes the orders when foreign
// keys stay on, as they do on D1.
func TestD1RebuildKeepsChildren(t *testing.T) {
	create, evolve := plan(t, migrate.SQLite, "01-create"), plan(t, migrate.SQLite, "evolve-fk-on")
	var catalogs [][]string
	for _, db := range []testdb.Backend{testdb.D1, testdb.SQLite} {
		url := testdb.New(t, db)
		r := newRunner(t, url)
		apply(t, r, create, migrate.All)
		seed(t, url)
		apply(t, r, evolve, migrate.All)
		if got := testdb.Strings(t, url, `SELECT customer_id FROM "order" ORDER BY id`); !slices.Equal(got, []string{"1", "1", "2"}) {
			t.Fatalf("%s: orders after the rebuild: %v", db, got)
		}
		if got := testdb.Strings(t, url, `PRAGMA foreign_key_check`); len(got) != 0 {
			t.Fatalf("%s: foreign_key_check: %v", db, got)
		}
		catalogs = append(catalogs, testdb.Catalog(t, url))
	}
	if !slices.Equal(catalogs[0], catalogs[1]) {
		t.Fatalf("D1 and SQLite differ:\n%s\n%s", strings.Join(catalogs[0], "\n"), strings.Join(catalogs[1], "\n"))
	}

	url := testdb.NewD1(t)
	r := newRunner(t, url)
	apply(t, r, create, migrate.All)
	seed(t, url)
	apply(t, r, edited(t, migrate.SQLite, "02-evolve", func(p map[string]any) {
		delete(steps(p)[1].(map[string]any), "foreignKeysOff")
	}), migrate.All)
	if got := count(t, url, `"order"`); got != "0" {
		t.Fatalf("orders after a rebuild of customer alone: %s, want the cascade to delete them", got)
	}
}

// EnvD1URL names a D1 database for TestRealD1, as a d1:// URL.
const EnvD1URL = "SUPERSCHEMATIC_MIGRATE_TEST_D1_URL"

// TestRealD1 runs d1Chain against a real D1 database: the one EnvD1URL
// names, with the token in CLOUDFLARE_API_TOKEN. It skips without them. The
// database is scratch: the test drops the chain's tables and the runner's
// before it starts. Until it has passed, the D1 driver is unverified (D27,
// amended).
func TestRealD1(t *testing.T) {
	url, token := os.Getenv(EnvD1URL), os.Getenv(d1.TokenEnv)
	if url == "" || token == "" {
		t.Skipf("set %s and %s to run the runner against a real D1 database", EnvD1URL, d1.TokenEnv)
	}
	driver, err := d1.Open(context.Background(), url, d1.Options{Token: token})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close(context.Background()) })
	d1Chain(t, driver)
}

// TestD1Chain runs TestRealD1's chain on the fake.
func TestD1Chain(t *testing.T) {
	d1Chain(t, openD1(t, testdb.NewD1(t), nil))
}

// d1Chain applies 01-create, then evolve-fk-on, whose rebuild of customer
// takes order, which references it ON DELETE CASCADE, along in one batch
// with defer_foreign_keys on, then 03-audit, and checks the orders survive.
// Then a step whose batch breaks a foreign key fails, and leaves nothing.
// It reaches the database only through driver.
func d1Chain(t *testing.T, driver *d1.Driver) {
	ctx := context.Background()
	exec := func(statements ...string) {
		t.Helper()
		err := driver.Transact(ctx, migrate.TxOptions{}, func(ctx context.Context, conn migrate.Conn) error {
			for _, statement := range statements {
				if err := conn.Exec(ctx, statement); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	query := func(sql string) []string {
		t.Helper()
		var out []string
		err := driver.Transact(ctx, migrate.TxOptions{ReadOnly: true}, func(ctx context.Context, conn migrate.Conn) error {
			return conn.Query(ctx, sql, nil, func(scan func(dest ...any) error) error {
				var value string
				if err := scan(&value); err != nil {
					return err
				}
				out = append(out, value)
				return nil
			})
		})
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return out
	}

	exec(`DROP TABLE IF EXISTS "order"`, `DROP TABLE IF EXISTS order__new`, `DROP TABLE IF EXISTS customer`,
		`DROP TABLE IF EXISTS customer__new`, `DROP TABLE IF EXISTS audit`, `DROP TABLE IF EXISTS superschematic_schema_state`,
		`DROP TABLE IF EXISTS superschematic_migrations`, `DROP TABLE IF EXISTS superschematic_lock`)
	r := &migrate.Runner{Driver: driver, Log: &testLog{t: t}}
	create, evolve, audit := plan(t, migrate.SQLite, "01-create"), plan(t, migrate.SQLite, "evolve-fk-on"), plan(t, migrate.SQLite, "03-audit")
	apply(t, r, create, migrate.All)
	exec(`INSERT INTO customer (id, name) VALUES (1, 'Ada'), (2, 'Grace')`,
		`INSERT INTO "order" (id, customer_id, total) VALUES (1, 1, 100), (2, 1, 200), (3, 2, 300)`)

	// The rebuild of customer, with order, which references it ON DELETE
	// CASCADE, in one batch with defer_foreign_keys on.
	apply(t, r, evolve, migrate.Expand)
	if got := query(`SELECT customer_id FROM "order" ORDER BY id`); !slices.Equal(got, []string{"1", "1", "2"}) {
		t.Fatalf("orders after the rebuild: %v", got)
	}
	if got := query(`SELECT name FROM customer ORDER BY id`); !slices.Equal(got, []string{"Ada", "Grace"}) {
		t.Fatalf("customers after the rebuild: %v", got)
	}
	if got := query(`SELECT sql FROM sqlite_master WHERE name = 'order'`); len(got) != 1 || !strings.Contains(got[0], `REFERENCES "customer"`) {
		t.Fatalf("order after the rebuild: %v", got)
	}
	apply(t, r, evolve, migrate.Contract)
	apply(t, r, audit, migrate.All)
	if st := status(t, r, "shop"); st.ModelHash != audit.To || st.PlanHash != "" {
		t.Fatalf("status after the chain: %+v", st)
	}
	if got := query(`SELECT count(*) FROM "order"`); !slices.Equal(got, []string{"3"}) {
		t.Fatalf("orders after the chain: %v", got)
	}

	// A step whose batch breaks a foreign key fails at its end, and none
	// of it, its log row included, stays.
	broken := edited(t, migrate.SQLite, "04-drop-audit", func(p map[string]any) {
		steps(p)[0].(map[string]any)["statements"] = []any{`DROP TABLE audit`, `INSERT INTO "order" (id, customer_id, total) VALUES (9, 99, 1)`}
	})
	var stepErr *migrate.StepError
	if _, err := r.Apply(ctx, broken, migrate.All); !errors.As(err, &stepErr) || stepErr.Index != 1 {
		t.Fatalf("apply of a step that breaks a foreign key = %v", err)
	}
	if got := query(`SELECT count(*) FROM sqlite_master WHERE name = 'audit'`); !slices.Equal(got, []string{"1"}) {
		t.Fatal("the failed batch dropped audit")
	}
	if st := status(t, r, "shop"); st.PlanHash != broken.Hash || len(st.Steps) != 0 {
		t.Fatalf("status after the failed batch: %+v", st)
	}
}
