package loader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestBundleScenariosAreTheLoadersParts (D103, step 5): the hand-written
// scenarios that run the core's bundles on the core
// (runtime/engine/testdata/rules/scenarios/core-bundles.json, whose
// transcript the testkit, development mode and the Rust adapter replay)
// define each table with the uses the loader expands its fixture into, in
// order and config for config, so the core runs what a table that
// implements the bundle composes. A Links use stands for the table's
// relation in the recorded format, and is left out.
func TestBundleScenariosAreTheLoadersParts(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "runtime", "engine", "testdata", "rules", "scenarios", "core-bundles.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Scenarios []struct {
			Name  string `json:"name"`
			Steps []struct {
				Call string            `json:"call"`
				Args []json.RawMessage `json:"args"`
			} `json:"steps"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]string{
		"Room": reservationDBFixture, "Booking": reservationDBFixture,
		"Rollout": barrierDBFixture, "Agent": barrierDBFixture,
		"Message": mailboxDBFixture, "Problem": assignmentDBFixture,
	}
	loaded := map[string]*ir.Schema{}
	compared := 0
	for _, scenario := range file.Scenarios {
		for _, step := range scenario.Steps {
			if step.Call != "schemas.define" {
				continue
			}
			var doc struct {
				Types map[string]struct {
					Behaviors []ir.BehaviorRef `json:"behaviors"`
				} `json:"types"`
			}
			if err := json.Unmarshal(step.Args[0], &doc); err != nil {
				t.Fatal(err)
			}
			for name, def := range doc.Types {
				fixture, ok := fixtures[name]
				if !ok {
					t.Errorf("%s: type %s has no fixture", scenario.Name, name)
					continue
				}
				if loaded[fixture] == nil {
					schema, err := LoadService(filepath.FromSlash(fixture))
					if err != nil {
						t.Fatal(err)
					}
					loaded[fixture] = schema
				}
				var defined []ir.BehaviorRef
				for _, ref := range def.Behaviors {
					if ref.Name != "Links" {
						defined = append(defined, ref)
					}
				}
				if err := ir.CanonicalizeBehaviors(defined); err != nil {
					t.Fatal(err)
				}
				var want, got []string
				for _, ref := range loaded[fixture].Types[name].Behaviors {
					want = append(want, ref.Name+"@"+ref.Field+" "+string(ref.Config))
				}
				for _, ref := range defined {
					got = append(got, ref.Name+"@"+ref.Field+" "+string(ref.Config))
				}
				if !slices.Equal(got, want) {
					t.Errorf("%s: %s's uses =\n%s\nwant the loader's\n%s", scenario.Name, name, strings.Join(got, "\n"), strings.Join(want, "\n"))
				}
				compared++
			}
		}
	}
	if compared != 6 {
		t.Errorf("compared %d defines, want 6", compared)
	}
}
