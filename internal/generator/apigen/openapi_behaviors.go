package apigen

import (
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// behaviorOperationErrors are the refusals of the behaviors' core a
// behavior operation answers beside the 400 and 500 of every route (D103),
// by status, each code with what it means: the problem's status, its code
// and the core's message.
var behaviorOperationErrors = []struct {
	status  int
	code    string
	meaning string
}{
	{403, "forbidden", "the caller lacks a permission the behavior's config names, which the core checks."},
	{404, "not_found", "no row holds the key."},
	{409, "vetoed", "a behavior's guard or handler refuses the change; details.vetoCode names the rule."},
	{409, "conflict", "a row the operation would write collides with one that exists."},
	{422, "invalid_instance", "a behavior's validation refuses the fields the operation would store."},
	{503, "busy", "the row kept changing while the operation ran again, and it gave up; try again."},
}

// addBehaviorErrorResponses adds to responses the problems a behavior
// operation answers with (D103): each refusal of the core, by its status.
// A refusal of status 500 is the server's own internal error.
func addBehaviorErrorResponses(responses map[string]interface{}) {
	var statuses []int
	meanings := map[int][]string{}
	for _, e := range behaviorOperationErrors {
		if _, seen := meanings[e.status]; !seen {
			statuses = append(statuses, e.status)
		}
		meanings[e.status] = append(meanings[e.status], e.code+": "+e.meaning)
	}
	for _, status := range statuses {
		responses[strconv.Itoa(status)] = map[string]interface{}{
			"description": strings.Join(meanings[status], " "),
			"content": map[string]interface{}{
				"application/json": map[string]interface{}{
					"schema": map[string]interface{}{
						"$ref": "#/components/schemas/Error",
					},
				},
			},
		}
	}
}

// markOwnedFieldsReadOnly marks readOnly each property of an object
// component that a behavior owns (D103): an owned field of the table the
// component is built from, or a field of a @source view that projects one,
// by its name. Only the behavior's operations change such a field, so no
// request carries it.
func markOwnedFieldsReadOnly(components map[string]interface{}, types map[string]*ir.TypeDef, schema *ir.Schema, dependencies map[string]*ir.Schema) {
	for name, td := range types {
		component, ok := components[name].(map[string]interface{})
		if !ok {
			continue
		}
		properties, ok := component["properties"].(map[string]interface{})
		if !ok {
			continue
		}
		owned := map[string]bool{}
		for _, fd := range td.OwnedFields() {
			owned[fd.Name] = true
		}
		if source := sourceTable(td, schema, dependencies); source != nil {
			for _, fd := range source.OwnedFields() {
				owned[fd.Name] = true
			}
		}
		for _, fd := range td.Fields {
			if !owned[fd.Name] {
				continue
			}
			if property, ok := properties[fd.Name].(map[string]interface{}); ok {
				property["readOnly"] = true
			}
		}
	}
}

// sourceTable is the type a @source view projects, from the schema or the
// dependency its target names, or nil.
func sourceTable(td *ir.TypeDef, schema *ir.Schema, dependencies map[string]*ir.Schema) *ir.TypeDef {
	if td == nil || td.Source == nil || td.Source.Target == "" {
		return nil
	}
	target := td.Source.Target
	i := strings.LastIndex(target, ".")
	if i < 0 {
		return schema.Types[target]
	}
	service, name := target[:i], target[i+1:]
	if dep := dependencies[service]; dep != nil {
		return dep.Types[name]
	}
	if service == schema.Name {
		return schema.Types[name]
	}
	return nil
}
