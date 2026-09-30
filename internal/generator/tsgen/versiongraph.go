package tsgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// VersionGraphInfo is one version graph's typed facade (D19),
// versiongraph/<name>.ts: its descriptor and a <Name>Graph over the
// TypeScript engine (Naming.VersionGraphNpmPackage), which turns typed edits
// into canonical rows and the rows the engine returns into typed trees.
type VersionGraphInfo struct {
	Name     string // PascalCase graph name, e.g. "Recipe"
	FileName string // snake_case graph name, e.g. "recipe"

	// Descriptor is the graph descriptor JSON the engine and its Postgres
	// adapter read, written as the object literal it also is.
	Descriptor    string
	SchemaEpoch   int64
	SnapshotEvery int64

	Kinds []VersionGraphKindInfo
}

// VersionGraphKindInfo is one member kind of a graph: its descriptor name,
// the tree member and the type that holds its rows, and its columns.
type VersionGraphKindInfo struct {
	Kind     string // descriptor kind name, e.g. "step"
	Member   string // tree member, the type name in lowerCamel case, e.g. "step"
	TypeName string // member type, e.g. "Step"

	// Columns are every column of the kind's rows, with the field of the
	// typed value that holds each.
	Columns []VersionGraphColumnInfo
}

// VersionGraphColumnInfo is one column of a kind's canonical rows and the
// typed field it holds.
type VersionGraphColumnInfo struct {
	Column string // column name, e.g. "step_key"
	Field  string // typed field, e.g. "stepKey"
	// RelationKey is, for a to-one relation, the target's key field: the
	// field holds an object whose key field is the column's value.
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
			Descriptor:    strings.TrimSuffix(string(descriptor), "\n"),
			SchemaEpoch:   g.Root.VersionGraph.SchemaEpoch,
			SnapshotEvery: g.Root.VersionGraph.SnapshotInterval(),
		}
		for i, member := range g.Members {
			kind, err := versionGraphKind(schema, member, g.Descriptor.Kinds[i])
			if err != nil {
				return nil, fmt.Errorf("tsgen: version graph %s: %w", g.Name, err)
			}
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
		Member:   lowerFirst(td.Name),
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

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// SetVersionGraphLibSpec computes the package.json dependency spec for the
// version-graph runtime's npm package (Naming.VersionGraphNpmPackage) as a
// file: path relative to outputDir, from [paths] versiongraph_typescript.
// An unset path leaves the spec empty so package.json names the published
// version instead. A package whose schema declares no graph has no such
// dependency.
func SetVersionGraphLibSpec(output *ModuleOutput, paths naming.LocalPaths, outputDir string) error {
	rel, err := naming.RelPath(outputDir, paths.VersionGraphTypeScript)
	if err != nil {
		return fmt.Errorf("version-graph package path: %w", err)
	}
	output.VersionGraphLibSpec = ""
	if rel != "" {
		output.VersionGraphLibSpec = "file:" + rel
	}
	return nil
}
