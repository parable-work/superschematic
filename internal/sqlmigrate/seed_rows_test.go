package sqlmigrate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestSeedRowsPlan plans fixture-seed-rows-db's seed rows (#625) in both
// dialects: the plan from an empty database seeds each row after its
// table; a plan from the model to itself has no step, so applying again
// changes nothing; a seed whose values change, its key kept, has no step,
// so it never overwrites an app's edit; and a new row is the plan's one
// step, in expand, with no hazard.
func TestSeedRowsPlan(t *testing.T) {
	dir := filepath.Join(sqlgenFixtures, "fixture-seed-rows-db")
	load := func(change func(*ir.Schema)) *ir.Schema {
		schema, err := loader.LoadService(dir)
		if err != nil {
			t.Fatal(err)
		}
		if change != nil {
			change(schema)
		}
		return schema
	}
	for _, dialect := range []Dialect{Postgres, SQLite} {
		t.Run(string(dialect), func(t *testing.T) {
			model := func(change func(*ir.Schema)) *Model {
				m, err := BuildModel(load(change), sqlgen.Options{SchemaName: "fixture-seed-rows-db"}, dialect)
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			base := model(nil)
			if len(base.Rows) != 7 {
				t.Fatalf("the model holds %d seed rows, want 7", len(base.Rows))
			}

			empty, err := Diff(nil, base, Options{})
			if err != nil {
				t.Fatal(err)
			}
			seeded, lastTable := 0, 0
			for i, step := range empty.Steps {
				switch step.Op {
				case string(opSeedRow):
					seeded++
					if i < lastTable {
						t.Errorf("step %d seeds %s before a table is created", step.Index, step.Subject)
					}
				case string(opCreateTable):
					lastTable = i
					if seeded > 0 {
						t.Errorf("step %d creates a table after a row is seeded", step.Index)
					}
				}
			}
			if seeded != 7 {
				t.Errorf("the plan from an empty database seeds %d rows, want 7", seeded)
			}

			same, err := Diff(base, model(nil), Options{})
			if err != nil {
				t.Fatal(err)
			}
			if len(same.Steps) != 0 {
				t.Errorf("the plan from the model to itself has %d steps", len(same.Steps))
			}

			changed, err := Diff(base, model(func(s *ir.Schema) {
				s.Types["Tier"].SeedRows.Rows[0]["title"] = "Free forever"
				s.Types["Reminder"].SeedRows.Rows[0]["leadMinutes"] = 45.0
			}), Options{})
			if err != nil {
				t.Fatal(err)
			}
			if len(changed.Steps) != 0 {
				t.Errorf("a seed whose values change has %d steps, want none: %s", len(changed.Steps), changed.Steps[0].Subject)
			}

			added, err := Diff(base, model(func(s *ir.Schema) {
				rows := &s.Types["Region"].SeedRows.Rows
				*rows = append(*rows, map[string]any{"code": "apac", "name": "Asia Pacific"})
			}), Options{})
			if err != nil {
				t.Fatal(err)
			}
			if len(added.Steps) != 1 {
				t.Fatalf("a new seed row plans %d steps, want 1", len(added.Steps))
			}
			step := added.Steps[0]
			if step.Op != string(opSeedRow) || step.Phase != Expand || step.Subject != `row/region/{"code":"apac"}` || len(step.Hazards) != 0 ||
				!strings.Contains(step.Statements[0], "WHERE NOT EXISTS") || !strings.Contains(step.Statements[0], "'apac'") {
				t.Errorf("the new row's step = %s %s %s %v:\n%s", step.Op, step.Phase, step.Subject, step.Hazards, strings.Join(step.Statements, "\n"))
			}
		})
	}
}
