package ormgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// VersionGraph is one version graph's generated shell, versiongraph_<name>.go
// (D17): its descriptor, the typed <Name>Graph and the SQL the shared
// machinery in versiongraph.go runs against its tables.
type VersionGraph struct {
	Name     string // PascalCase graph name, e.g. "Recipe"
	FileName string // snake_case graph name, e.g. "recipe"

	// Descriptor is the graph descriptor JSON the core reads.
	Descriptor  string
	SchemaEpoch int64

	RefType    string // e.g. "RecipeRef"
	CommitType string // e.g. "RecipeCommit"

	Kinds []VersionGraphKind

	InsertRef     string
	ReadRef       string
	LockRef       string
	TouchRef      string
	LockRoot      string
	NextSequence  string
	InsertCommit  string
	InsertPatches string
	Walk          string
	Patches       string
	CommitRoot    string
	RefCommits    string
}

// VersionGraphKind is one member kind of a graph, the Go shapes its typed
// upsert fills, and its SQL.
type VersionGraphKind struct {
	Kind     string // descriptor kind name, e.g. "step"
	TypeName string // member type, e.g. "Step"
	IDColumn string // row id column, as to_jsonb names it

	KeyGoName  string // the member's key field, e.g. "Id"
	KeyPointer bool   // the key field is a pointer (an auto-generated key)

	RootGoName     string // the member's relation to the root, e.g. "Recipe"
	RootGoType     string // the root's Go type, e.g. "types.Recipe"
	RootPointer    bool   // the relation field is a pointer (an optional relation)
	RootKeyGoName  string // the root's key field, e.g. "Id"
	RootKeyPointer bool   // the root's key field is a pointer

	RefGoName     string // the member's ref relation field, "Ref"
	RefKeyPointer bool   // the ref's key field is a pointer

	OwnRows string
	Slot    string
	Images  string
	Upsert  string
}

// versionGraphs builds the shell of every graph schema declares.
func versionGraphs(schema *ir.Schema, repositories []Repository) ([]VersionGraph, error) {
	byType := make(map[string]*Repository, len(repositories))
	for i := range repositories {
		byType[repositories[i].TypeName] = &repositories[i]
	}
	var graphs []VersionGraph
	for _, g := range graphdesc.Graphs(schema) {
		descriptor, err := g.Descriptor.JSON()
		if err != nil {
			return nil, err
		}
		root := byType[g.Root.Name]
		ref := byType[g.Name+"Ref"]
		commit := byType[g.Name+"Commit"]
		patch := byType[g.Name+"Patch"]
		if root == nil || ref == nil || commit == nil || patch == nil {
			return nil, fmt.Errorf("ormgen: version graph %s is missing its root, ref, commit or patch table", g.Name)
		}
		vg := VersionGraph{
			Name:        g.Name,
			FileName:    g.FileName,
			Descriptor:  strings.TrimSuffix(string(descriptor), "\n"),
			SchemaEpoch: g.Root.VersionGraph.SchemaEpoch,
			RefType:     ref.TypeName,
			CommitType:  commit.TypeName,
		}
		vg.graphSQL(root, ref.QuotedTableName, commit.QuotedTableName, patch.QuotedTableName)
		for _, member := range g.Members {
			repo := byType[member.Type.Name]
			if repo == nil || !repo.Versioned {
				return nil, fmt.Errorf("ormgen: version graph %s member %s has no versioned table", g.Name, member.Type.Name)
			}
			kind := memberSQL(member, repo)
			if err := kind.goShapes(member, repo, root); err != nil {
				return nil, fmt.Errorf("ormgen: version graph %s: %w", g.Name, err)
			}
			vg.Kinds = append(vg.Kinds, kind)
		}
		graphs = append(graphs, vg)
	}
	return graphs, nil
}

