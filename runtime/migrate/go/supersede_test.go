package migrate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
)

// refused applies p in phase and fails the test unless the runner refuses
// it with every one of want in its message.
func refused(t *testing.T, r *migrate.Runner, p *migrate.Plan, phase migrate.Phase, want ...string) {
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

// fromCreate reads a fixture outside the chain that starts from 01's model.
func fromCreate(t *testing.T, dialect migrate.Dialect) *migrate.Plan {
	t.Helper()
	name := "unique-index"
	if dialect == migrate.SQLite {
		name = "fk-violation"
	}
	p := plan(t, dialect, name)
	if p.From != plan(t, dialect, "01-create").To {
		t.Fatalf("%s does not start from 01's model", name)
	}
	return p
}

// TestSupersede: once a plan's expand steps have finished, the database is
// at its expanded model (D27, amended). A plan from that model supersedes
// the pending contract: the runner says so, runs the new plan and never the
// old contract, which it refuses afterwards. A plan from the old plan's from
// is refused while the database holds the expanded model, and so is a plan
// from it once the old contract has started.
func TestSupersede(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect migrate.Dialect) {
		create, evolve, supersede := plan(t, dialect, "01-create"), plan(t, dialect, "02-evolve"), plan(t, dialect, "supersede-02")
		if evolve.Expanded == "" || supersede.From != evolve.Expanded {
			t.Fatal("supersede-02 does not start from 02-evolve's expanded model")
		}

		t.Run("a plan from the expanded model", func(t *testing.T) {
			url := testdb.New(t, dialect)
			r := newRunner(t, url)
			apply(t, r, create, migrate.All)
			seed(t, url)
			apply(t, r, evolve, migrate.Expand)

			// The database is at the expanded model, so a plan from the
			// model 02 started from is refused.
			refused(t, r, fromCreate(t, dialect), migrate.All, "has plan "+evolve.Hash+" in progress", "expand steps are done")

			result := apply(t, r, supersede, migrate.All)
			if result.Superseded != evolve.Hash || !result.Finished || !equalInts(result.Ran, ints(1, len(supersede.Steps))) {
				t.Fatalf("the superseding plan: %+v", result)
			}
			if !r.Log.(*testLog).contains("plan " + supersede.Hash + " supersedes plan " + evolve.Hash) {
				t.Fatal("the runner did not say which plan the new one supersedes")
			}
			st := status(t, r, "shop")
			if st.ModelHash != supersede.To || st.PlanHash != "" || st.PlanPhase != "" {
				t.Fatalf("status after the superseding plan: %+v", st)
			}
			// 02's contract never ran: customer.name keeps its rows.
			if got := testdb.Strings(t, url, `SELECT name FROM customer WHERE id = 1`); len(got) != 1 || got[0] != "Ada" {
				t.Fatalf("customer.name after the superseding plan: %v", got)
			}
			if got := testdb.Strings(t, url, `SELECT count(*) FROM "order"`); got[0] != "3" {
				t.Fatalf("orders after the superseding plan: %v", got)
			}

			// The superseded contract is no longer in progress.
			refused(t, r, evolve, migrate.Contract, "the plan starts from model "+evolve.From, "service shop is at model "+supersede.To)
			refused(t, r, evolve, migrate.All, "the plan starts from model "+evolve.From)
			if st := status(t, r, "shop"); st.ModelHash != supersede.To || st.PlanHash != "" {
				t.Fatalf("a refusal changed the state: %+v", st)
			}
		})

		t.Run("contract after expand", func(t *testing.T) {
			url := testdb.New(t, dialect)
			r := newRunner(t, url)
			apply(t, r, create, migrate.All)
			apply(t, r, evolve, migrate.Expand)
			result := apply(t, r, evolve, migrate.Contract)
			if !result.Finished || result.Superseded != "" {
				t.Fatalf("contract: %+v", result)
			}
			if st := status(t, r, "shop"); st.ModelHash != evolve.To || st.PlanHash != "" {
				t.Fatalf("status after contract: %+v", st)
			}
			// The plan from the expanded model is now from the wrong model.
			refused(t, r, supersede, migrate.All, "the plan starts from model "+evolve.Expanded, "service shop is at model "+evolve.To)
		})

		t.Run("a superseded plan whose expand had nothing to do", func(t *testing.T) {
			// A plan with only contract steps: between its phases the
			// database is still at its from. A plan back to that model
			// supersedes it and leaves the database at the old plan's from
			// again, so only the rule that --phase contract runs the plan in
			// progress keeps a stale contract job from dropping what the
			// servers use.
			var createModel any
			dec := json.NewDecoder(bytes.NewReader(create.ToModel))
			dec.UseNumber()
			if err := dec.Decode(&createModel); err != nil {
				t.Fatal(err)
			}
			contractOnly := edited(t, dialect, "02-evolve", func(p map[string]any) {
				var kept []any
				for _, s := range steps(p) {
					if step := s.(map[string]any); step["phase"] == "contract" {
						step["index"] = len(kept) + 1
						kept = append(kept, step)
					}
				}
				p["steps"] = kept
				p["expandedModel"] = createModel
			})
			back := edited(t, dialect, "01-create", func(p map[string]any) {
				p["from"] = create.To
				p["steps"] = []any{}
			})
			if contractOnly.Expanded != create.To || back.From != create.To || back.To != create.To {
				t.Fatal("the edited plans do not start and end at 01's model")
			}

			url := testdb.New(t, dialect)
			r := newRunner(t, url)
			apply(t, r, create, migrate.All)
			apply(t, r, contractOnly, migrate.Expand)
			if st := status(t, r, "shop"); st.PlanHash != contractOnly.Hash || st.ModelHash != create.To {
				t.Fatalf("status after an expand with no steps: %+v", st)
			}
			if result := apply(t, r, back, migrate.All); result.Superseded != contractOnly.Hash {
				t.Fatalf("the plan back to 01's model: %+v", result)
			}
			refused(t, r, contractOnly, migrate.Contract, "is not in progress", "--phase expand first")
			if st := status(t, r, "shop"); st.ModelHash != create.To || st.PlanHash != "" {
				t.Fatalf("a refused contract changed the state: %+v", st)
			}
		})

		t.Run("a contract that has started", func(t *testing.T) {
			url := testdb.New(t, dialect)
			r := newRunner(t, url)
			apply(t, r, create, migrate.All)
			// 02 with a last contract step that fails: its other contract
			// steps run, and the plan stays in progress.
			failing := edited(t, dialect, "02-evolve", func(p map[string]any) {
				p["steps"] = append(steps(p), map[string]any{
					"index": len(steps(p)) + 1, "phase": "contract", "op": "dropTable", "subject": "table/missing",
					"statements": []any{"DROP TABLE missing"}, "transactional": true,
				})
			})
			if failing.Expanded != evolve.Expanded {
				t.Fatal("the edited plan has another expanded model")
			}
			var stepErr *migrate.StepError
			if _, err := r.Apply(context.Background(), failing, migrate.All); !errors.As(err, &stepErr) || stepErr.Index != len(failing.Steps) {
				t.Fatalf("apply = %v, want the last step to fail", err)
			}
			if st := status(t, r, "shop"); st.PlanHash != failing.Hash || st.ModelHash != evolve.Expanded {
				t.Fatalf("status after the failure: %+v", st)
			}
			refused(t, r, supersede, migrate.All, "has plan "+failing.Hash+" in progress, and its contract has started")
		})
	})
}

