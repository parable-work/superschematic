package registry

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// BehaviorReference is a member of a behavior's config that names
// something of the table its use sits on (D103's step 5): the
// declaration states it, so the build holds a use's config to it and
// derives what reads it, where per-behavior code did before.
type BehaviorReference struct {
	// Path is the member, a ConfigPath: status, deadlines.*.start.field,
	// rules[].when.allTerminal.
	Path string `json:"path"`
	// Kind is what the member names: ReferenceField, ReferenceWorkflow,
	// ReferenceState or ReferenceListen.
	Kind string `json:"kind"`
	// Type is, for a field, the scalar the field's values are: a field
	// of another type is refused. Empty, any own field.
	Type string `json:"type,omitempty"`
	// Reason is what the build says after a problem with the member, the
	// core's words for why it holds (a deadline starts from the time a
	// value holds).
	Reason string `json:"reason,omitempty"`
}

// The kinds of a behavior's reference.
const (
	// ReferenceField names an own field of the table, by its name.
	ReferenceField = "field"
	// ReferenceWorkflow names a Workflow use of the table, by its field:
	// the Workflow the use's other members read (a config's status).
	ReferenceWorkflow = "workflow"
	// ReferenceState names a state of the Workflow the config's workflow
	// reference names, or of the table's one Workflow when it names none.
	ReferenceState = "state"
	// ReferenceListen is an object whose schema and link name a table of
	// the database and its to-one relation to this one, whose writes the
	// use hears (BehaviorListens).
	ReferenceListen = "listen"
)

// referenceKinds are the kinds a reference takes, in the order the docs
// list them.
var referenceKinds = []string{ReferenceField, ReferenceWorkflow, ReferenceState, ReferenceListen}