// graphSQL fills the statements that read and write a graph's refs,
// commits and patches.
func (vg *VersionGraph) graphSQL(root *Repository, ref, commit, patch string) {
	vg.ReadRef = `SELECT root_id::text, COALESCE(parent_ref_id::text, ''), COALESCE(base_commit_id::text, ''), ` +
		`COALESCE(head_commit_id::text, ''), sealed_at IS NOT NULL, deleted_at IS NOT NULL, _version FROM ` + ref + ` WHERE id = $1::uuid`
	vg.LockRef = vg.ReadRef + ` FOR UPDATE`
	vg.InsertRef = `INSERT INTO ` + ref + ` (root_id, parent_ref_id, base_commit_id, "name", created_by, updated_by) ` +
		`VALUES ($1::uuid, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, $5, $5) RETURNING id::text`
	vg.TouchRef = `UPDATE ` + ref + ` SET head_commit_id = COALESCE(NULLIF($3, '')::uuid, head_commit_id), ` +
		`sealed_at = CASE WHEN $4::boolean THEN now() ELSE sealed_at END, updated_at = now(), updated_by = $5 ` +
		`WHERE id = $1::uuid AND _version = $2 RETURNING _version`
	// FOR NO KEY UPDATE conflicts with itself and not with the key-share
	// locks that inserting a row that references the root takes.
	vg.LockRoot = `SELECT 1 FROM ` + root.QuotedTableName + ` WHERE ` + root.QuotedPrimaryKey + ` = $1::uuid FOR NO KEY UPDATE`
	vg.NextSequence = `SELECT COALESCE(MAX("sequence"), 0) + 1 FROM ` + commit + ` WHERE root_id = $1::uuid`
	vg.InsertCommit = `INSERT INTO ` + commit + ` (root_id, ref_id, parent_commit_id, message, schema_epoch, content_hash, "sequence", created_by) ` +
		`VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8) RETURNING id::text`
	vg.InsertPatches = `INSERT INTO ` + patch + ` (commit_id, entity_kind, entity_key, entity_id, entity_version, operation) ` +
		`SELECT $1::uuid, p.kind, p.key::uuid, p.id::uuid, p.version, p.op ` +
		`FROM jsonb_to_recordset($2::jsonb) AS p(kind text, key text, id text, version bigint, op text)`
	vg.Walk = `WITH RECURSIVE chain AS (` +
		`SELECT id, parent_commit_id, schema_epoch, 1 AS depth FROM ` + commit + ` WHERE id = $1::uuid ` +
		`UNION ALL SELECT c.id, c.parent_commit_id, c.schema_epoch, chain.depth + 1 FROM ` + commit + ` AS c ` +
		`JOIN chain ON c.id = chain.parent_commit_id WHERE chain.depth < $2` +
		`) SELECT id::text, COALESCE(parent_commit_id::text, ''), schema_epoch FROM chain ORDER BY depth`
	vg.Patches = `SELECT DISTINCT ON (p.entity_kind, p.entity_key) p.entity_kind, p.entity_id::text, p.entity_version, p.operation ` +
		`FROM ` + patch + ` AS p JOIN unnest($1::text[]::uuid[]) WITH ORDINALITY AS c(id, depth) ON p.commit_id = c.id ` +
		`ORDER BY p.entity_kind, p.entity_key, c.depth`
	vg.CommitRoot = `SELECT root_id::text FROM ` + commit + ` WHERE id = $1::uuid`
	vg.RefCommits = `WITH RECURSIVE chain AS (` +
		`SELECT id, parent_commit_id, 1 AS depth FROM ` + commit + ` WHERE id = $1::uuid AND ref_id = $2::uuid ` +
		`UNION ALL SELECT c.id, c.parent_commit_id, chain.depth + 1 FROM ` + commit + ` AS c ` +
		`JOIN chain ON c.id = chain.parent_commit_id WHERE c.ref_id = $2::uuid AND chain.depth < $3` +
		`) SELECT id::text FROM chain ORDER BY depth`
}

