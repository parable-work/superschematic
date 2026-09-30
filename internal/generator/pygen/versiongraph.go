package pygen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// VersionGraphInfo is one version graph's typed facade (D19),
// <module>/versiongraph_<name>.py: its descriptor and a <Name>Graph over the
// Python engine (Naming.VersionGraphPythonModule), which turns typed edits
// into canonical rows through the models' JSON and the rows the engine
// returns into typed trees.
type VersionGraphInfo struct {
	Name     string // PascalCase graph name, e.g. "Recipe"
	FileName string // snake_case graph name, e.g. "recipe"
	// ConstPrefix prefixes the facade's constants, e.g. "RECIPE".
	ConstPrefix string

	// Descriptor is the graph descriptor JSON the engine and its Postgres
	// adapter read, as a Python string literal.
	Descriptor    string
	SchemaEpoch   int64
	SnapshotEvery int64

	Kinds []VersionGraphKindInfo
}

// VersionGraphKindInfo is one member kind of a graph: its descriptor name,
// the attribute of the graph's tree and edits that holds its rows, the model
// that types them, and its columns.
type VersionGraphKindInfo struct {
	Kind     string // descriptor kind name, e.g. "step"
	Member   string // tree and edits attribute, e.g. "step"
	TypeName string // member model, e.g. "Step"

	// Columns are every column of the kind's rows, with the field of the
	// typed value that holds each.
	Columns []VersionGraphColumnInfo
}

// VersionGraphColumnInfo is one column of a kind's canonical rows and the
// typed field, by its JSON name, that holds it.
type VersionGraphColumnInfo struct {
	Column string // column name, e.g. "step_key"
	Field  string // the field's JSON name, e.g. "stepKey"
	// RelationKey is, for a to-one relation, the target's key field: the
	// field holds the target, and the column its key.
	RelationKey string
	// Write is set for a column a typed upsert writes: every column but the
	// row id, the version, the ref, the root, the tombstone and the audit
	// columns, which the engine and its adapter write.
	Write bool
}

// versionGraphs builds the facade of every graph schema declares.
func versionGraphs(schema *ir.Schema) ([]VersionGraphInfo, error) {
	described, err := graphdesc.Graphs(schema)
	if err != nil {
		return nil, err
	}
	graphs := make([]VersionGraphInfo, 0, len(described))
	for _, g := range described {
		descriptor, err := g.Descriptor.JSON()
		if err != nil {
			return nil, err
		}
		vg := VersionGraphInfo{
			Name:          g.Name,
			FileName:      g.FileName,
			ConstPrefix:   strings.ToUpper(g.FileName),
			Descriptor:    pyTripleQuoted(strings.TrimSuffix(string(descriptor), "\n")),
			SchemaEpoch:   g.Root.VersionGraph.SchemaEpoch,
			SnapshotEvery: g.Root.VersionGraph.SnapshotInterval(),
		}
		members := map[string]string{}
		for i, member := range g.Members {
			kind, err := versionGraphKind(schema, member, g.Descriptor.Kinds[i])
			if err != nil {
				return nil, fmt.Errorf("pygen: version graph %s: %w", g.Name, err)
			}
			if other, taken := members[kind.Member]; taken {
				return nil, fmt.Errorf("pygen: version graph %s: kinds %s and %s both name the attribute %s", g.Name, other, kind.Kind, kind.Member)
			}
			members[kind.Member] = kind.Kind
			vg.Kinds = append(vg.Kinds, kind)
		}
		graphs = append(graphs, vg)
	}
	return graphs, nil
}

