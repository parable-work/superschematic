package migrate_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
)

// TestApplyFromAnEmptyDatabase: the first plan runs every step on an empty
// database, records its model, and a second apply of it runs nothing and
// says the plan is applied.
func TestApplyFromAnEmptyDatabase(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect migrate.Dialect) {
		url := testdb.New(t, dialect)
		r := newRunner(t, url)
		create := plan(t, dialect, "01-create")

		result := apply(t, r, create, migrate.All)
		if result.AlreadyApplied || !result.Finished || !equalInts(result.Ran, ints(1, len(create.Steps))) {
			t.Fatalf("first apply: %+v", result)
		}
		st := status(t, r, "shop")
		if st.ModelHash != create.To || string(st.Model) != string(create.Model().Canonical) || st.PlanHash != "" || st.Dialect != dialect {
			t.Fatalf("status after the plan: %+v", st)
		}
		seed(t, url)

		again := apply(t, r, create, migrate.All)
		if !again.AlreadyApplied || len(again.Ran) != 0 {
			t.Fatalf("second apply: %+v", again)
		}
		if log := r.Log.(*testLog); !log.contains("is already applied") {
			t.Fatal("the second apply did not say the plan is applied")
		}
		if got := count(t, url, `"order"`); got != "3" {
			t.Fatalf("orders after a second apply: %s", got)
		}
	})
}

// TestApplyAChain: plans apply one after another, each from the previous
// one's model; rows survive every step that does not drop them; a plan
// taken back and forth (A to B, B to A, A to B again) runs again.
func TestApplyAChain(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect migrate.Dialect) {
		url := testdb.New(t, dialect)
		r := newRunner(t, url)
		create, evolve, audit, dropAudit := plan(t, dialect, "01-create"), plan(t, dialect, "02-evolve"),
			plan(t, dialect, "03-audit"), plan(t, dialect, "04-drop-audit")

		apply(t, r, create, migrate.All)
		seed(t, url)
		apply(t, r, evolve, migrate.All)
		if got := count(t, url, `"order"`); got != "3" {
			t.Fatalf("orders after 02: %s", got)
		}
		if got := count(t, url, "customer"); got != "2" {
			t.Fatalf("customers after 02: %s", got)
		}
		apply(t, r, audit, migrate.All)
		if st := status(t, r, "shop"); st.ModelHash != audit.To {
			t.Fatalf("model after 03 is %s, want %s", st.ModelHash, audit.To)
		}

		// Back to 02's model, then 03 again: the same plan, so the same
		// hash, whose earlier log rows must not make it skip its steps.
		if dropAudit.To != evolve.To || dropAudit.From != audit.To {
			t.Fatal("04 does not take 03's model back to 02's")
		}
		apply(t, r, dropAudit, migrate.All)
		result := apply(t, r, audit, migrate.All)
		if !equalInts(result.Ran, []int{1}) || !result.Finished {
			t.Fatalf("03 again: %+v", result)
		}
		if got := count(t, url, "audit"); got != "0" {
			t.Fatalf("audit rows: %s", got)
		}
	})
}

// TestResumeAfterAFailure: a failure injected after any step of any plan of
// the chain leaves the plan in progress with the steps before it logged;
// the next apply runs only the rest, and the database ends with the same
// catalog as one that never failed.
func TestResumeAfterAFailure(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect migrate.Dialect) {
		chain := []*migrate.Plan{plan(t, dialect, "01-create"), plan(t, dialect, "02-evolve"), plan(t, dialect, "03-audit")}
		resumeAfterEveryStep(t, dialect, chain)
	})
}

