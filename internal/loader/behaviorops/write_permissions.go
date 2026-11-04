package behaviorops

import (
	"encoding/json"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// writeRules are the behaviors whose config's permission the core checks on
// an update of a row (D104): Constants' lets a caller change a constant or
// move a oneWay field back, StateRules' change a field a state freezes.
var writeRules = []string{"Constants", "StateRules"}

// UpdatesARow reports whether op, whose method is not GET, writes a row of td
// that exists: a PUT or a PATCH, or an operation of another method with an
// argument named as a key field of td (D104), which addresses the row (a
// POST to questions/{id}/reassign that takes id). Any other is taken for a
// create.
func UpdatesARow(op *ir.FieldDef, td *ir.TypeDef) bool {
	switch strings.ToUpper(op.HTTPMethod) {
	case "PUT", "PATCH":
		return true
	}
	for _, arg := range op.Arguments {
		if arg == nil {
			continue
		}
		for _, fd := range td.Fields {
			if fd != nil && fd.Key && fd.Name == arg.Name {
				return true
			}
		}
	}
	return false
}

// WritePermissions are the permissions the core checks on op's writes (D104):
// the permission of each Constants and StateRules use of each table op
// updates a row of (UpdatesARow) through an argument of a @source view of it,
// sorted, each once. A view's target that names a service is looked up in
// deps by that name; one that names none in the API's database, its authDb or
// its one DB dependency. None for a GET, a behavior operation, and an
// operation that updates no table so.
func WritePermissions(api *ir.Schema, op *ir.FieldDef, deps map[string]*ir.Schema) []string {
	if api == nil || op == nil || op.BehaviorOperation != nil || strings.EqualFold(op.HTTPMethod, "GET") {
		return nil
	}
	var out []string
	for _, arg := range op.Arguments {
		if arg == nil {
			continue
		}
		td := sourceTable(api, arg.TypeRef.Name, deps)
		if td == nil || !UpdatesARow(op, td) {
			continue
		}
		for _, ref := range td.TableBehaviors() {
			if !slices.Contains(writeRules, ref.Name) || len(ref.Config) == 0 {
				continue
			}
			var config struct {
				Permission string `json:"permission"`
			}
			if json.Unmarshal(ref.Config, &config) != nil || config.Permission == "" || slices.Contains(out, config.Permission) {
				continue
			}
			out = append(out, config.Permission)
		}
	}
	slices.Sort(out)
	return out
}

// sourceTable is the table the type of api named name is a @source view of,
// read from deps: by the service its target names, or the API's database
// when it names none. It is nil for a type that is no view, and for a target
// deps does not hold.
func sourceTable(api *ir.Schema, name string, deps map[string]*ir.Schema) *ir.TypeDef {
	view := api.Types[name]
	if view == nil {
		view = api.Inputs[name]
	}
	if view == nil || view.Source == nil {
		return nil
	}
	service, typeName := "", view.Source.Target
	if i := strings.LastIndex(typeName, "."); i >= 0 {
		service, typeName = typeName[:i], typeName[i+1:]
	}
	if service == "" {
		db, _, ok := api.Database()
		if !ok {
			return nil
		}
		service = db
	}
	schema := deps[service]
	if schema == nil {
		return nil
	}
	return schema.Types[typeName]
}