// versionGraphKind lists a member's columns in field order, and the version
// column, which the loader adds to the table rather than to the type.
func versionGraphKind(schema *ir.Schema, member graphdesc.Member, described graphdesc.Kind) (VersionGraphKindInfo, error) {
	td := member.Type
	kind := VersionGraphKindInfo{
		Kind:     member.Kind,
		Member:   kindMember(member.Kind),
		TypeName: td.Name,
	}
	seen := map[string]bool{}
	for _, fd := range td.Fields {
		column := graphdesc.Column(schema, fd)
		if _, ok := described.Columns[column]; !ok {
			// A list of relations, which a join or the other table holds.
			continue
		}
		seen[column] = true
		info := VersionGraphColumnInfo{Column: column, Field: fd.Name}
		if target := schema.Types[fd.TypeRef.Name]; target != nil && target.Role == ir.RoleDBTable && !target.JsonField &&
			!fd.JsonField && !fd.TypeRef.IsArray && !fd.TypeRef.IsMap {
			key := keyFieldName(target)
			if key == "" {
				return VersionGraphKindInfo{}, fmt.Errorf("%s.%s: the relation to %s needs a key field", td.Name, fd.Name, target.Name)
			}
			info.RelationKey = key
		}
		switch {
		case fd.Key, fd.InternalMetadata, isAuditField(fd.Name):
			// The adapter mints a row id for each ref's row, and writes the
			// version and the audit columns.
		case column == member.RootColumn, column == graphdesc.RefColumn, column == graphdesc.TombstoneColumn:
			// The engine writes the root, the ref and the tombstone.
		default:
			info.Write = true
		}
		kind.Columns = append(kind.Columns, info)
	}
	if !seen[described.Version] {
		kind.Columns = append(kind.Columns, VersionGraphColumnInfo{Column: described.Version, Field: described.Version})
	}
	return kind, nil
}

// kindMember is the attribute of a graph's tree and edits that holds a
// kind: its name in snake case, with a trailing underscore where that is a
// Python keyword or one of the tree's own attributes.
func kindMember(kind string) string {
	member := codegen.ToSnakeCase(kind)
	if _, keyword := pythonKeywords[member]; keyword || member == "content_hash" || member == "findings" {
		member += "_"
	}
	return member
}

// keyFieldName is a table's @key field, else its field named id, or "".
func keyFieldName(td *ir.TypeDef) string {
	for _, fd := range td.Fields {
		if fd.Key {
			return fd.Name
		}
	}
	for _, fd := range td.Fields {
		if fd.Name == "id" {
			return fd.Name
		}
	}
	return ""
}

// isAuditField reports whether a field is an audit field the adapter
// writes, as the ORM generator names them.
func isAuditField(name string) bool {
	switch name {
	case "createdAt", "createdBy", "updatedAt", "updatedBy", "deletedAt", "deletedBy":
		return true
	}
	return false
}

// pyTripleQuoted writes s as a Python triple-quoted string literal: a
// backslash is escaped, and so is a quote that would end the literal.
func pyTripleQuoted(s string) string {
	var b strings.Builder
	b.WriteString(`"""`)
	quotes := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			b.WriteString(`\\`)
			quotes = 0
		case c == '"' && (quotes == 2 || i == len(s)-1):
			b.WriteString(`\"`)
			quotes = 0
		case c == '"':
			b.WriteByte(c)
			quotes++
		default:
			b.WriteByte(c)
			quotes = 0
		}
	}
	b.WriteString(`"""`)
	return b.String()
}

// SetVersionGraphPath computes the uv path source for the version graph's
// Python package (Naming.VersionGraphPyPIDist) relative to outputDir, from
// [paths] versiongraph_python. An unset path leaves it empty, and
// pyproject.toml names the dependency without a source, as it does the
// scalar library.
func SetVersionGraphPath(output *ModuleOutput, paths naming.LocalPaths, outputDir string) error {
	rel, err := naming.RelPath(outputDir, paths.VersionGraphPython)
	if err != nil {
		return fmt.Errorf("version graph Python package path: %w", err)
	}
	output.VersionGraphDepPath = rel
	return nil
}

// versionGraphFileName is the module of a graph's facade inside the
// package.
func versionGraphFileName(graph VersionGraphInfo) string {
	return "versiongraph_" + codegen.ToSnakeCase(graph.FileName) + ".py"
}
