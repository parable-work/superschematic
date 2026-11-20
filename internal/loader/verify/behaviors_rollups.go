package verify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// rollupsBehavior is the name of the core's Rollups (D104's step 4).
const rollupsBehavior = "Rollups"

// rollupsWorkflowStatus names a linking table's Workflow status, as a
// rollup's field names it.
const rollupsWorkflowStatus = "Workflow.status"

// rollupNumbers are the IR types sum, min and max read: a number (D104's
// step 4), Constants' oneWayNumbers, which a JSON number holds.
var rollupNumbers = oneWayNumbers

// rollupSpec is one rollup of a Rollups config as the build reads it.
type rollupSpec struct {
	Schema   string `json:"schema"`
	Link     string `json:"link"`
	Function string `json:"function"`
	Field    string `json:"field"`
}

// checkRollupsConfig holds the config of a Rollups use the table td
// implements to the tables of its database (D104's step 4), in the
// engine's words where it has them, each problem `type <T>: behavior
// Rollups config: rollup <name>: <reason>` and, like the core, the first
// reported, in the config's order. These are the rules that read another
// table, which a type's define check on the core cannot see: the rollup's
// table is a table of the database that composes a behavior, so the core's
// layout holds its rows; its link is a to-one relation of it that points
// at td, the rows a HasMany of td lists; all and any read its one
// Workflow; countBy, sum, min and max read a field of it of a fitting
// type. latest waits for a later step, as the core says. The gates' rules
// (a state of td's one Workflow) are the core's define check's. A config
// the configSchema refuses is reported by checkTypeBehaviors and left alone
// here.
func checkRollupsConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config struct {
		Rollups json.RawMessage `json:"rollups"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	names, specs, err := orderedRollups(config.Rollups)
	if err != nil {
		return
	}
	for i, name := range names {
		if why := rollupProblem(schema, td, specs[i]); why != "" {
			r.errorf(td.Owner, "type %s: behavior %s config: rollup %s: %s", td.Name, ref.Name, name, why)
			return
		}
	}
}

// orderedRollups reads a config's rollups in the order it writes them.
func orderedRollups(raw json.RawMessage) ([]string, []rollupSpec, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, nil, err
	}
	var names []string
	var specs []rollupSpec
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		name, _ := key.(string)
		var spec rollupSpec
		if err := dec.Decode(&spec); err != nil {
			return nil, nil, err
		}
		names = append(names, name)
		specs = append(specs, spec)
	}
	return names, specs, nil
}

// rollupProblem is why one rollup cannot read its table on td, or "".
func rollupProblem(schema *ir.Schema, td *ir.TypeDef, spec rollupSpec) string {
	if spec.Function == "latest" {
		return "latest reads the linked row created last, and a table's row carries no time of its creation the core reads: latest waits for a later step (D104's step 4)"
	}
	linking := schema.Types[spec.Schema]
	if linking == nil || linking.Role != ir.RoleDBTable || linking.JsonField || linking.IsEngineType() {
		return fmt.Sprintf("%s is not a table of %s", spec.Schema, schema.Name)
	}
	link := fieldNamedIn(linking, spec.Link)
	if link == nil || link.Owned() || !sqlutil.IsTableRelation(schema, link) || link.TypeRef.IsArray || link.TypeRef.Name != td.Name {
		var pointing []string
		for _, fd := range linking.Fields {
			if fd != nil && !fd.Owned() && !fd.TypeRef.IsArray && sqlutil.IsTableRelation(schema, fd) && fd.TypeRef.Name == td.Name {
				pointing = append(pointing, fd.Name)
			}
		}
		known := "none"
		if len(pointing) > 0 {
			known = strings.Join(pointing, ", ")
		}
		return fmt.Sprintf("%s has no to-one relation %s to %s (its relations to %s: %s)", spec.Schema, spec.Link, td.Name, td.Name, known)
	}
	if len(linking.TableBehaviors()) == 0 {
		return fmt.Sprintf("%s composes no behavior, so the behaviors' core holds none of its rows: a rollup reads a table that composes one, Workflow among them (D103)", spec.Schema)
	}
	workflows := workflowUses(linking)
	switch spec.Function {
	case "all", "any":
		switch len(workflows) {
		case 0:
			return fmt.Sprintf("%s reads the terminal states of %s's Workflow, which it does not compose", spec.Function, spec.Schema)
		case 1:
			return ""
		default:
			return fmt.Sprintf("%s reads the terminal states of %s's Workflow, and it has several (%s): a rollup reads a linking table's one Workflow", spec.Function, spec.Schema, strings.Join(workflows, ", "))
		}
	case "countBy":
		if spec.Field == rollupsWorkflowStatus {
			if len(workflows) == 1 {
				return ""
			}
			return fmt.Sprintf("%s has no field %s; countBy takes a string, enum or boolean field of its type, or %s when it composes one Workflow", spec.Schema, spec.Field, rollupsWorkflowStatus)
		}
		if slices.Contains(workflows, spec.Field) {
			return ""
		}
		column := rollupColumn(schema, linking, spec.Field)
		switch {
		case column == nil:
			return fmt.Sprintf("%s has no field %s; countBy takes a string, enum or boolean field of its type, or %s when it composes Workflow", spec.Schema, spec.Field, rollupsWorkflowStatus)
		case column.Type != "string" && column.Type != "boolean" && schema.Enums[column.Type] == nil:
			return fmt.Sprintf("countBy takes a string, enum or boolean field, and %s's %s holds %s", spec.Schema, spec.Field, rollupKind(column))
		}
	case "sum", "min", "max":
		column := rollupColumn(schema, linking, spec.Field)
		switch {
		case column == nil:
			return fmt.Sprintf("%s has no field %s; %s takes a number or integer field of its type", spec.Schema, spec.Field, spec.Function)
		case !slices.Contains(rollupNumbers, column.Type):
			return fmt.Sprintf("%s takes a number or integer field, and %s's %s holds %s", spec.Function, spec.Schema, spec.Field, rollupKind(column))
		}
	}
	return ""
}

// rollupColumn is the own field of the linking table a rollup's field
// names, as the core's spec holds it: a list, a map and a field encrypted
// at rest are none a rollup reads.
func rollupColumn(schema *ir.Schema, linking *ir.TypeDef, name string) *behaviorlayout.Column {
	for _, column := range behaviorlayout.SpecFields(schema, linking) {
		if column.Field != name {
			continue
		}
		fd := sqlutil.FieldNamed(linking, name)
		if fd != nil && (fd.TypeRef.IsArray || fd.TypeRef.IsMap || fd.Encrypted) {
			continue
		}
		return &column
	}
	return nil
}

// rollupKind names what a column holds in a sentence: its IR type.
func rollupKind(column *behaviorlayout.Column) string {
	switch column.Type {
	case "Generic.JSON":
		return "any JSON value"
	case "number":
		return "a number"
	case "string", "boolean":
		return "a " + column.Type
	}
	return "a " + column.Type
}