// resumeAfterEveryStep applies chain once straight through, then once per
// step of every plan with a failure injected after that step, and compares
// each final catalog with the first.
func resumeAfterEveryStep(t *testing.T, dialect migrate.Dialect, chain []*migrate.Plan) {
	reference := testdb.New(t, dialect)
	r := newRunner(t, reference)
	for _, p := range chain {
		apply(t, r, p, migrate.All)
	}
	want := testdb.Catalog(t, reference)
	last := chain[len(chain)-1]

	injected := errors.New("injected")
	for i, p := range chain {
		for k := 1; k <= len(p.Steps); k++ {
			t.Run(fmt.Sprintf("plan %d step %d", i+1, k), func(t *testing.T) {
				url := testdb.New(t, dialect)
				r := newRunner(t, url)
				for _, before := range chain[:i] {
					apply(t, r, before, migrate.All)
				}
				r.AfterStep = func(_ context.Context, step *migrate.Step) error {
					if step.Index == k {
						return injected
					}
					return nil
				}
				if _, err := r.Apply(context.Background(), p, migrate.All); !errors.Is(err, injected) {
					t.Fatalf("apply with a failure after step %d: %v", k, err)
				}
				r.AfterStep = nil
				st := status(t, r, p.Service)
				if k < len(p.Steps) {
					if st.PlanHash != p.Hash || len(st.Steps) != k {
						t.Fatalf("after the failure: plan %q with %d steps logged, want %s with %d", st.PlanHash, len(st.Steps), p.Hash, k)
					}
					result := apply(t, r, p, migrate.All)
					if !equalInts(result.Ran, ints(k+1, len(p.Steps))) || !result.Finished {
						t.Fatalf("resume ran %v, want %v", result.Ran, ints(k+1, len(p.Steps)))
					}
				} else {
					if st.PlanHash != "" || st.ModelHash != p.To {
						t.Fatalf("after a failure past the last step: %+v", st)
					}
					if result := apply(t, r, p, migrate.All); !result.AlreadyApplied {
						t.Fatalf("apply after the last step: %+v", result)
					}
				}
				for _, after := range chain[i+1:] {
					apply(t, r, after, migrate.All)
				}
				if st := status(t, r, last.Service); st.ModelHash != last.To {
					t.Fatalf("final model %s, want %s", st.ModelHash, last.To)
				}
				if got := testdb.Catalog(t, url); !slices.Equal(got, want) {
					t.Fatalf("catalog after resuming differs:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
				}
			})
		}
	}
}

// TestAFailedStepNamesItself: a statement that fails stops the run with an
// error naming the step's index, subject and statement; the plan stays in
// progress at that step and the steps before it stay done.
func TestAFailedStepNamesItself(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect migrate.Dialect) {
		url := testdb.New(t, dialect)
		r := newRunner(t, url)
		broken := edited(t, dialect, "01-create", func(p map[string]any) {
			p["steps"] = append(steps(p), map[string]any{
				"index": len(steps(p)) + 1, "phase": "expand", "op": "createIndex", "subject": "table/missing/index/missing_idx",
				"statements": []any{"CREATE INDEX missing_idx ON missing (id)"}, "transactional": true,
			})
		})
		_, err := r.Apply(context.Background(), broken, migrate.All)
		var stepErr *migrate.StepError
		if !errors.As(err, &stepErr) {
			t.Fatalf("apply = %v, want a StepError", err)
		}
		last := len(broken.Steps)
		if stepErr.Index != last || stepErr.Subject != "table/missing/index/missing_idx" || stepErr.Statement != "CREATE INDEX missing_idx ON missing (id)" {
			t.Fatalf("StepError %+v", stepErr)
		}
		for _, want := range []string{fmt.Sprintf("step %d", last), "table/missing/index/missing_idx", "CREATE INDEX missing_idx ON missing (id)"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q does not name %q", err, want)
			}
		}
		st := status(t, r, "shop")
		if st.PlanHash != broken.Hash || len(st.Steps) != last-1 || st.ModelHash != "" {
			t.Fatalf("status after the failure: %+v", st)
		}
		// The step's log row is the SHA-256 of its statements joined by
		// "\n;\n".
		sum := sha256.Sum256([]byte(strings.Join(broken.Steps[0].Statements, "\n;\n")))
		if st.Steps[0].SQLHash != hex.EncodeToString(sum[:]) || st.Steps[0].Subject != "table/customer" || st.Steps[0].FinishedAt == "" {
			t.Fatalf("log row of step 1: %+v", st.Steps[0])
		}
	})
}

