package migrate_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
)

// vectorsDir holds the plans the compiler writes for the runner (D27,
// "Testing"): one directory per case, each with NN-<name>.plan.json files
// applied in name order on a fresh database, the first from an empty one.
// A plan starts from the model the plan before it ends at or, superseding
// that plan's contract, from the model it holds between its phases; then
// only the earlier plan's expand steps run (D27, amended).
var vectorsDir = filepath.Join("..", "testdata", "plans")

// TestCompilerVectors applies every case of the compiler's plan vectors on
// the dialect its plans name: the chain ends at the last plan's to, a second
// run of each plan does nothing, a plan from the model the one before it
// holds between its phases supersedes that plan's contract, a failure
// injected after any step resumes to the same catalog, two runners at once
// serialize, and the first plan is refused on a database at another model.
func TestCompilerVectors(t *testing.T) {
	entries, err := os.ReadDir(vectorsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	cases := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		paths, err := filepath.Glob(filepath.Join(vectorsDir, entry.Name(), "*.plan.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == 0 {
			continue
		}
		cases++
		sort.Strings(paths)
		t.Run(entry.Name(), func(t *testing.T) { compilerVector(t, paths) })
	}
	if cases == 0 {
		t.Skipf("no plan vectors under %s", vectorsDir)
	}
}

func compilerVector(t *testing.T, paths []string) {
	var chain []*migrate.Plan
	var phases []migrate.Phase // the phases each plan of the chain runs
	for i, path := range paths {
		doc, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		p, err := migrate.ReadPlan(doc)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		from := ""
		if i > 0 {
			from = chain[i-1].To
			if p.Dialect != chain[0].Dialect || p.Service != chain[0].Service {
				t.Fatalf("%s is for %s %s; the case's first plan for %s %s", path, p.Dialect, p.Service, chain[0].Dialect, chain[0].Service)
			}
			if expanded := chain[i-1].Expanded; expanded != "" && p.From == expanded {
				from, phases[i-1] = expanded, migrate.Expand
			}
		}
		if p.From != from {
			t.Fatalf("%s starts from %q, want %q", path, p.From, from)
		}
		chain = append(chain, p)
		phases = append(phases, migrate.All)
	}
	dialect := chain[0].Dialect
	last := chain[len(chain)-1]

	t.Run("applies", func(t *testing.T) {
		url := testdb.New(t, dialect)
		r := newRunner(t, url)
		for i, p := range chain {
			result := apply(t, r, p, phases[i])
			superseded := ""
			if i > 0 && phases[i-1] == migrate.Expand {
				superseded = chain[i-1].Hash
			}
			if result.Superseded != superseded {
				t.Fatalf("plan %s superseded %q, want %q", p.Hash, result.Superseded, superseded)
			}
			if phases[i] == migrate.Expand {
				// Expand only: the database holds the plan's expanded
				// model, and a second run of expand does nothing.
				if !result.ExpandDone || result.Finished || len(result.Ran) != len(stepsIn(p, migrate.Expand)) {
					t.Fatalf("plan %s, expand: %+v", p.Hash, result)
				}
				if st := status(t, r, p.Service); st.ModelHash != p.Expanded || st.PlanPhase != migrate.PlanPhaseExpanded {
					t.Fatalf("status between the phases of plan %s: %+v", p.Hash, st)
				}
				if again := apply(t, r, p, migrate.Expand); len(again.Ran) != 0 || again.Finished {
					t.Fatalf("plan %s, expand again: %+v", p.Hash, again)
				}
				continue
			}
			if !result.Finished || len(result.Ran) != len(p.Steps) {
				t.Fatalf("plan %s: %+v", p.Hash, result)
			}
			if again := apply(t, r, p, migrate.All); !again.AlreadyApplied || len(again.Ran) != 0 {
				t.Fatalf("plan %s again: %+v", p.Hash, again)
			}
		}
		if st := status(t, r, last.Service); st.ModelHash != last.To || st.PlanHash != "" {
			t.Fatalf("status %+v, want model %s", st, last.To)
		}
	})

	t.Run("resumes after a failure", func(t *testing.T) {
		resumeAfterEveryStep(t, dialect, chain, phases)
	})

	t.Run("two runners serialize", func(t *testing.T) {
		url := testdb.New(t, dialect)
		first, second := newRunner(t, url), newRunner(t, url)
		for i, p := range chain {
			var wg sync.WaitGroup
			errs := make([]error, 2)
			for j, r := range []*migrate.Runner{first, second} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, errs[j] = r.Apply(context.Background(), p, phases[i])
				}()
			}
			wg.Wait()
			if err := errors.Join(errs...); err != nil {
				t.Fatalf("plan %s: %v", p.Hash, err)
			}
			steps := len(p.Steps)
			if phases[i] == migrate.Expand {
				steps = len(stepsIn(p, migrate.Expand))
			}
			logged := testdb.Strings(t, url, `SELECT count(*) FROM superschematic_migrations WHERE plan_hash = $1`, p.Hash)
			if logged[0] != fmt.Sprint(steps) {
				t.Fatalf("plan %s: %s log rows for %d steps", p.Hash, logged[0], steps)
			}
		}
		if st := status(t, first, last.Service); st.ModelHash != last.To {
			t.Fatalf("model %s, want %s", st.ModelHash, last.To)
		}
	})

	t.Run("the wrong baseline is refused", func(t *testing.T) {
		url := testdb.New(t, dialect)
		r := newRunner(t, url)
		decoy, err := migrate.ReadModel([]byte(fmt.Sprintf(`{"version":1,"dialect":%q,"service":%q,"decoy":true}`, dialect, chain[0].Service)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Adopt(context.Background(), decoy); err != nil {
			t.Fatal(err)
		}
		for _, p := range chain {
			if _, err := r.Apply(context.Background(), p, migrate.All); !errors.Is(err, migrate.ErrRefused) {
				t.Fatalf("plan %s on a database at another model = %v, want a refusal", p.Hash, err)
			}
		}
	})
}

// stepsIn returns the plan's steps of phase.
func stepsIn(p *migrate.Plan, phase migrate.Phase) []*migrate.Step {
	var out []*migrate.Step
	for _, step := range p.Steps {
		if step.Phase == phase {
			out = append(out, step)
		}
	}
	return out
}
