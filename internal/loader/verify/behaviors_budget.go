package verify

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// integerSegments are the last segments, lowercased, of the IR types a
// Budget meter's limitField or reserveField may have, an integer scalar
// (D104): the core's superschematic_rules_behaviors::budget::INTEGER_SEGMENTS,
// the engine's integer trait, which TestBudgetIntegerTypesAreTheCores holds
// them to.
var integerSegments = []string{"int", "int32", "int64", "integer"}

// integerType reports whether an IR type is an integer scalar, as the core
// reads one: its name's last segment, lowercased, is one of integerSegments.
func integerType(name string) bool {
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return slices.Contains(integerSegments, strings.ToLower(name))
}

// budgetMeter is one meter of a Budget config as the build reads it.
type budgetMeter struct {
	LimitField   *string         `json:"limitField"`
	ReserveField *string         `json:"reserveField"`
	Scope        *string         `json:"scope"`
	Reset        json.RawMessage `json:"reset"`
	Window       json.RawMessage `json:"window"`
}

// budgetMeters reads a Budget config's meters, or nil.
func budgetMeters(config json.RawMessage) map[string]budgetMeter {
	var budget struct {
		Meters map[string]budgetMeter `json:"meters"`
	}
	if len(config) == 0 || json.Unmarshal(config, &budget) != nil {
		return nil
	}
	return budget.Meters
}

