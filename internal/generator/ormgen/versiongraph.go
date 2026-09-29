package ormgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	ir "github.com/parable-work/superschematic/ir"
)

// VersionGraph is one version graph's generated facade,
// versiongraph_<name>.go (D19): its descriptor and the typed <Name>Graph,
// which turns typed edits into canonical rows for the version-graph engine
// (runtime/versiongraph/go/engine) and the rows it returns into typed trees.
type VersionGraph struct {
	Name     string // PascalCase graph name, e.g. "Recipe"
	FileName string // snake_case graph name, e.g. "recipe"

	// Descriptor is the graph descriptor JSON the engine and its Postgres
	// adapter read.
	Descriptor    string
	SchemaEpoch   int64
	SnapshotEvery int64

	RefType     string // e.g. "RecipeRef"
	CommitType  string // e.g. "RecipeCommit"
	ReleaseType string // e.g. "RecipeRelease"

	Kinds []VersionGraphKind
}

// VersionGraphKind is one member kind of a graph: its descriptor name, its
// type and the columns its canonical row takes from a typed value.
type VersionGraphKind struct {
	Kind     string // descriptor kind name, e.g. "step"
	TypeName string // member type, e.g. "Step"

	// Columns are the columns a typed upsert writes: every column but the
	// row id, the version, the ref, the root, the tombstone and the audit
	// columns, which the engine and its adapter write.
	Columns []VersionGraphColumn
}

// VersionGraphColumn is one column of a typed upsert's canonical row, whose
// value is the schema runtime's JSON of a field of the typed value.
type VersionGraphColumn struct {
	Column string // column name, e.g. "step_key"
	// Value is the Go expression of the column's value, of a *types.<Type>
	// named v: the field, or a relation's target key.
	Value string
	// Optional is set for a value that is null when it is zero, as
	// CreateOne leaves an unset optional field out: an optional field, a
	// relation's key, and the entity key, whose absence makes a new
	// entity.
	Optional bool
	// Guard, when set, is a pointer the value is read through: an optional
	// relation. A nil one is null.
	Guard string
}

// versionGraphs builds the facade of every graph schema declares.
func versionGraphs(schema *ir.Schema, repositories []Repository) ([]VersionGraph, error) {
	byType := make(map[string]*Repository, len(repositories))
	for i := range repositories {
		byType[repositories[i].TypeName] = &repositories[i]
	}
	described, err := graphdesc.Graphs(schema)
	if err != nil {
		return nil, err
	}
	var graphs []VersionGraph
	for _, g := range described {
		descriptor, err := g.Descriptor.JSON()
		if err != nil {
			return nil, err
		}
		ref := byType[g.Name+"Ref"]
		commit := byType[g.Name+"Commit"]
		release := byType[g.Name+"Release"]
		if byType[g.Root.Name] == nil || ref == nil || commit == nil || release == nil ||
			byType[g.Name+"Patch"] == nil || byType[g.Name+"SnapshotEntry"] == nil {
			return nil, fmt.Errorf("ormgen: version graph %s is missing its root, ref, commit, patch, release or snapshot entry table", g.Name)
		}
		vg := VersionGraph{
			Name:          g.Name,
			FileName:      g.FileName,
			Descriptor:    strings.TrimSuffix(string(descriptor), "\n"),
			SchemaEpoch:   g.Root.VersionGraph.SchemaEpoch,
			SnapshotEvery: g.Root.VersionGraph.SnapshotInterval(),
			RefType:       ref.TypeName,
			CommitType:    commit.TypeName,
			ReleaseType:   release.TypeName,
		}
		for _, member := range g.Members {
			repo := byType[member.Type.Name]
			if repo == nil || !repo.Versioned {
				return nil, fmt.Errorf("ormgen: version graph %s member %s has no versioned table", g.Name, member.Type.Name)
			}
			vg.Kinds = append(vg.Kinds, VersionGraphKind{
				Kind:     member.Kind,
				TypeName: repo.TypeName,
				Columns:  rowColumns(member, repo),
			})
		}
		graphs = append(graphs, vg)
	}
	return graphs, nil
}

// rowColumns lists the columns a typed upsert of a member writes, in the
// table's column order.
func rowColumns(member graphdesc.Member, repo *Repository) []VersionGraphColumn {
	var columns []VersionGraphColumn
	for _, col := range repo.OrderedMembers {
		if col.IsRelationship {
			rel := col.Relationship
			if rel.IsArray || rel.DBColumnName == member.RootColumn || rel.DBColumnName == graphdesc.RefColumn {
				// The engine writes the ref and its root.
				continue
			}
			field := "v." + codegen.ToPascalCase(rel.FieldName)
			column := VersionGraphColumn{Column: rel.DBColumnName, Value: field + ".Id", Optional: true}
			if !rel.IsRequired {
				column.Guard = field
			}
			columns = append(columns, column)
			continue
		}
		field := col.Field
		if field.IsPrimaryKey || field.IsInternalMetadata || field.IsAuditField || field.DBName == graphdesc.TombstoneColumn {
			// The adapter mints a row id for each ref's row, and writes the
			// version, the tombstone and the audit columns.
			continue
		}
		columns = append(columns, VersionGraphColumn{
			Column:   field.DBName,
			Value:    "v." + codegen.ToPascalCase(field.Name),
			Optional: !field.IsRequired || field.DBName == graphdesc.EntityKeyColumn,
		})
	}
	return columns
}
