package apigen

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// The vendor-extension keys the SDK tool documents carry by default. A
// distribution renames them, or adds its own, with a ToolHook.
const (
	// DefaultToolScalarKey is the key a tool argument property carries its
	// canonical scalar name under ("Identity.UUID").
	DefaultToolScalarKey = "x-superschematic-scalar"
	// DefaultToolGuidanceKey is the MCP _meta key a visible tool carries its
	// @docs guidance (useWhen, doNotUseWhen, success, errors) under.
	DefaultToolGuidanceKey = "superschematic/operation-guidance"
)

// ToolKeys are the vendor-extension keys the SDK tool documents
// (tools/schema.json, tools/index.ts, the provider tool lists) are written
// with.
type ToolKeys struct {
	// Scalar is the key a tool argument property carries its canonical
	// scalar name under. Empty leaves the name out.
	Scalar string
	// Guidance is the MCP _meta key a visible tool carries its @docs
	// guidance under. Empty leaves the guidance out of _meta; the tool
	// documents' own guidance member is written either way.
	Guidance string
	// Parameters are written at the root of every tool's argument schema,
	// in order: first in the encoding the input schema digest hashes, and
	// after additionalProperties in the rendered documents. The core adds
	// none.
	Parameters []ToolKeyValue
}

// ToolKeyValue is one key of ToolKeys.Parameters. Value must encode as
// JSON; a number, a string or a boolean also renders as a TypeScript
// literal type in tools/index.ts.
type ToolKeyValue struct {
	Key   string
	Value any
}

// DefaultToolKeys returns the keys the core writes.
func DefaultToolKeys() ToolKeys {
	return ToolKeys{Scalar: DefaultToolScalarKey, Guidance: DefaultToolGuidanceKey}
}

// ToolHook edits what the SDK generators publish about an API's MCP tools:
// the vendor keys of the tool documents and each operation's resolved @mcp
// record. It is how an extension renames the core's keys, adds its own
// argument-schema keys, fills in the family and style of its icon set, or
// adds _meta entries, without a core option. Registered with
// Registry.RegisterToolHook; hooks run in registration order after the api
// generator resolves the records and before it checks them for collisions.
type ToolHook struct {
	// Name identifies the hook in errors.
	Name string
	// Extension is the registering extension's Name().
	Extension string
	// Edit receives the API schema and the tool set and edits the set in
	// place. An error fails the api generator and names the hook.
	Edit func(schema *ir.Schema, tools *ToolSet) error
}

// ToolSet is what a ToolHook edits.
type ToolSet struct {
	// Keys starts as DefaultToolKeys.
	Keys ToolKeys
	// Tools holds one entry per operation of the API, in endpoint order.
	// A hook edits entries; it does not add or remove them.
	Tools []Tool
}

// Tool is one operation as a ToolHook sees it.
type Tool struct {
	// Namespace is the operation's endpoint namespace ("order").
	Namespace string
	// Operation is the schema's operation. Read it; do not change it.
	Operation *ir.FieldDef
	// MCP is the resolved @mcp record, a copy the hook may edit or
	// replace; nil when the operation has no @mcp. The SDK generators
	// publish the record the hooks leave.
	MCP *ir.OperationMCP
}

// reservedToolParameterKeys are the argument-schema keys the core writes;
// ToolKeys.Parameters cannot reuse them.
var reservedToolParameterKeys = []string{"type", "additionalProperties", "properties", "required"}

// applyToolHooks runs hooks over the endpoints' tool set, writes the MCP
// records they leave back onto the endpoints, and returns the keys. With no
// hooks it returns DefaultToolKeys and changes nothing.
func applyToolHooks(schema *ir.Schema, endpoints []EndpointInfo, hooks []ToolHook) (ToolKeys, error) {
	keys := DefaultToolKeys()
	if len(hooks) == 0 {
		return keys, nil
	}
	set := &ToolSet{Keys: keys, Tools: make([]Tool, len(endpoints))}
	for i, endpoint := range endpoints {
		set.Tools[i] = Tool{Namespace: endpoint.Namespace, Operation: endpoint.Operation, MCP: endpoint.MCP}
	}
	for _, hook := range hooks {
		if hook.Edit == nil {
			continue
		}
		if err := hook.Edit(schema, set); err != nil {
			return ToolKeys{}, fmt.Errorf("apigen: tool hook %s: %w", hook.Name, err)
		}
		if len(set.Tools) != len(endpoints) {
			return ToolKeys{}, fmt.Errorf("apigen: tool hook %s: the tool set has %d entries, want %d; a hook edits tools, it does not add or remove them", hook.Name, len(set.Tools), len(endpoints))
		}
		if err := validateToolKeys(set.Keys); err != nil {
			return ToolKeys{}, fmt.Errorf("apigen: tool hook %s: %w", hook.Name, err)
		}
	}
	for i := range endpoints {
		endpoints[i].MCP = set.Tools[i].MCP
	}
	return set.Keys, nil
}