// TestAPlanWithoutExpanded: a plan written before plans carried their
// expanded model keeps the rule from before it (D27, amended). Its expand
// steps leave the applied model at its from, and every other plan is
// refused until its contract runs.
func TestAPlanWithoutExpanded(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect migrate.Dialect) {
		url := testdb.New(t, dialect)
		r := newRunner(t, url)
		create := plan(t, dialect, "01-create")
		old := edited(t, dialect, "02-evolve", func(p map[string]any) {
			delete(p, "expanded")
			delete(p, "expandedModel")
		})
		if old.Expanded != "" || old.BetweenPhases() != nil {
			t.Fatal("the edited plan still has an expanded model")
		}
		apply(t, r, create, migrate.All)
		apply(t, r, old, migrate.Expand)
		st := status(t, r, "shop")
		if st.PlanHash != old.Hash || st.PlanPhase != migrate.PlanPhaseExpand || st.ModelHash != create.To {
			t.Fatalf("status between the phases: %+v", st)
		}
		// The applied model is the plan's from, which the database no
		// longer holds: a plan from it is refused, not run.
		refused(t, r, fromCreate(t, dialect), migrate.All, "has plan "+old.Hash+" in progress", "expand steps are done")
		refused(t, r, plan(t, dialect, "supersede-02"), migrate.All, "has plan "+old.Hash+" in progress")

		result := apply(t, r, old, migrate.Contract)
		if !result.Finished || result.Superseded != "" {
			t.Fatalf("contract: %+v", result)
		}
		if st := status(t, r, "shop"); st.ModelHash != old.To || st.PlanHash != "" {
			t.Fatalf("status after contract: %+v", st)
		}
	})
}
