package sqlmigrate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
)

// indentedJSON is canonical JSON indented for review: object keys sorted,
// two spaces, a trailing newline.
func indentedJSON(t *testing.T, canonical []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := json.Indent(&out, canonical, "", "  "); err != nil {
		t.Fatal(err)
	}
	out.WriteByte('\n')
	return out.Bytes()
}

// checkGolden compares got with the golden file at path, or rewrites it
// with -update.
func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to write it): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from golden (run with -update to accept):\n%s", path, got)
	}
}

// TestPlanGoldens plans every case of planCases and compares the plan with
// testdata/plans/<case>.json. The golden leaves out toModel, the new
// model's canonical JSON, which the test checks against the model instead;
// its hash is the plan's to. Regenerate with:
// go test ./internal/sqlmigrate -run TestPlanGoldens -update
func TestPlanGoldens(t *testing.T) {
	for _, pc := range planCases {
		t.Run(pc.name, func(t *testing.T) {
			_, to := pc.models(t)
			plan := pc.plan(t)

			wantModel, err := to.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(plan.ToModel, wantModel) {
				t.Errorf("toModel is not the new model's canonical JSON")
			}
			if hash, _ := to.Hash(); plan.To != hash {
				t.Errorf("to = %s, want the new model's hash %s", plan.To, hash)
			}
			sealed := *plan
			if err := sealed.Seal(); err != nil || sealed.Hash != plan.Hash {
				t.Errorf("the plan is not sealed")
			}
			for i, step := range plan.Steps {
				if step.Index != i+1 {
					t.Errorf("step %d has index %d", i+1, step.Index)
				}
			}

			plan.ToModel = nil
			canonical, err := plan.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, filepath.Join("testdata", "plans", pc.name+".json"), indentedJSON(t, canonical))
		})
	}
}

// modelFixtures are the services whose models have goldens: sqlgen's
// golden fixtures and the plan cases' base.
var modelFixtures = []string{
	filepath.Join(sqlgenFixtures, "fixture-db"),
	filepath.Join(sqlgenFixtures, "fixture-list-defaults-db"),
	filepath.Join(sqlgenFixtures, "fixture-nested-arrays-db"),
	filepath.Join(sqlgenFixtures, "fixture-optimistic-db"),
	filepath.Join(sqlgenFixtures, "fixture-projection"),
	filepath.Join(sqlgenFixtures, "fixture-version-graph-db"),
	shopDir,
}

// TestModelGoldens builds the model of each fixture and compares it with
// testdata/models/<service>.json. Regenerate with:
// go test ./internal/sqlmigrate -run TestModelGoldens -update
func TestModelGoldens(t *testing.T) {
	for _, dir := range modelFixtures {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			schema, err := loader.LoadService(dir)
			if err != nil {
				t.Fatal(err)
			}
			model, err := BuildModel(schema, sqlgen.Options{SchemaName: name}, Postgres)
			if err != nil {
				t.Fatal(err)
			}
			again, err := BuildModel(schema, sqlgen.Options{SchemaName: name}, Postgres)
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := model.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if second, _ := again.CanonicalJSON(); !bytes.Equal(canonical, second) {
				t.Errorf("two builds of the model differ")
			}
			checkGolden(t, filepath.Join("testdata", "models", name+".json"), indentedJSON(t, canonical))
		})
	}
}
