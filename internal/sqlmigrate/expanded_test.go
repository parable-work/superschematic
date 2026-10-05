package sqlmigrate

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestExpandedModel checks the model a plan holds between its phases
// (D27, amended) for every plan case of both dialects with contract steps:
// it is a model in canonical form, sorted; the plan from it to the new
// model, with the same readers and no renames, has no expand steps and the
// plan's contract steps; and that plan's model between the phases is the
// same model. The convergence tests check that the plan's expand steps
// leave the schema the plan from an empty database to it creates.
func TestExpandedModel(t *testing.T) {
	cases := append(append(append([]planCase(nil), planCases...), sqlitePlanCases()...), expandedCases...)
	checked := 0
	for _, pc := range cases {
		if pc.fromEmpty {
			continue
		}
		from, to := pc.models(t)
		plan, err := Diff(from, to, Options{Renames: pc.renames, ReadersBefore: pc.readersBefore, ReadersAfter: pc.readersAfter})
		if err != nil {
			t.Fatalf("%s: %v", pc.golden(), err)
		}
		if plan.Expanded == "" {
			continue
		}
		checked++
		t.Run(pc.golden(), func(t *testing.T) {
			expanded := decodeModel(t, plan.ExpandedModel)
			if canonical, err := expanded.CanonicalJSON(); err != nil || !bytes.Equal(canonical, plan.ExpandedModel) {
				t.Fatalf("expandedModel does not read back as the same model (%v)", err)
			}
			sorted := decodeModel(t, plan.ExpandedModel)
			sorted.sort()
			if canonical, _ := sorted.CanonicalJSON(); !bytes.Equal(canonical, plan.ExpandedModel) {
				t.Errorf("expandedModel is not sorted")
			}

			again, err := Diff(expanded, to, Options{ReadersBefore: pc.readersBefore, ReadersAfter: pc.readersAfter})
			if err != nil {
				t.Fatalf("diff from expandedModel: %v", err)
			}
			if again.From != plan.Expanded || again.To != plan.To {
				t.Errorf("the plan from expandedModel goes from %s to %s", again.From, again.To)
			}
			for _, step := range again.Steps {
				if step.Phase == Expand {
					t.Errorf("the plan from expandedModel has expand step %d %s %s: %v", step.Index, step.Op, step.Subject, step.Statements)
				}
			}
			var contract []*Step
			for _, step := range plan.Steps {
				if step.Phase == Contract {
					contract = append(contract, step)
				}
			}
			if len(again.Steps) != len(contract) {
				t.Fatalf("the plan from expandedModel has %d steps; the plan has %d contract steps:\n%s\nwant\n%s",
					len(again.Steps), len(contract), again.SQL(), plan.SQL())
			}
			for i, want := range contract {
				compareContractStep(t, again.Steps[i], want)
			}
			if again.Expanded != plan.Expanded {
				t.Errorf("the plan from expandedModel holds model %s between its phases, not %s", again.Expanded, plan.Expanded)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no plan case has contract steps")
	}
}

// expandedCases are checked by TestExpandedModel only: a content column of
// a version graph dropped as the graph's schema epoch rises, whose contract
// hazard compares the two epochs, with another added and a third leaving
// the content while its column stays.
var expandedCases = []planCase{
	{name: "graph-content-drop", base: filepath.Join(sqlgenFixtures, "fixture-version-graph-db"), after: func(s *ir.Schema) {
		dropField(s, "Tasting", "remarks")
		addField(s, "Tasting", &ir.FieldDef{Name: "notes", TypeRef: stringRef})
		fieldNamed(s, "Note", "body").ConflictUnit = ir.ConflictUnitExcluded
		typeNamed(s, "Recipe").VersionGraph.SchemaEpoch = 2
	}},
}

// TestExpandedGraphs: between the phases a version graph keeps the previous
// schema epoch, a member's content is the new model's for the columns the
// new table has, and the previous model's for a column contract drops.
func TestExpandedGraphs(t *testing.T) {
	plan := expandedCases[0].plan(t)
	expanded := decodeModel(t, plan.ExpandedModel)
	if len(expanded.Graphs) != 1 || expanded.Graphs[0].SchemaEpoch != 1 {
		t.Fatalf("graphs between the phases: %+v", expanded.Graphs)
	}
	content := map[string][]string{}
	for _, m := range expanded.Graphs[0].Members {
		content[m.Table] = m.Content
	}
	if got := content["note"]; !slices.Equal(got, []string{"reply_to"}) {
		t.Errorf("note's content between the phases is %v; body left it in expand", got)
	}
	for _, col := range []string{"notes", "remarks", "taster"} {
		if !slices.Contains(content["tasting"], col) {
			t.Errorf("tasting's content between the phases %v lacks %s", content["tasting"], col)
		}
	}
}

// renameHint starts the sentence a destructive hazard ends with when the
// plan also adds a column or a table of the same shape: the plan from the
// model between the phases adds nothing, so its reason has no hint.
const renameHint = " It may be a rename: "

// compareContractStep compares a step of the plan from the model between
// the phases with the plan's contract step: the same operation, subject and
// SQL, run the same way, with the same hazards. A reason may differ only by
// the plan's rename hint.
func compareContractStep(t *testing.T, got, want *Step) {
	t.Helper()
	name := want.Op + " " + want.Subject
	if got.Phase != want.Phase || got.Op != want.Op || got.Subject != want.Subject {
		t.Errorf("step %d is %s %s %s, want %s %s", got.Index, got.Phase, got.Op, got.Subject, want.Phase, name)
		return
	}
	if !slices.Equal(got.Statements, want.Statements) {
		t.Errorf("%s: statements\n%s\nwant\n%s", name, strings.Join(got.Statements, ";\n"), strings.Join(want.Statements, ";\n"))
	}
	if got.Transactional != want.Transactional || got.ForeignKeysOff != want.ForeignKeysOff || !slices.Equal(got.Recovery, want.Recovery) {
		t.Errorf("%s: transactional %t, foreign keys off %t, recovery %v; want %t, %t, %v",
			name, got.Transactional, got.ForeignKeysOff, got.Recovery, want.Transactional, want.ForeignKeysOff, want.Recovery)
	}
	if len(got.Hazards) != len(want.Hazards) {
		t.Errorf("%s: %d hazards, want %d", name, len(got.Hazards), len(want.Hazards))
		return
	}
	for i, w := range want.Hazards {
		g := got.Hazards[i]
		if g.ID != w.ID || g.Class != w.Class || g.Subject != w.Subject || g.Reader != w.Reader {
			t.Errorf("%s: hazard %s, want %s", name, g.ID, w.ID)
			continue
		}
		reason := w.Reason
		if before, _, ok := strings.Cut(reason, renameHint); ok {
			reason = before
			t.Logf("%s: the plan's %s reason names a rename the plan from expandedModel does not see", name, w.Class)
		}
		if g.Reason != reason {
			t.Errorf("%s: %s reason\n%s\nwant\n%s", name, w.Class, g.Reason, reason)
		}
	}
}

// decodeModel reads a model's JSON.
func decodeModel(t *testing.T, raw []byte) *Model {
	t.Helper()
	var m Model
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

// expandOnly is a plan with its expand steps only.
func expandOnly(plan *Plan) *Plan {
	out := *plan
	out.Steps = nil
	for _, step := range plan.Steps {
		if step.Phase == Expand {
			out.Steps = append(out.Steps, step)
		}
	}
	return &out
}

// writeExpandedCase writes the convergence case of a plan's model between
// its phases (D27, amended): fromSQL, rows seeded into it unless the case
// seeds none, and the plan's expand steps must leave the schema the plan
// from an empty database to the plan's expandedModel creates, written as
// one script (CreateSQL).
func writeExpandedCase(t *testing.T, dir string, pc planCase, plan *Plan, fromSQL string, dialect seeds) {
	t.Helper()
	script, err := CreateSQL(decodeModel(t, plan.ExpandedModel))
	if err != nil {
		t.Fatalf("%s: the plan from an empty database to expandedModel: %v", pc.golden(), err)
	}
	writeJSON(t, filepath.Join(dir, "plan.json"), expandOnly(plan))
	writeFile(t, filepath.Join(dir, "from.sql"), []byte(fromSQL))
	writeFile(t, filepath.Join(dir, "to.sql"), []byte(script))
	if pc.noSeed != "" {
		return
	}
	from, to := pc.models(t)
	r, err := resolveRenames(from, to, pc.renames)
	if err != nil {
		t.Fatal(err)
	}
	seed, checks := seedRows(t, from, to, r, dialect)
	writeFile(t, filepath.Join(dir, "seed.sql"), []byte(seed))
	writeJSON(t, filepath.Join(dir, "checks.json"), checks)
}