func validateToolKeys(keys ToolKeys) error {
	seen := make(map[string]bool, len(keys.Parameters))
	for _, kv := range keys.Parameters {
		switch {
		case kv.Key == "":
			return fmt.Errorf("a tool parameter key is empty")
		case slices.Contains(reservedToolParameterKeys, kv.Key):
			return fmt.Errorf("tool parameter key %q is written by the core", kv.Key)
		case kv.Key == keys.Scalar:
			return fmt.Errorf("tool parameter key %q is the scalar key", kv.Key)
		case seen[kv.Key]:
			return fmt.Errorf("tool parameter key %q is listed twice", kv.Key)
		}
		seen[kv.Key] = true
		if _, err := json.Marshal(kv.Value); err != nil {
			return fmt.Errorf("tool parameter key %q: value does not encode as JSON: %w", kv.Key, err)
		}
	}
	return nil
}

// ToolUnionInfo describes a union the tool argument schemas render as
// oneOf.
type ToolUnionInfo struct {
	Description   string
	Discriminator string
	Members       []ToolUnionMemberInfo
}

// ToolUnionMemberInfo is one object member of a union and its
// discriminator value.
type ToolUnionMemberInfo struct {
	Name               string
	DiscriminatorValue string
}

// allTypeUnions returns the unions of the schema and its dependencies; the
// schema's own win a name clash, then dependencies in name order.
func (m *typeMapper) allTypeUnions() map[string]ToolUnionInfo {
	result := make(map[string]ToolUnionInfo)
	add := func(schema *ir.Schema) {
		if schema == nil {
			return
		}
		for _, union := range codegen.ExtractUnions(schema) {
			if _, exists := result[union.Name]; exists {
				continue
			}
			members := make([]ToolUnionMemberInfo, 0, len(union.Members))
			for _, member := range union.Members {
				members = append(members, ToolUnionMemberInfo{Name: member.Name, DiscriminatorValue: member.DiscriminatorValue})
			}
			result[union.Name] = ToolUnionInfo{Description: union.Doc(), Discriminator: union.Discriminator, Members: members}
		}
	}
	add(m.schema)
	names := make([]string, 0, len(m.dependencies))
	for name := range m.dependencies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		add(m.dependencies[name])
	}
	return result
}

// ToolEnumInfo is an enum the tool argument schemas list the values of.
type ToolEnumInfo struct {
	// Values are the serialized values in declaration order.
	Values []string
}

// allTypeEnums returns the enums of the schema and its dependencies; the
// schema's own win a name clash, then dependencies in name order.
func (m *typeMapper) allTypeEnums() map[string]ToolEnumInfo {
	result := make(map[string]ToolEnumInfo)
	add := func(schema *ir.Schema) {
		if schema == nil {
			return
		}
		for _, enum := range codegen.ExtractEnums(schema) {
			if _, exists := result[enum.Name]; exists {
				continue
			}
			values := make([]string, 0, len(enum.Values))
			for _, value := range enum.Values {
				values = append(values, value.Value)
			}
			result[enum.Name] = ToolEnumInfo{Values: values}
		}
	}
	add(m.schema)
	names := make([]string, 0, len(m.dependencies))
	for name := range m.dependencies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		add(m.dependencies[name])
	}
	return result
}

// allTypeFields returns the fields of every type in the schema and its
// dependencies. On a name clash the type with more fields wins; on a tie
// the schema's own, then the dependency first in name order.
func (m *typeMapper) allTypeFields() map[string][]Param {
	result := make(map[string][]Param)
	for name, typeDef := range m.schema.Types {
		if typeDef != nil {
			result[name] = m.fieldsFromTypeDef(typeDef)
		}
	}
	names := make([]string, 0, len(m.dependencies))
	for name := range m.dependencies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, depName := range names {
		dependency := m.dependencies[depName]
		if dependency == nil {
			continue
		}
		for name, typeDef := range dependency.Types {
			if typeDef == nil {
				continue
			}
			fields := m.fieldsFromTypeDef(typeDef)
			if current, exists := result[name]; !exists || len(fields) > len(current) {
				result[name] = fields
			}
		}
	}
	return result
}
