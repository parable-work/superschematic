package migrate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/d1"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
	"github.com/parable-work/superschematic/runtime/migrate/go/postgres"
	"github.com/parable-work/superschematic/runtime/migrate/go/sqlite"
)

var seal = flag.Bool("seal", false, "rewrite the from, to and hash members of the plan fixtures in testdata")

// chainFile is a fixture that follows the one before it: NN-<name>.
var chainFile = regexp.MustCompile(`^\d\d-`)

// supersedeFile is a fixture that starts from the model between the phases
// of chain fixture NN: supersede-NN.
var supersedeFile = regexp.MustCompile(`^supersede-(\d\d)\.`)

// TestFixturesAreSealed: every hand-written plan in testdata/<dialect> reads
// with ReadPlan, and its from is the to of the plan before it in name order
// (the first from an empty database), for supersede-NN the expanded of NN,
// or, for any other fixture outside the chain, the to of 01. With -seal it
// rewrites from, to, expanded and hash first.
func TestFixturesAreSealed(t *testing.T) {
	for _, dialect := range []string{"postgres", "sqlite"} {
		dir := filepath.Join("testdata", dialect)
		names, err := filepath.Glob(filepath.Join(dir, "*.plan.json"))
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(names)
		var previous, first string
		expanded := map[string]string{} // NN -> the expanded of chain fixture NN
		for _, path := range names {
			name := filepath.Base(path)
			from := first
			if chainFile.MatchString(name) {
				from = previous
			}
			if m := supersedeFile.FindStringSubmatch(name); m != nil {
				from = expanded[m[1]]
				if from == "" {
					t.Fatalf("%s: fixture %s- has no expanded", path, m[1])
				}
			}
			doc, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if *seal {
				doc = sealDoc(t, doc, func(plan map[string]any) { plan["from"] = from }, true)
				if err := os.WriteFile(path, doc, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := migrate.ReadPlan(doc)
			if err != nil {
				t.Fatalf("%s: %v (run go test -run TestFixturesAreSealed -seal)", path, err)
			}
			if plan.From != from {
				t.Errorf("%s: from is %q, want %q", path, plan.From, from)
			}
			if string(plan.Dialect) != dialect {
				t.Errorf("%s: dialect %s", path, plan.Dialect)
			}
			if chainFile.MatchString(name) {
				previous = plan.To
				if first == "" {
					first = plan.To
				}
				expanded[name[:2]] = plan.Expanded
			}
		}
	}
}

// sealDoc applies edit to a plan document and seals it again: it sets
// to from toModel, and expanded from expandedModel when the plan has one,
// when setTo, and hash from the content. The result is indented.
func sealDoc(t testing.TB, doc []byte, edit func(plan map[string]any), setTo bool) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var plan map[string]any
	if err := dec.Decode(&plan); err != nil {
		t.Fatal(err)
	}
	edit(plan)
	if setTo {
		raw, err := json.Marshal(plan["toModel"])
		if err != nil {
			t.Fatal(err)
		}
		model, err := migrate.ReadModel(raw)
		if err != nil {
			t.Fatal(err)
		}
		plan["to"] = model.Hash
		if between, ok := plan["expandedModel"]; ok {
			raw, err := json.Marshal(between)
			if err != nil {
				t.Fatal(err)
			}
			model, err := migrate.ReadModel(raw)
			if err != nil {
				t.Fatal(err)
			}
			plan["expanded"] = model.Hash
		}
	}
	delete(plan, "hash")
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := migrate.PlanHash(raw)
	if err != nil {
		t.Fatal(err)
	}
	plan["hash"] = hash
	out, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// fixture reads testdata/<dialect>/<name>.plan.json.
func fixture(t testing.TB, dialect migrate.Dialect, name string) []byte {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("testdata", string(dialect), name+".plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// readPlan reads a plan document and fails the test on a refusal.
func readPlan(t testing.TB, doc []byte) *migrate.Plan {
	t.Helper()
	plan, err := migrate.ReadPlan(doc)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// plan reads a fixture plan.
func plan(t testing.TB, dialect migrate.Dialect, name string) *migrate.Plan {
	t.Helper()
	return readPlan(t, fixture(t, dialect, name))
}

// edited returns a fixture edited and sealed again, with to recomputed.
func edited(t testing.TB, dialect migrate.Dialect, name string, edit func(plan map[string]any)) *migrate.Plan {
	t.Helper()
	return readPlan(t, sealDoc(t, fixture(t, dialect, name), edit, true))
}

// steps returns a plan document's steps for editing.
func steps(plan map[string]any) []any { return plan["steps"].([]any) }

// forEachBackend runs test once per backend: on SQLite and a fake D1
// always, and on Postgres when testdb.EnvURL is set.
func forEachBackend(t *testing.T, test func(t *testing.T, db testdb.Backend)) {
	for _, db := range testdb.Backends {
		t.Run(string(db), func(t *testing.T) { test(t, db) })
	}
}

// evolveFixture names the fixture a test applies as the chain's second
// plan. D1 cannot run 02-evolve, whose rebuild turns foreign keys off, so on
// D1 it is evolve-fk-on: the same models, with the rebuild D27's amendment
// makes, of customer together with order, which references it.
func evolveFixture(db testdb.Backend) string {
	if db == testdb.D1 {
		return "evolve-fk-on"
	}
	return "02-evolve"
}

// skipForeignKeysOff skips a test on D1 when one of plans has a step that
// turns foreign keys off, which the D1 driver refuses. Such plans predate
// D27's amendment, and the planner's change that stops writing them lands
// on its own.
func skipForeignKeysOff(t *testing.T, db testdb.Backend, plans ...*migrate.Plan) {
	t.Helper()
	if db != testdb.D1 {
		return
	}
	for _, p := range plans {
		for _, step := range p.Steps {
			if step.ForeignKeysOff {
				t.Skipf("step %d of plan %s turns foreign keys off, which D1 cannot run; plans written before D27's amendment rebuild this way", step.Index, p.Hash)
			}
		}
	}
}

// openDriver opens a driver on url for the test. timeout is the lock or
// busy timeout; zero is the driver's default. A D1 driver reaches the fake
// server testdb started for url.
func openDriver(t testing.TB, url string, timeout time.Duration) migrate.Driver {
	t.Helper()
	ctx := context.Background()
	if d1.IsURL(url) {
		d, err := d1.Open(ctx, url, testdb.D1Options(t, url))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close(ctx) })
		return d
	}
	if migrate.URLDialect(url) == migrate.Postgres {
		d, err := postgres.Open(ctx, url, postgres.Options{LockTimeout: timeout})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close(ctx) })
		return d
	}
	d, err := sqlite.Open(ctx, url, sqlite.Options{BusyTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close(ctx) })
	return d
}

// newRunner returns a runner on its own connection to url, logging to the
// test, whose lock timeout retries take 10ms.
func newRunner(t testing.TB, url string) *migrate.Runner {
	t.Helper()
	return &migrate.Runner{
		Driver: openDriver(t, url, 0),
		Log:    &testLog{t: t},
		Waits:  []time.Duration{10 * time.Millisecond, 10 * time.Millisecond},
	}
}

// apply applies plan in phase and fails the test on an error.
func apply(t testing.TB, r *migrate.Runner, plan *migrate.Plan, phase migrate.Phase) *migrate.Result {
	t.Helper()
	result, err := r.Apply(context.Background(), plan, phase)
	if err != nil {
		t.Fatalf("apply %s: %v", plan.Hash, err)
	}
	return result
}

// status reads a service's status.
func status(t testing.TB, r *migrate.Runner, service string) *migrate.Status {
	t.Helper()
	st, err := r.Status(context.Background(), service)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// testLog writes the runner's lines to the test log and keeps them.
type testLog struct {
	t     testing.TB
	mu    sync.Mutex
	lines []string
}

func (l *testLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := strings.TrimRight(string(p), "\n")
	l.lines = append(l.lines, line)
	l.t.Log(line)
	return len(p), nil
}

func (l *testLog) contains(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}

// seed writes two customers and three orders into a database at 01.
func seed(t testing.TB, url string) {
	t.Helper()
	testdb.Exec(t, url,
		`INSERT INTO customer (id, name) VALUES (1, 'Ada'), (2, 'Grace')`,
		`INSERT INTO "order" (id, customer_id, total) VALUES (1, 1, 100), (2, 1, 200), (3, 2, 300)`,
	)
}

// count returns the rows of table.
func count(t testing.TB, url, table string) string {
	t.Helper()
	return testdb.Strings(t, url, `SELECT count(*) FROM `+table)[0]
}

func ints(from, to int) []int {
	var out []int
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
