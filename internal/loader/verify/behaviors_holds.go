package verify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The core's commerce behaviors on a table (D70, D103's step 4), and the
// contract they meet through.
const (
	holdsBehavior    = "Holds"
	stockBehavior    = "Stock"
	calendarBehavior = "Calendar"
	supplyContract   = "Supply"
)

// holdSpec is one hold of a Holds config as the build reads it.
type holdSpec struct {
	Link string `json:"link"`
	Each *struct {
		Field  string `json:"field"`
		Target string `json:"target"`
	} `json:"each"`
	Schema   string          `json:"schema"`
	Quantity json.RawMessage `json:"quantity"`
	Interval json.RawMessage `json:"interval"`
}

// checkHoldsConfig holds the config of a Holds use the table td implements
// to the tables of its database (D103: the loader checks that a link Holds
// reads names a type that implements Supply), each problem `type <T>:
// behavior Holds config: holds.<name>...: <reason>` and, like the core,
// the first reported, in the config's order. These are the rules that read
// another table or the IR's shape of a field, which a type's define check
// on the core cannot see: an each hold's field is a list of td whose
// elements' type has the target field, and the table a hold names (an each
// hold's schema, or a link hold's relation's target) is a table of the
// database that implements a behavior whose declaration implements Supply
// (Stock, Calendar or a project's own supply), and asks of it what it
// holds: a quantity of Stock, an interval of Calendar. What the core's
// define check sees (a link that is no to-one relation, the states, the
// Workflow) is the core's to refuse, in its words. Holds requires
// Workflow, and Stock and Calendar conflict, as their declarations say,
// which checkTypeBehaviors holds. A config the configSchema refuses is
// reported by checkTypeBehaviors and left alone here.
func checkHoldsConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, reg *registry.Registry, r *Result) {
	var config struct {
		Holds json.RawMessage `json:"holds"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	names, specs, err := orderedHolds(config.Holds)
	if err != nil {
		return
	}
	for i, name := range names {
		if why := holdProblem(schema, td, reg, name, specs[i]); why != "" {
			r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, why)
			return
		}
	}
}

// orderedHolds reads a config's holds in the order it writes them.
func orderedHolds(raw json.RawMessage) ([]string, []holdSpec, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, nil, err
	}
	var names []string
	var specs []holdSpec
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		name, _ := key.(string)
		var spec holdSpec
		if err := dec.Decode(&spec); err != nil {
			return nil, nil, err
		}
		names = append(names, name)
		specs = append(specs, spec)
	}
	return names, specs, nil
}

// holdProblem is why one hold cannot take what it names on td, or "".
func holdProblem(schema *ir.Schema, td *ir.TypeDef, reg *registry.Registry, name string, spec holdSpec) string {
	at := "holds." + name
	var supply string
	switch {
	case spec.Each != nil:
		list := fieldNamedIn(td, spec.Each.Field)
		if list == nil || list.Owned() {
			// The core refuses a field the table lacks, in its words.
			return ""
		}
		if !list.TypeRef.IsArray {
			return fmt.Sprintf("%s.each.field %q is not a list field of %s: an each hold takes one item per element of a list", at, spec.Each.Field, td.Name)
		}
		element := schema.Types[list.TypeRef.Name]
		if element == nil || element.Role == ir.RoleDBTable && !element.JsonField {
			return fmt.Sprintf("%s.each.field %q is a list of %s, which is no struct whose elements name a supply: an each hold reads its target from each element", at, spec.Each.Field, list.TypeRef.Name)
		}
		if fieldNamedIn(element, spec.Each.Target) == nil {
			var known []string
			for _, fd := range element.Fields {
				if fd != nil {
					known = append(known, fd.Name)
				}
			}
			return fmt.Sprintf("%s.each.target %q is not a field of %s, the type of %s's %s (its fields: %s)", at, spec.Each.Target, element.Name, td.Name, spec.Each.Field, strings.Join(known, ", "))
		}
		supply = spec.Schema
	case spec.Link != "":
		link := fieldNamedIn(td, spec.Link)
		if link == nil || link.Owned() || link.TypeRef.IsArray || !sqlutil.IsTableRelation(schema, link) {
			// The core refuses a link that is no to-one relation, in its
			// words.
			return ""
		}
		supply = link.TypeRef.Name
	default:
		return ""
	}
	target := schema.Types[supply]
	if target == nil || target.Role != ir.RoleDBTable || target.JsonField {
		return fmt.Sprintf("%s names %s, which is not a table of %s: a hold takes what a table that implements %s gives (D103)", at, supply, schema.Name, supplyContract)
	}
	var suppliers []string
	for _, use := range target.TableBehaviors() {
		behavior, ok := reg.Behavior(use.Name)
		if !ok {
			continue
		}
		if contract, ok := behavior.Contract(supplyContract); ok && contract.Role == registry.ContractRoleImplements {
			suppliers = append(suppliers, use.Name)
		}
	}
	if len(suppliers) == 0 {
		return fmt.Sprintf("%s names %s, which implements no behavior that implements %s (Stock, Calendar or a supply of its own): a hold takes what a supply gives (D103)", at, supply, supplyContract)
	}
	counted := len(spec.Quantity) > 0
	for _, supplier := range suppliers {
		switch {
		case counted && supplier == calendarBehavior:
			return fmt.Sprintf("%s asks a quantity of %s, whose %s holds time: a hold of time asks an interval", at, supply, calendarBehavior)
		case !counted && supplier == stockBehavior:
			return fmt.Sprintf("%s asks an interval of %s, whose %s holds units: a hold of units asks a quantity", at, supply, stockBehavior)
		}
	}
	return ""
}