// TestSQLHashOfManyStatements: a step of two statements (Postgres's 03)
// logs the SHA-256 of both joined by "\n;\n" (computed with shasum).
func TestSQLHashOfManyStatements(t *testing.T) {
	url := testdb.Postgres(t)
	r := newRunner(t, url)
	apply(t, r, plan(t, migrate.Postgres, "01-create"), migrate.All)
	apply(t, r, plan(t, migrate.Postgres, "02-evolve"), migrate.All)
	audit := plan(t, migrate.Postgres, "03-audit")
	apply(t, r, audit, migrate.All)
	got := testdb.Strings(t, url, `SELECT sql_hash FROM superschematic_migrations WHERE plan_hash = $1 AND step = 1`, audit.Hash)
	if len(got) != 1 || got[0] != "b864766961fbd3daa5186c734fd09c7efa86c5d8c6dbe904ee614fb557506104" {
		t.Fatalf("sql_hash %v", got)
	}
}

// TestApplyAStepWithNoStatements: a step with an empty statements list,
// such as the one a version graph's content change gets, runs nothing, and
// the runner logs it with the SHA-256 of no SQL.
func TestApplyAStepWithNoStatements(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect migrate.Dialect) {
		url := testdb.New(t, dialect)
		r := newRunner(t, url)
		p := edited(t, dialect, "01-create", func(p map[string]any) {
			p["steps"] = append(steps(p), map[string]any{
				"index": len(steps(p)) + 1, "phase": "expand", "op": "changeGraphContent", "subject": "table/order",
				"statements": []any{}, "transactional": true,
			})
		})
		last := len(p.Steps)
		result := apply(t, r, p, migrate.All)
		if !result.Finished || !equalInts(result.Ran, ints(1, last)) {
			t.Fatalf("apply: %+v", result)
		}
		if st := status(t, r, "shop"); st.ModelHash != p.To || st.PlanHash != "" {
			t.Fatalf("status after the plan: %+v", st)
		}
		if log := r.Log.(*testLog); !log.contains(fmt.Sprintf("step %d/%d expand table/order: done", last, last)) {
			t.Error("the runner did not log the step")
		}
		empty := sha256.Sum256(nil)
		got := testdb.Strings(t, url, `SELECT sql_hash FROM superschematic_migrations WHERE plan_hash = $1 AND step = $2`, p.Hash, last)
		if len(got) != 1 || got[0] != hex.EncodeToString(empty[:]) {
			t.Fatalf("sql_hash %v", got)
		}
	})
}

// TestTwoRunnersSerialize: two runners applying one plan at once never run
// a step twice. On Postgres the second waits for the advisory lock until
// the first is done and finds the plan applied, also across a CREATE INDEX
// CONCURRENTLY (02), which waits for every older snapshot; on SQLite the two
// take turns per step and split the steps between them.
func TestTwoRunnersSerialize(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect migrate.Dialect) {
		url := testdb.New(t, dialect)
		first, second := newRunner(t, url), newRunner(t, url)
		for _, name := range []string{"01-create", "02-evolve"} {
			twoRunners(t, dialect, url, plan(t, dialect, name), first, second)
		}
	})
}

