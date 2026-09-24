package apigen

import (
	"sort"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// The vendor-extension keys the SDK tool documents carry.
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
