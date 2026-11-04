package behaviorops

import (
	"encoding/json"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// writeRules are the behaviors whose config names a permission the core
// checks on an update of a row (D104), by the member that names it:
// Constants' permission lets a caller change a constant or move a oneWay
// field back, StateRules' change a field a state freezes, Lease's
// overridePermission change a row another principal holds the lease of,
// Retries' limitsPermission change limitsField, and Budget's
// limitPermission change a meter's limitField.
var writeRules = map[string]string{
	"Constants":  "permission",
	"StateRules": "permission",
	"Lease":      "overridePermission",
	"Retries":    "limitsPermission",
	"Budget":     "limitPermission",
}

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
// the permission each use of each table op updates a row of (UpdatesARow)
// through an argument of a @source view of it names of those writeRules
// lists (Constants' and StateRules' permission, Lease's overridePermission,
// Retries' limitsPermission, Budget's limitPermission), sorted, each once. A view's target that names a service is looked up in
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
			member, ok := writeRules[ref.Name]
			if !ok || len(ref.Config) == 0 {
				continue
			}
			var config map[string]json.RawMessage
			var permission string
			if json.Unmarshal(ref.Config, &config) != nil || json.Unmarshal(config[member], &permission) != nil || permission == "" || slices.Contains(out, permission) {
				continue
			}
			out = append(out, permission)
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