// twoRunners applies p with first and second at once: second starts while
// first, which has run step 1, pauses.
func twoRunners(t *testing.T, dialect migrate.Dialect, url string, p *migrate.Plan, first, second *migrate.Runner) {
	t.Helper()
	started := make(chan struct{})
	first.AfterStep = func(_ context.Context, step *migrate.Step) error {
		if step.Index == 1 {
			close(started)
			time.Sleep(300 * time.Millisecond)
		}
		return nil
	}
	defer func() { first.AfterStep = nil }()
	var wg sync.WaitGroup
	var firstResult, secondResult *migrate.Result
	var firstErr, secondErr error
	var firstDone, secondDone time.Time
	wg.Add(2)
	go func() {
		defer wg.Done()
		firstResult, firstErr = first.Apply(context.Background(), p, migrate.All)
		firstDone = time.Now()
	}()
	go func() {
		defer wg.Done()
		<-started
		secondResult, secondErr = second.Apply(context.Background(), p, migrate.All)
		secondDone = time.Now()
	}()
	wg.Wait()
	if firstErr != nil || secondErr != nil {
		t.Fatalf("plan %s: first: %v; second: %v", p.Hash, firstErr, secondErr)
	}
	ran := append(append([]int{}, firstResult.Ran...), secondResult.Ran...)
	slices.Sort(ran)
	if !equalInts(ran, ints(1, len(p.Steps))) {
		t.Fatalf("plan %s: steps ran %v and %v", p.Hash, firstResult.Ran, secondResult.Ran)
	}
	if !firstResult.Finished || !secondResult.Finished {
		t.Fatalf("plan %s: results %+v and %+v", p.Hash, firstResult, secondResult)
	}
	if dialect == migrate.Postgres && (!secondResult.AlreadyApplied || secondDone.Before(firstDone)) {
		t.Fatalf("plan %s: the second runner did not wait for the first: %+v", p.Hash, secondResult)
	}
	logged := testdb.Strings(t, url, `SELECT count(*) FROM superschematic_migrations WHERE plan_hash = $1`, p.Hash)
	if logged[0] != fmt.Sprint(len(p.Steps)) {
		t.Fatalf("plan %s: %s log rows", p.Hash, logged[0])
	}
	if st := status(t, first, "shop"); st.ModelHash != p.To || st.PlanHash != "" {
		t.Fatalf("status %+v", st)
	}
}

// TestRefusals: the runner refuses, and changes nothing, when the database
// is not at the plan's from, when another plan is in progress, when contract
// is asked for before expand has run, and when the plan, the database or
// the recorded state are of different dialects.
func TestRefusals(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect migrate.Dialect) {
		create, evolve, audit := plan(t, dialect, "01-create"), plan(t, dialect, "02-evolve"), plan(t, dialect, "03-audit")
		refused := func(t *testing.T, r *migrate.Runner, p *migrate.Plan, phase migrate.Phase, want ...string) {
			t.Helper()
			_, err := r.Apply(context.Background(), p, phase)
			if !errors.Is(err, migrate.ErrRefused) {
				t.Fatalf("apply = %v, want a refusal", err)
			}
			for _, w := range want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("%q does not say %q", err, w)
				}
			}
		}

		t.Run("the wrong baseline", func(t *testing.T) {
			url := testdb.New(t, dialect)
			r := newRunner(t, url)
			refused(t, r, evolve, migrate.All, "the plan starts from model "+evolve.From, "service shop has no applied model", "status --model")
			apply(t, r, create, migrate.All)
			refused(t, r, audit, migrate.All, "the plan starts from model "+audit.From, "service shop is at model "+create.To)
			if st := status(t, r, "shop"); st.PlanHash != "" || st.ModelHash != create.To {
				t.Fatalf("a refusal changed the state: %+v", st)
			}
		})

		t.Run("another plan in progress", func(t *testing.T) {
			url := testdb.New(t, dialect)
			r := newRunner(t, url)
			apply(t, r, create, migrate.All)
			apply(t, r, evolve, migrate.Expand)
			refused(t, r, audit, migrate.All, "has plan "+evolve.Hash+" in progress", "expand steps are done")
		})

		t.Run("contract before expand", func(t *testing.T) {
			url := testdb.New(t, dialect)
			r := newRunner(t, url)
			apply(t, r, create, migrate.All)
			refused(t, r, evolve, migrate.Contract, "expand step 1 has not run", "--phase expand first")
			if st := status(t, r, "shop"); st.PlanHash != "" {
				t.Fatalf("a refused contract recorded the plan: %+v", st)
			}
			// Part-way through expand is still before contract.
			r.AfterStep = func(_ context.Context, step *migrate.Step) error {
				if step.Index == 1 {
					return errors.New("stop")
				}
				return nil
			}
			_, _ = r.Apply(context.Background(), evolve, migrate.Expand)
			r.AfterStep = nil
			refused(t, r, evolve, migrate.Contract, "expand step 2 has not run")
		})

		t.Run("a plan for another dialect", func(t *testing.T) {
			other := migrate.SQLite
			if dialect == migrate.SQLite {
				other = migrate.Postgres
			}
			r := newRunner(t, testdb.New(t, dialect))
			refused(t, r, plan(t, other, "01-create"), migrate.All, "the plan is for "+string(other)+" and the database is "+string(dialect))
		})

		t.Run("state recorded for another dialect", func(t *testing.T) {
			url := testdb.New(t, dialect)
			r := newRunner(t, url)
			apply(t, r, create, migrate.All)
			testdb.Exec(t, url, `UPDATE superschematic_schema_state SET dialect = 'elsewhere'`)
			refused(t, r, evolve, migrate.All, "records dialect elsewhere")
		})
	})
}

