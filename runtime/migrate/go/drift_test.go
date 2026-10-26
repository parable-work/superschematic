package migrate_test

import (
	"context"
	"strings"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
)

// TestDrift (adopt --diff, D85): a database built to a model holds what
// it holds; one built to the previous model differs from the next one in
// what the plan between them changes; and a column, a table and an index
// changed by hand are each a line of their own, the runner's own tables
// never one. The database's state is never written.
func TestDrift(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db testdb.Backend) {
		if db == testdb.D1 {
			t.Skip("the D1 fake holds no catalog to read")
		}
		dialect := db.Dialect()
		url := testdb.New(t, db)
		r := newRunner(t, url)
		create, evolve := plan(t, dialect, "01-create"), plan(t, dialect, evolveFixture(db))
		for _, step := range create.Steps {
			testdb.Exec(t, url, step.Statements...)
		}
		drift := func(model *migrate.Model) []string {
			t.Helper()
			lines, err := r.Drift(context.Background(), model)
			if err != nil {
				t.Fatal(err)
			}
			return lines
		}
		if lines := drift(create.Model()); len(lines) != 0 {
			t.Fatalf("a database built to its model differs:\n%s", strings.Join(lines, "\n"))
		}
		if lines := drift(evolve.Model()); len(lines) == 0 {
			t.Fatal("a database built to 01 holds 02's model")
		}

		// The runner's tables are not the service's.
		if _, err := r.Adopt(context.Background(), create.Model()); err != nil {
			t.Fatal(err)
		}
		if lines := drift(create.Model()); len(lines) != 0 {
			t.Fatalf("the runner's state differs:\n%s", strings.Join(lines, "\n"))
		}

		testdb.Exec(t, url,
			`ALTER TABLE customer ADD COLUMN nickname TEXT`,
			`CREATE TABLE stray (id INTEGER PRIMARY KEY)`,
			`CREATE INDEX idx_customer_name_by_hand ON customer (name)`,
		)
		got := strings.Join(drift(create.Model()), "\n")
		for _, want := range []string{
			"column customer.nickname: the database holds it, and the model does not",
			"table stray: the database holds it, and the model does not",
			"index idx_customer_name_by_hand: the database holds it, and the model does not",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the drift does not say %q:\n%s", want, got)
			}
		}
		if st := status(t, r, "shop"); st.ModelHash != create.To {
			t.Fatalf("the diff changed the state: %+v", st)
		}
	})
}
