package sqlmigrate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
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
// testdata/plans/<case>.json, and every case of sqlitePlanCases with
// testdata/plans/<case>.sqlite.json. The golden leaves out toModel, the
// new model's canonical JSON, which the test checks against the model
// instead; its hash is the plan's to. It leaves out expandedModel too, which
// the test checks is canonical and hashes to the plan's expanded, present
// exactly when the plan has contract steps (TestExpandedModel checks what it
// holds). Regenerate with:
// go test ./internal/sqlmigrate -run TestPlanGoldens -update
func TestPlanGoldens(t *testing.T) {
	cases := append(append([]planCase(nil), planCases...), sqlitePlanCases()...)
	for _, pc := range cases {
		t.Run(pc.golden(), func(t *testing.T) {
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
			contract := false
			for i, step := range plan.Steps {
				if step.Index != i+1 {
					t.Errorf("step %d has index %d", i+1, step.Index)
				}
				contract = contract || step.Phase == Contract
			}
			checkExpandedHash(t, plan, contract)

			plan.ToModel = nil
			plan.ExpandedModel = nil
			canonical, err := plan.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, filepath.Join("testdata", "plans", pc.golden()+".json"), indentedJSON(t, canonical))
		})
	}
}

// checkExpandedHash checks that a plan carries expanded and expandedModel
// exactly when it has contract steps, that expandedModel is canonical JSON,
// and that it hashes to expanded.
func checkExpandedHash(t *testing.T, plan *Plan, contract bool) {
	t.Helper()
	if !contract {
		if plan.Expanded != "" || plan.ExpandedModel != nil {
			t.Errorf("a plan with no contract steps has expanded %q", plan.Expanded)
		}
		return
	}
	if plan.Expanded == "" || len(plan.ExpandedModel) == 0 {
		t.Fatalf("a plan with contract steps has no expanded model")
	}
	canonical, err := ir.CanonicalJSON(plan.ExpandedModel)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, plan.ExpandedModel) {
		t.Errorf("expandedModel is not canonical JSON")
	}
	sum := sha256.Sum256(plan.ExpandedModel)
	if hash := hex.EncodeToString(sum[:]); hash != plan.Expanded {
		t.Errorf("expandedModel hashes to %s, not to expanded %s", hash, plan.Expanded)
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
	filepath.Join(sqlgenFixtures, "fixture-queue-db"),
	filepath.Join(sqlgenFixtures, "fixture-version-graph-db"),
	filepath.Join(sqlgenFixtures, "fixture-user-model-db"),
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
