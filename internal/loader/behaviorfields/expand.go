// Package behaviorfields adds the fields a field-anchored behavior owns to
// the tables that compose one (D103 in docs/DECISIONS.md). A DB type that
// writes `status: Workflow<{ ... }>` stays a table: each field the
// behavior declares becomes a column of it, which the readers do not write
// (they emit no field for the anchor) and the loader adds here, once the
// behaviors' own checks pass and before the rest of the verification
// pass, so every authoring form gets the same fields and every check that
// resolves a field by its name (an index, a projection's column,
// @enqueueOnChange) takes them as the table's own. The generators then
// meet them as ordinary fields of the table, each marked with the use that
// owns it (ir.FieldDef.Behavior), which the repositories read and leave
// out of their inputs.
//
// The names come from the layout rule, which lives in
// internal/generator/behaviorlayout alone: the value field takes the
// anchor's name and column (status), and every other field the anchor's
// name with its own after it (statusEnteredAt, status_entered_at).
package behaviorfields

import (
	"fmt"
	"slices"
	"sort"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// builtins are the types a declared field may take that are no scalar.
var builtins = []string{"string", "number", "boolean"}

// Expand adds, to each DB table of schema that composes field-anchored
// behaviors, the fields those behaviors declare, after the table's own
// fields: for each use in list order, each declared field in declaration
// order, a volatile field excepted, since a value computed at a read takes
// no column. A field takes its declaration's type and description; the
// value field is required, since the core sets it on every row it creates,
// and every other field is optional, absent until a behavior sets it. Each
// such table is given @optimistic's _version unless it is @versioned, the
// version the core's change sets hold each row to. Expand returns the
// scalars the added fields name, which the loader hydrates from the
// registry.
//
// The behaviors' checks have held what Expand relies on
// (verify.CheckBehaviors, checkFieldAnchors among them): each behavior is
// registered, names its value field and types its fields, and no added
// field or column is one the table already has. A field already added, as an IR read back holds it, is
// kept, so Expand may run again on its own output.
func Expand(schema *ir.Schema, reg *registry.Registry) ([]string, error) {
	if schema == nil || schema.Kind != ir.SchemaKindDB {
		return nil, nil
	}
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	var scalars []string
	for _, name := range names {
		td := schema.Types[name]
		if td == nil || td.Role != ir.RoleDBTable || len(td.FieldBehaviors()) == 0 {
			continue
		}
		used, err := expandType(td, reg)
		if err != nil {
			return nil, err
		}
		for _, scalar := range used {
			if !slices.Contains(scalars, scalar) {
				scalars = append(scalars, scalar)
			}
		}
	}
	for _, scalar := range scalars {
		if schema.Scalars == nil {
			schema.Scalars = map[string]*ir.ScalarDef{}
		}
		if schema.Scalars[scalar] == nil {
			schema.Scalars[scalar] = &ir.ScalarDef{Name: scalar}
		}
	}
	return scalars, nil
}

// ExpandExternal adds, to each type of types that composes field-anchored
// behaviors, the fields those behaviors own, as Expand adds them to the
// database's tables. types are the flattened source types the TypeScript
// reader resolves for a @source view of another service's table
// (verify.Input.ExternalTypes), which hold the table's own fields and its
// uses but none of the fields a use owns, so the verification pass checks
// a view that projects one (status, statusEnteredAt) against the table as
// its database's load holds it (D103). A use the database's own load
// refuses (an unregistered behavior, a field the table already has) is
// left as it is.
func ExpandExternal(types map[string]*ir.TypeDef, reg *registry.Registry) {
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		td := types[name]
		if td == nil || len(td.FieldBehaviors()) == 0 {
			continue
		}
		fields := td.Fields
		if _, err := expandType(td, reg); err != nil {
			td.Fields = fields
		}
	}
}

// expandType adds td's owned fields and returns the scalars they name.
func expandType(td *ir.TypeDef, reg *registry.Registry) ([]string, error) {
	var scalars []string
	for _, use := range td.FieldBehaviors() {
		behavior, ok := reg.Behavior(use.Name)
		if !ok {
			return nil, fmt.Errorf("type %s: field %s: behavior %s is not registered", td.Name, use.Field, use.Name)
		}
		for _, declared := range behavior.Fields {
			if declared.Volatile {
				continue
			}
			value := declared.Name == behavior.Value
			name := behaviorlayout.OwnedFieldName(use.Field, declared.Name, value)
			if existing := fieldNamed(td, name); existing != nil {
				if existing.Behavior != nil && existing.Behavior.Use == use.Field && existing.Behavior.Field == declared.Name {
					continue
				}
				return nil, fmt.Errorf("type %s: field %s: behavior %s adds field %s, which the type already declares", td.Name, use.Field, use.Name, name)
			}
			field := &ir.FieldDef{
				Name:        name,
				Description: declared.Description,
				TypeRef:     ir.TypeRef{Name: declared.Type},
				Required:    value,
				Behavior: &ir.FieldBehavior{
					Use:      use.Field,
					Behavior: use.Name,
					Field:    declared.Name,
					Value:    value,
				},
			}
			if column := behaviorlayout.OwnedColumn(use, declared.Name, value); column != codegen.ToSnakeCase(name) {
				field.SQLName = column
			}
			td.Fields = append(td.Fields, field)
			if !slices.Contains(builtins, declared.Type) && !slices.Contains(scalars, declared.Type) {
				scalars = append(scalars, declared.Type)
			}
		}
	}
	if !td.Versioned {
		td.Optimistic = true
	}
	return scalars, nil
}

func fieldNamed(td *ir.TypeDef, name string) *ir.FieldDef {
	for _, fd := range td.Fields {
		if fd != nil && fd.Name == name {
			return fd
		}
	}
	return nil
}