// configPathSegment is one segment of a ConfigPath: a member's name,
// optionally followed by [] for each item of its list, or * for each
// member of an object.
var configPathSegment = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*(\[\])?|\*)$`)

// A ConfigPath names members of a behavior's config: segments joined by
// dots, each a member's name, the name followed by [] for each item of
// the list it holds, or * for each member of an object. overridePermission
// is one member; transitions[].permission each transition's permission;
// gates.*.permission each gate's.

// CheckConfigPath reports whether path is a ConfigPath that configSchema,
// a behavior's config schema, has: each segment a member its properties
// name, an item of an array, or a member of an object whose
// additionalProperties is a schema, through any oneOf or anyOf branch.
func CheckConfigPath(configSchema json.RawMessage, path string) error {
	if path == "" {
		return fmt.Errorf("the path is empty")
	}
	var schema any
	if len(configSchema) == 0 || json.Unmarshal(configSchema, &schema) != nil {
		return fmt.Errorf("the behavior takes no config")
	}
	at := []any{schema}
	for _, segment := range strings.Split(path, ".") {
		if !configPathSegment.MatchString(segment) {
			return fmt.Errorf("segment %q is neither a member's name, a name followed by [], nor *", segment)
		}
		var next []any
		for _, s := range at {
			for _, branch := range schemaBranches(s) {
				m, _ := branch.(map[string]any)
				switch segment {
				case "*":
					if values, ok := m["additionalProperties"].(map[string]any); ok {
						next = append(next, values)
					}
				default:
					name, each := strings.CutSuffix(segment, "[]")
					properties, _ := m["properties"].(map[string]any)
					member, ok := properties[name]
					if !ok {
						continue
					}
					if !each {
						next = append(next, member)
						continue
					}
					for _, list := range schemaBranches(member) {
						if items, ok := list.(map[string]any)["items"]; ok {
							next = append(next, items)
						}
					}
				}
			}
		}
		if len(next) == 0 {
			return fmt.Errorf("the config schema has no member %s", segment)
		}
		at = next
	}
	return nil
}

// schemaBranches are a schema and each of its oneOf and anyOf branches.
func schemaBranches(schema any) []any {
	out := []any{schema}
	m, ok := schema.(map[string]any)
	if !ok {
		return out
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		if branches, ok := m[key].([]any); ok {
			out = append(out, branches...)
		}
	}
	return out
}

// ConfigValue is a value a ConfigPath reaches in a use's config, and where:
// the path with each * as the member's name and each [] as nothing, as a
// problem with it names it (deadlines.reminder.start.field).
type ConfigValue struct {
	At    string
	Value any
}

// ConfigValues are the values path reaches in config, a use's decoded
// config, in order: an object's members by name, sorted, as the canonical
// config holds them, a list's items in its order. A member the config
// leaves out reaches none.
func ConfigValues(config any, path string) []ConfigValue {
	ordered := configValuesOrdered(config, path)
	out := make([]ConfigValue, len(ordered))
	for i, v := range ordered {
		out[i] = ConfigValue{At: v.at, Value: v.value}
	}
	return out
}

// orderedValue is a value a ConfigPath reaches, where (ConfigValue.At),
// and its position: the index of each list item and object member it
// passes through, so the values of two paths through one list interleave
// item by item when sorted by it.
type orderedValue struct {
	order []int
	at    string
	value any
}

func configValuesOrdered(config any, path string) []orderedValue {
	at := []orderedValue{{value: config}}
	for _, segment := range strings.Split(path, ".") {
		var next []orderedValue
		for _, v := range at {
			m, ok := v.value.(map[string]any)
			if !ok {
				continue
			}
			if segment == "*" {
				keys := make([]string, 0, len(m))
				for key := range m {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for i, key := range keys {
					next = append(next, orderedValue{order: append(slices.Clone(v.order), i), at: joinAt(v.at, key), value: m[key]})
				}
				continue
			}
			name, each := strings.CutSuffix(segment, "[]")
			member, ok := m[name]
			if !ok {
				continue
			}
			where := joinAt(v.at, name)
			if !each {
				next = append(next, orderedValue{order: v.order, at: where, value: member})
				continue
			}
			list, _ := member.([]any)
			for i, item := range list {
				next = append(next, orderedValue{order: append(slices.Clone(v.order), i), at: where, value: item})
			}
		}
		at = next
	}
	return at
}

func joinAt(at, name string) string {
	if at == "" {
		return name
	}
	return at + "." + name
}

// StatesPermissions reports whether the declaration says which operation
// checks which permission: one of its operations lists any (D103's step
// 5). One that says none attributes every permission its config names to
// every operation of its use.
func (d BehaviorDeclaration) StatesPermissions() bool {
	for _, op := range d.Operations {
		if len(op.Permissions) > 0 {
			return true
		}
	}
	return false
}

// OperationPermissions are the permissions op checks under a use's config:
// the strings its permissions' paths reach in config, sorted, each once.
func OperationPermissions(op BehaviorOperation, config json.RawMessage) []string {
	if len(op.Permissions) == 0 || len(config) == 0 {
		return nil
	}
	var decoded any
	if json.Unmarshal(config, &decoded) != nil {
		return nil
	}
	var out []string
	for _, path := range op.Permissions {
		for _, v := range ConfigValues(decoded, path) {
			if s, ok := v.Value.(string); ok && s != "" && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ReferencesOf returns the declaration's references of kind, in order.
func (d BehaviorDeclaration) ReferencesOf(kind string) []BehaviorReference {
	var out []BehaviorReference
	for _, ref := range d.References {
		if ref.Kind == kind {
			out = append(out, ref)
		}
	}
	return out
}

// checkBehaviorPaths holds a declaration's references and its operations'
// permissions to its config schema: each path is a ConfigPath the config
// schema has, and each reference's kind is one of referenceKinds, a type
// only a field's.
func checkBehaviorPaths(decl BehaviorDeclaration) error {
	for _, ref := range decl.References {
		if !slices.Contains(referenceKinds, ref.Kind) {
			return fmt.Errorf("registry: behavior %s reference %s has kind %q, not one of %s", decl.Name, ref.Path, ref.Kind, strings.Join(referenceKinds, ", "))
		}
		if ref.Type != "" && ref.Kind != ReferenceField {
			return fmt.Errorf("registry: behavior %s reference %s names type %s, which only a field's does", decl.Name, ref.Path, ref.Type)
		}
		if err := CheckConfigPath(decl.ConfigSchema, ref.Path); err != nil {
			return fmt.Errorf("registry: behavior %s reference %s: %w", decl.Name, ref.Path, err)
		}
	}
	for _, op := range decl.Operations {
		for _, path := range op.Permissions {
			if err := CheckConfigPath(decl.ConfigSchema, path); err != nil {
				return fmt.Errorf("registry: behavior %s operation %s permissions %s: %w", decl.Name, op.Name, path, err)
			}
		}
	}
	return nil
}