// checkBudgetConfig holds the config of a Budget use a table implements to
// the table and its database (D104), in the core's words where the core
// sees the problem (superschematic_rules_behaviors::budget): each meter's
// limitField and reserveField an own integer field, its scope a to-one
// relation of the table, its window a reset to count; onExceeded on a table
// that implements Lease; and the Workflow escalate moves, the one its
// status names or the table's one, its move held to it. What only the IR
// shows is the build's: a limitField or reserveField a behavior owns, a
// relation by its name, an audit field, a list or a map and a field
// encrypted at rest; a scope that is a to-many relation; and a scope's
// target, which implements Budget with the meter, through a chain of
// scopes that ends, since the core's define check reads no other table.
// Each problem is `type <T>: behavior Budget config: <reason>`, and like
// the core it reports the first: each meter in the canonical config's
// order, then onExceeded, then escalate. A config the configSchema refuses
// is reported by checkTypeBehaviors and left alone here.
func checkBudgetConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, uses map[string]registry.Behavior, r *Result) {
	var config struct {
		Meters     map[string]budgetMeter `json:"meters"`
		OnExceeded json.RawMessage        `json:"onExceeded"`
		Escalate   *statusMove            `json:"escalate"`
		Status     *string                `json:"status"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	if why := budgetProblem(schema, td, uses, config.Meters, len(config.OnExceeded) > 0, config.Escalate, config.Status); why != "" {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, why)
	}
}

// budgetProblem is the first problem of a table's Budget config, or "".
func budgetProblem(schema *ir.Schema, td *ir.TypeDef, uses map[string]registry.Behavior, meters map[string]budgetMeter, onExceeded bool, escalate *statusMove, status *string) string {
	fields := tableFieldsOf(schema, td, uses)
	for _, name := range slices.Sorted(maps.Keys(meters)) {
		meter := meters[name]
		for _, field := range []struct {
			member string
			value  *string
		}{{"limitField", meter.LimitField}, {"reserveField", meter.ReserveField}} {
			if field.value == nil {
				continue
			}
			rule := fieldRule{at: "meter " + name + ": " + field.member, member: field.member, names: "the table's own field", operations: true, holds: "one integer",
				types: integerType, wrong: "is not an integer field"}
			if why := fieldProblem(fields, rule, *field.value); why != "" {
				return why
			}
		}
		if meter.Scope != nil {
			if why := scopeProblem(schema, td, name, *meter.Scope); why != "" {
				return why
			}
		}
		if len(meter.Window) > 0 && len(meter.Reset) == 0 {
			return fmt.Sprintf("meter %s: window rolling counts the last period, and the meter names no reset to give it one", name)
		}
	}
	if onExceeded && !implementsLease(td) {
		return "onExceeded sends a directive through Lease's direct, which the type does not list"
	}
	if escalate == nil && status == nil {
		return ""
	}
	workflow, found, why := flowFor(td, status)
	switch {
	case why != "":
		return why
	case escalate == nil:
		return ""
	case !found:
		return noWorkflow("escalate")
	}
	return moveProblem(workflow, "escalate", *escalate)
}

// toOneRelations are td's own fields that are to-one relations of a table
// of schema, by name, in field order: what a Budget meter's scope names.
func toOneRelations(schema *ir.Schema, td *ir.TypeDef) []*ir.FieldDef {
	var out []*ir.FieldDef
	for _, fd := range td.Fields {
		if fd != nil && !fd.Owned() && !fd.TypeRef.IsArray && sqlutil.IsTableRelation(schema, fd) {
			out = append(out, fd)
		}
	}
	return out
}

// scopeProblem holds a meter's scope to td and its database (D104): a
// to-one relation of td, in the core's words, not a to-many one; whose
// target implements Budget with the meter; through a chain of scopes that
// does not come back to td.
func scopeProblem(schema *ir.Schema, td *ir.TypeDef, meter, scope string) string {
	if fd := sqlutil.FieldNamed(td, scope); fd != nil && fd.TypeRef.IsArray && sqlutil.IsTableRelation(schema, fd) {
		return fmt.Sprintf("meter %s: scope %s holds a list of rows, and a meter draws on one scope", meter, scope)
	}
	chain := []string{td.Name}
	at, relation := td, scope
	for {
		var target *ir.FieldDef
		var names []string
		for _, fd := range toOneRelations(schema, at) {
			names = append(names, fd.Name)
			if fd.Name == relation {
				target = fd
			}
		}
		if target == nil {
			if at != td {
				// The target's own check refuses its scope.
				return ""
			}
			listed := "none"
			if len(names) > 0 {
				listed = strings.Join(names, ", ")
			}
			return fmt.Sprintf("meter %s: scope %s is not a to-one relation of %s (its relations: %s)", meter, scope, td.Name, listed)
		}
		next := schema.Types[target.TypeRef.Name]
		config, ok := implementedConfig(next, budgetBehavior)
		if !ok {
			if at != td {
				return ""
			}
			return fmt.Sprintf("meter %s: scope %s: %s, which the relation points at, does not implement Budget", meter, scope, next.Name)
		}
		targetMeters := budgetMeters(config)
		inner, has := targetMeters[meter]
		if !has {
			if at != td {
				return ""
			}
			return fmt.Sprintf("meter %s: scope %s: %s, which the relation points at, has no meter %s (its meters: %s)", meter, scope, next.Name, meter, strings.Join(slices.Sorted(maps.Keys(targetMeters)), ", "))
		}
		chain = append(chain, next.Name)
		if next == td {
			return fmt.Sprintf("meter %s: scope %s: its scopes come back to %s (%s), and a meter's chain of scopes ends", meter, scope, td.Name, strings.Join(chain, ", "))
		}
		if slices.Contains(chain[:len(chain)-1], next.Name) || inner.Scope == nil {
			// A chain that loops without td is the looping table's to
			// refuse; one that ends is held.
			return ""
		}
		at, relation = next, *inner.Scope
	}
}

// checkEngineBudgetConfig refuses, on an engine type, each member of
// Budget's config that a table's Budget alone takes (D104): each meter's
// reset other than daily, then its window, meters in the canonical
// config's order, then observePermission and status. The declaration is
// shared, so the configSchema accepts them; the engine's Budget refuses
// them too, and the build says so first, naming the table form. A config
// the configSchema refuses is reported by checkTypeBehaviors and left alone
// here.
func checkEngineBudgetConfig(td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	meters := budgetMeters(ref.Config)
	for _, name := range slices.Sorted(maps.Keys(meters)) {
		meter := meters[name]
		if len(meter.Reset) > 0 && string(meter.Reset) != `"daily"` {
			r.errorf(td.Owner, "type %s: @behavior Budget names reset %s on meter %q, which a table's Budget takes (implements Budget<%s, C>, D104); an engine type's meter resets daily or never, so leave it out", td.Name, compactJSON(meter.Reset), name, td.Name)
		}
		if len(meter.Window) > 0 {
			r.errorf(td.Owner, "type %s: @behavior Budget names window on meter %q, which a table's Budget takes (implements Budget<%s, C>, D104); an engine type's meter counts its current day, or everything, so leave it out", td.Name, name, td.Name)
		}
	}
	if configNames(ref.Config, "observePermission") {
		r.errorf(td.Owner, "type %s: @behavior Budget names observePermission, which a table's Budget takes (implements Budget<%s, C>, D104); an engine type's meters count what its instances record, so leave it out", td.Name, td.Name)
	}
	if configNames(ref.Config, "status") {
		r.errorf(td.Owner, "type %s: @behavior Budget names status, the Workflow field a table's Budget reads (implements Budget<%s, C>, D104); an engine type's Budget reads its one Workflow, so leave status out", td.Name, td.Name)
	}
}

// compactJSON writes a JSON value with no space, as JSON.stringify does.
func compactJSON(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	out, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(out)
}