// TestPhases: --phase expand runs the expand steps and records the phase;
// the contract-only objects stay until --phase contract runs the rest and
// records the plan's model.
func TestPhases(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect migrate.Dialect) {
		url := testdb.New(t, dialect)
		r := newRunner(t, url)
		create, evolve := plan(t, dialect, "01-create"), plan(t, dialect, "02-evolve")
		apply(t, r, create, migrate.All)
		seed(t, url)
		expandSteps := 0
		for _, step := range evolve.Steps {
			if step.Phase == migrate.Expand {
				expandSteps++
			}
		}

		result := apply(t, r, evolve, migrate.Expand)
		if !result.ExpandDone || result.Finished || !equalInts(result.Ran, ints(1, expandSteps)) {
			t.Fatalf("expand: %+v", result)
		}
		st := status(t, r, "shop")
		if st.PlanHash != evolve.Hash || st.PlanPhase != "expand" || st.ModelHash != create.To || len(st.Steps) != expandSteps {
			t.Fatalf("status between the phases: %+v", st)
		}
		// customer.name is dropped in contract, so it is still there.
		if got := testdb.Strings(t, url, `SELECT name FROM customer WHERE id = 1`); len(got) != 1 || got[0] != "Ada" {
			t.Fatalf("customer.name before contract: %v", got)
		}
		// Expand again runs nothing.
		if again := apply(t, r, evolve, migrate.Expand); len(again.Ran) != 0 || again.Finished {
			t.Fatalf("expand again: %+v", again)
		}

		result = apply(t, r, evolve, migrate.Contract)
		if !result.Finished || !equalInts(result.Ran, ints(expandSteps+1, len(evolve.Steps))) {
			t.Fatalf("contract: %+v", result)
		}
		st = status(t, r, "shop")
		if st.PlanHash != "" || st.PlanPhase != "" || st.ModelHash != evolve.To {
			t.Fatalf("status after contract: %+v", st)
		}
		if got := testdb.Strings(t, url, `SELECT count(*) FROM customer`); got[0] != "2" {
			t.Fatalf("customers after contract: %v", got)
		}
	})
}

