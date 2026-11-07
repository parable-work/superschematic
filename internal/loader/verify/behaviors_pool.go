package verify

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// poolBehavior is the name of the core's Pool, which the behaviors' core
// alone implements, on a table (D104).
const poolBehavior = "Pool"

// orderTypes are the IR types Pool's order field may have, a number (D104):
// the core's superschematic_rules_behaviors::pool::ORDER_TYPES, Constants'
// ONE_WAY_NUMBERS, which TestOrderTypesAreTheCores holds them to.
var orderTypes = []string{"number", "Generic.Int64", "Generic.Float64"}

// checkPoolConfig holds the config of a Pool use the table td implements to
// the table (D104), in the core's words where the core sees the problem
// (superschematic_rules_behaviors::pool): order names an own field of the
// table, and one that holds a number. What only the IR shows is the
// build's, in the words Presence's principalField gets: an order field a
// behavior owns, a relation by its name, an audit field, a list or a map
// and a field encrypted at rest. The problem is `type <T>: behavior Pool
// config: <reason>`. A table that implements Pool without Budget, or any
// other behavior that implements Capacity, is taken: its members have room
// by Capacity's rule. A config the configSchema refuses is reported by
// checkTypeBehaviors and left alone here.
func checkPoolConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, uses map[string]registry.Behavior, r *Result) {
	var config struct {
		Order string `json:"order"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	if why := poolProblem(tableFieldsOf(schema, td, uses), config.Order); why != "" {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, why)
	}
}

// poolProblem is the problem of a table's Pool order field, or "": the
// core's refusal of a field the table's spec lacks, the build's of a field
// a behavior owns, a relation, an audit field, a list and a field encrypted
// at rest, and the core's of a field that holds no number.
func poolProblem(fields *tableFields, order string) string {
	rule := fieldRule{at: "order", member: "order", names: "the table's own field whose value ranks the members", operations: true, holds: "one number"}
	if why := fieldProblem(fields, rule, order); why != "" {
		return why
	}
	if _, _, column, _ := fields.classify(order, false); column != nil && !slices.Contains(orderTypes, column.Type) {
		return fmt.Sprintf("order %q is not a number field of %s: it ranks the members by a number (number, Generic.Int64 or Generic.Float64)", order, fields.td.Name)
	}
	return ""
}
