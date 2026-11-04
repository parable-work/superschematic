package behaviorlayout_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// approvalsFixture is the DB service whose Task implements Constants,
// StateRules and Approvals beside its status Workflow (D104).
const approvalsFixture = "../../loader/tsreader/testdata/services/fixture-approvals-db"

// The layout document of a table that implements Constants, StateRules and
// Approvals holds their uses in the order the type lists them, before its
// status: Constants' and StateRules' with no field, operation or sub-table,
// Approvals' with its three operations, listSignOffs writing nothing, and
// its signOffs sub-table keyed by number; no deadline queue and no change
// queue, since none of the three arms a deadline or hears a change; and its
// audit createdBy, which StateRules' by and Approvals' gates read (D104).
// testdata/fixture-approvals-db.layout.json is the document the adapters of
// every language are written against.
func TestLayoutOfConstantsStateRulesAndApprovals(t *testing.T) {
	layout := layoutOfFixture(t, approvalsFixture, "fixture-approvals-db")
	task := layout.Types["Task"]
	if task == nil {
		t.Fatalf("the layout has no Task: %+v", layout.Types)
	}
	var uses []string
	for _, use := range task.Uses {
		var operations []string
		for _, op := range use.Operations {
			operations = append(operations, op.Name+map[bool]string{true: " writes", false: ""}[op.Writes])
		}
		var tables []string
		for _, table := range use.Tables {
			tables = append(tables, table.Name+" "+table.Table+" "+table.Row+" "+table.Key.Name)
		}
		uses = append(uses, use.ID+" "+use.Behavior+" "+use.Member+" ["+strings.Join(operations, ", ")+"] ["+strings.Join(tables, ", ")+"]")
	}
	if want := []string{
		"Constants Constants constants [] []",
		"StateRules StateRules staterules [] []",
		"Approvals Approvals approvals [signOff writes, withdrawSignOff writes, listSignOffs] [signOffs task_approvals_sign_offs task_id number]",
		"status Workflow status [transition writes] []",
	}; strings.Join(uses, "\n") != strings.Join(want, "\n") {
		t.Errorf("Task's uses =\n%s\nwant\n%s", strings.Join(uses, "\n"), strings.Join(want, "\n"))
	}
	if task.Deadlines != nil || task.Changes != nil {
		t.Errorf("Task's deadlines = %+v, changes = %+v, want neither", task.Deadlines, task.Changes)
	}
	audit, _ := json.Marshal(task.Audit)
	if string(audit) != `[{"field":"createdBy","column":"created_by","type":"Identity.UUID"}]` {
		t.Errorf("Task's audit = %s", audit)
	}
	var own []string
	for _, c := range task.Own {
		own = append(own, c.Field)
	}
	if got := strings.Join(own, ","); got != "title,owner,reviewer,plan,proof,urgent,attempts" {
		t.Errorf("Task's own = %s", got)
	}
	var spec []string
	for _, f := range task.Spec.Fields {
		spec = append(spec, f.Name+":"+f.Type)
	}
	if got := strings.Join(spec, ","); got != "title:string,owner:Identity.UUID,reviewer:Identity.UUID,plan:string,proof:string,urgent:boolean,attempts:number,createdBy:Identity.UUID" {
		t.Errorf("Task's spec fields = %s", got)
	}
}

// The audit names createdBy when a use reads it (D104): a StateRules
// state's by that names it, or an Approvals gate that excludes the creator,
// excludeCreator absent or true; neither, and a Workflow by that names it
// not, reads none. SpecFields, the fields a refusal of a config names, are
// the spec's either way.
func TestLayoutReadsCreatedByForStateRulesAndApprovals(t *testing.T) {
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.FromSlash(approvalsFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	td := schema.Types["Task"]
	original := make([]json.RawMessage, len(td.Behaviors))
	for i, ref := range td.Behaviors {
		original[i] = ref.Config
	}
	for _, tc := range []struct {
		name       string
		staterules string
		approvals  string
		reads      bool
	}{
		{"StateRules' by names it", `{"states":{"planned":{"frozen":["plan"],"by":["createdBy"]}}}`, `{"gates":{"done":{"count":1,"permission":"p","excludeCreator":false}}}`, true},
		{"a gate excludes the creator when excludeCreator is absent", `{"states":{"planned":{"frozen":["plan"],"by":["reviewer"]}}}`, `{"gates":{"done":{"count":1,"permission":"p"}}}`, true},
		{"a gate excludes the creator", `{"states":{"planned":{"frozen":["plan"]}}}`, `{"gates":{"proving":{"count":1,"permission":"p","excludeCreator":false},"done":{"count":1,"permission":"p","excludeCreator":true}}}`, true},
		{"neither", `{"states":{"planned":{"frozen":["plan"],"by":["reviewer"]}}}`, `{"gates":{"done":{"count":1,"permission":"p","excludeCreator":false}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i, ref := range td.Behaviors {
				switch ref.Name {
				case "StateRules":
					td.Behaviors[i].Config = json.RawMessage(tc.staterules)
				case "Approvals":
					td.Behaviors[i].Config = json.RawMessage(tc.approvals)
				default:
					td.Behaviors[i].Config = original[i]
				}
			}
			task := layoutOf(t, schema).Types["Task"]
			if got := task.Audit != nil; got != tc.reads {
				t.Errorf("Task's audit = %+v, want read %v", task.Audit, tc.reads)
			}
			if got := behaviorlayout.AuditFields(schema, td); len(got) != 1 || got[0].Field != "createdBy" {
				t.Errorf("AuditFields = %+v, want createdBy either way", got)
			}
			// SpecFields are the spec's fields, which a refusal names.
			var spec, fields []string
			for _, f := range task.Spec.Fields {
				spec = append(spec, f.Name)
			}
			for _, c := range behaviorlayout.SpecFields(schema, td) {
				fields = append(fields, c.Field)
			}
			if strings.Join(fields, ",") != strings.Join(spec, ",") {
				t.Errorf("SpecFields = %v, want the spec's fields %v", fields, spec)
			}
		})
	}
}