// memberSQL builds a member kind's statements from its repository's
// columns. The upsert writes a row given as JSON: jsonb_populate_record
// casts each value to its column's type, and the ref, root, tombstone and
// audit columns come from the arguments rather than the row.
func memberSQL(member graphdesc.Member, repo *Repository) VersionGraphKind {
	table := repo.QuotedTableName
	kind := VersionGraphKind{
		Kind:     member.Kind,
		TypeName: repo.TypeName,
		IDColumn: repo.PrimaryKeyCol,
		OwnRows:  `SELECT to_jsonb(t) FROM ` + table + ` AS t WHERE t.ref_id = $1::uuid`,
		Slot:     `SELECT t.` + repo.QuotedPrimaryKey + `::text FROM ` + table + ` AS t WHERE t.entity_key = $1::uuid AND t.ref_id = $2::uuid`,
		Images: `SELECT h.` + repo.HistoryDataCol + ` FROM ` + repo.QuotedHistoryTableName + ` AS h ` +
			`JOIN unnest($1::text[]::uuid[], $2::bigint[]) AS p(id, version) ON h.` + repo.QuotedPrimaryKey + ` = p.id AND h._version = p.version`,
	}

	var columns, values, updates []string
	for _, col := range repo.OrderedMembers {
		var column string
		switch {
		case col.IsRelationship:
			column = col.Relationship.DBColumnName
		case col.Field.IsPrimaryKey || col.Field.IsInternalMetadata:
			continue
		default:
			column = col.Field.DBName
		}
		quoted := sqlutil.QuoteIdentifier(column)
		columns = append(columns, quoted)
		values = append(values, "r."+quoted)
		switch column {
		case graphdesc.EntityKeyColumn, graphdesc.RefColumn, member.RootColumn, "created_at", "created_by":
			// The conflict key, the root and the creation audit stay as
			// the row was first written.
		default:
			updates = append(updates, quoted+" = EXCLUDED."+quoted)
		}
	}
	// jsonb_populate_record ignores a key the table has no column for, so
	// every kind sets all four audit keys and every statement uses $5.
	set := `jsonb_build_object('ref_id', $2::text, '` + member.RootColumn + `', $3::text, 'deleted_on_ref', $4::boolean, ` +
		`'created_at', now(), 'created_by', $5::text, 'updated_at', now(), 'updated_by', $5::text)`
	kind.Upsert = `INSERT INTO ` + table + ` AS t (` + strings.Join(columns, ", ") + `) ` +
		`SELECT ` + strings.Join(values, ", ") + ` FROM jsonb_populate_record(NULL::` + table + `, $1::jsonb || ` + set + `) AS r ` +
		`ON CONFLICT (entity_key, ref_id) DO UPDATE SET ` + strings.Join(updates, ", ") + ` RETURNING to_jsonb(t)`
	return kind
}

// goShapes reads the Go shapes of the fields a typed upsert sets: the
// member's key, its relation to the root and its ref.
func (k *VersionGraphKind) goShapes(member graphdesc.Member, repo, root *Repository) error {
	for _, field := range repo.Fields {
		if field.IsPrimaryKey {
			k.KeyGoName = codegen.ToPascalCase(field.Name)
			k.KeyPointer = field.DerefValue || field.IsAutoGenerated
		}
	}
	for _, field := range root.Fields {
		if field.IsPrimaryKey {
			k.RootKeyGoName = codegen.ToPascalCase(field.Name)
		}
	}
	for _, rel := range repo.Relationships {
		switch rel.DBColumnName {
		case member.RootColumn:
			k.RootGoName = codegen.ToPascalCase(rel.FieldName)
			k.RootGoType = "types." + rel.TargetType
			k.RootPointer = !rel.IsRequired
			k.RootKeyPointer = rel.TargetPrimaryKeyIsPointer
		case graphdesc.RefColumn:
			k.RefGoName = codegen.ToPascalCase(rel.FieldName)
			k.RefKeyPointer = rel.TargetPrimaryKeyIsPointer
		}
	}
	if k.KeyGoName == "" || k.RootGoName == "" || k.RootKeyGoName == "" || k.RefGoName == "" {
		return fmt.Errorf("member %s lacks a key, a relation to the root or a ref", repo.TypeName)
	}
	return nil
}