// TestStatusChangesNothing: status on a database the runner never touched
// reports no state and creates no table.
func TestStatusChangesNothing(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect migrate.Dialect) {
		url := testdb.New(t, dialect)
		r := newRunner(t, url)
		if st := status(t, r, "shop"); st.Recorded || st.ModelHash != "" || st.Model != nil {
			t.Fatalf("status of an empty database: %+v", st)
		}
		if got := testdb.Catalog(t, url); dialect == migrate.SQLite && len(got) != 0 {
			t.Fatalf("status created %v", got)
		}
		query := `SELECT count(*) FROM sqlite_master WHERE name LIKE 'superschematic%'`
		if dialect == migrate.Postgres {
			query = `SELECT count(*) FROM pg_class WHERE relname LIKE 'superschematic%'`
		}
		if got := testdb.Strings(t, url, query); got[0] != "0" {
			t.Fatalf("status created %s state tables", got[0])
		}
	})
}

// TestAdopt: adopt records a model without running anything and prints the
// hash it replaces; a plan from that model then applies. It refuses while a
// plan is in progress and for a model of another dialect.
func TestAdopt(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect migrate.Dialect) {
		url := testdb.New(t, dialect)
		r := newRunner(t, url)
		create, evolve, audit := plan(t, dialect, "01-create"), plan(t, dialect, "02-evolve"), plan(t, dialect, "03-audit")

		// A database built by hand to 01's model.
		for _, step := range create.Steps {
			testdb.Exec(t, url, step.Statements...)
		}
		replaced, err := r.Adopt(context.Background(), create.Model())
		if err != nil || replaced != "" {
			t.Fatalf("adopt = %q, %v", replaced, err)
		}
		if st := status(t, r, "shop"); st.ModelHash != create.To || string(st.Model) != string(create.Model().Canonical) {
			t.Fatalf("status after adopt: %+v", st)
		}
		if result := apply(t, r, evolve, migrate.All); !result.Finished {
			t.Fatalf("apply after adopt: %+v", result)
		}

		// Adopting again replaces the model plans recorded.
		replaced, err = r.Adopt(context.Background(), create.Model())
		if err != nil || replaced != evolve.To {
			t.Fatalf("adopt over a plan's model = %q, %v; want %s", replaced, err, evolve.To)
		}

		// The database is at 02 again but the state says 01: adopt 02.
		replaced, err = r.Adopt(context.Background(), evolve.Model())
		if err != nil || replaced != create.To {
			t.Fatalf("adopt = %q, %v", replaced, err)
		}
		if result := apply(t, r, audit, migrate.All); !result.Finished {
			t.Fatalf("apply after adopt: %+v", result)
		}

		r.AfterStep = func(context.Context, *migrate.Step) error { return errors.New("stop") }
		_, _ = r.Apply(context.Background(), plan(t, dialect, "04-drop-audit"), migrate.All)
		if st := status(t, r, "shop"); st.ModelHash != evolve.To {
			t.Fatalf("04 did not finish: %+v", st)
		}
		if _, err := r.Adopt(context.Background(), create.Model()); err != nil {
			t.Fatal(err)
		}
		_, _ = r.Apply(context.Background(), edited(t, dialect, "02-evolve", func(p map[string]any) {
			// 02 again from a database that already has its objects:
			// stop it after step 1, which re-adds nothing, so it stays in
			// progress.
			steps(p)[0].(map[string]any)["statements"] = []any{"SELECT 1"}
		}), migrate.All)
		if _, err := r.Adopt(context.Background(), audit.Model()); !errors.Is(err, migrate.ErrRefused) || !strings.Contains(err.Error(), "in progress") {
			t.Fatalf("adopt during a plan = %v, want a refusal", err)
		}

		other := migrate.SQLite
		if dialect == migrate.SQLite {
			other = migrate.Postgres
		}
		if _, err := r.Adopt(context.Background(), plan(t, other, "01-create").Model()); !errors.Is(err, migrate.ErrRefused) {
			t.Fatalf("adopt of a %s model = %v, want a refusal", other, err)
		}
	})
}
