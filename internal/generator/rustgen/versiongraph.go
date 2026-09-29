package rustgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// VersionGraphInfo is one version graph's typed facade,
// src/versiongraph_<name>.rs (D19): its descriptor and the <Name>Graph
// that runs each operation on the version graph's Rust engine, turning
// typed edits into canonical rows through the types' serde JSON and
// canonical rows back into typed trees.
type VersionGraphInfo struct {
	Name     string // PascalCase graph name, e.g. "Recipe"
	FileName string // snake_case graph name, e.g. "recipe"
	// ConstPrefix prefixes the facade's constants, e.g. "RECIPE".
	ConstPrefix string

	// Descriptor is the graph descriptor as a Rust raw string literal.
	Descriptor    string
	SchemaEpoch   int64
	SnapshotEvery int64

	// KeyType is the Rust type of an entity key, a root and a ref or commit
	// id: the member types' entity key type, e.g. "IdentityUUID".
	KeyType string
	// KindEnum and OperationEnum are the generated enums of the graph's
	// entity kinds and patch operations.
	KindEnum      string
	OperationEnum string

	Kinds []VersionGraphKindInfo
}

// VersionGraphKindInfo is one member kind of a graph: its descriptor name,
// its field in the facade's tree and edits, its type and the columns its
// canonical row maps to the type's fields.
type VersionGraphKindInfo struct {
	Kind      string // descriptor kind name, e.g. "step"
	FieldName string // Rust field of the kind in <Name>Tree and <Name>Edits
	TypeName  string // member type, e.g. "Step"
	// ConstPrefix prefixes the kind's column tables, e.g. "RECIPE_STEP".
	ConstPrefix string

	// Writes are the columns a typed upsert writes: every column but the
	// row id, the version, the ref, the root, the tombstone and the audit
	// columns, which the engine and its adapter write.
	Writes []VersionGraphColumnInfo
	// Reads are the columns a canonical row gives a typed value: every
	// column but a to-one relation's, whose target the row does not hold.
	Reads []VersionGraphColumnInfo
}

// VersionGraphColumnInfo pairs a column with the field of the typed value
// that holds it, by the field's JSON name. A relation's column holds its
// target's key.
type VersionGraphColumnInfo struct {
	Column   string
	Field    string
	Relation bool
}

// versionGraphs builds the facade of every graph schema declares. types
// are the generated structs, which give each member's entity key type.
func versionGraphs(schema *ir.Schema, types []TypeInfo) ([]VersionGraphInfo, error) {
	described, err := graphdesc.Graphs(schema)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]*TypeInfo, len(types))
	for i := range types {
		byName[types[i].Name] = &types[i]
	}
	var graphs []VersionGraphInfo
	for _, g := range described {
		descriptor, err := g.Descriptor.JSON()
		if err != nil {
			return nil, err
		}
		vg := VersionGraphInfo{
			Name:          g.Name,
			FileName:      g.FileName,
			ConstPrefix:   strings.ToUpper(g.FileName),
			Descriptor:    rawString(strings.TrimSuffix(string(descriptor), "\n")),
			SchemaEpoch:   g.Root.VersionGraph.SchemaEpoch,
			SnapshotEvery: g.Root.VersionGraph.SnapshotInterval(),
			KindEnum:      g.Name + "EntityKind",
			OperationEnum: g.Name + "PatchOperation",
		}
		for _, member := range g.Members {
			typ := byName[member.Type.Name]
			if typ == nil {
				return nil, fmt.Errorf("rustgen: version graph %s member %s has no generated struct", g.Name, member.Type.Name)
			}
			keyType, err := entityKeyType(typ)
			if err != nil {
				return nil, fmt.Errorf("rustgen: version graph %s: %w", g.Name, err)
			}
			if vg.KeyType == "" {
				vg.KeyType = keyType
			} else if vg.KeyType != keyType {
				return nil, fmt.Errorf("rustgen: version graph %s: member %s's entity key is %s, another member's %s", g.Name, typ.Name, keyType, vg.KeyType)
			}
			writes, reads := graphColumns(schema, member)
			for _, field := range typ.Fields {
				if field.IsVersion {
					// The version field is the types' own, not the schema's.
					reads = append(reads, VersionGraphColumnInfo{Column: graphdesc.VersionColumn, Field: field.Name})
				}
			}
			vg.Kinds = append(vg.Kinds, VersionGraphKindInfo{
				Kind:        member.Kind,
				FieldName:   toRustFieldName(member.Kind),
				TypeName:    typ.Name,
				ConstPrefix: vg.ConstPrefix + "_" + strings.ToUpper(member.Kind),
				Writes:      writes,
				Reads:       reads,
			})
		}
		graphs = append(graphs, vg)
	}
	return graphs, nil
}

// entityKeyType is the Rust type of a member's entity key, without its
// Option.
func entityKeyType(typ *TypeInfo) (string, error) {
	for _, field := range typ.Fields {
		if field.Name == "entityKey" {
			return strings.TrimSuffix(strings.TrimPrefix(field.RustType, "Option<"), ">"), nil
		}
	}
	return "", fmt.Errorf("member %s has no entityKey field", typ.Name)
}

// graphColumns lists the columns of a member's canonical row a typed
// upsert writes, and the ones a typed value reads back, in field order.
func graphColumns(schema *ir.Schema, member graphdesc.Member) (writes, reads []VersionGraphColumnInfo) {
	for _, fd := range member.Type.Fields {
		target := schema.Types[fd.TypeRef.Name]
		isTable := target != nil && target.Role == ir.RoleDBTable && !target.JsonField && !fd.JsonField
		if isTable && fd.TypeRef.IsArray && !fd.TypeRef.IsMap {
			// A list of relations has no column: the other table holds it.
			continue
		}
		column := graphdesc.Column(schema, fd)
		if isTable && !fd.TypeRef.IsMap {
			if column == member.RootColumn || column == graphdesc.RefColumn {
				// The engine writes the ref and its root.
				continue
			}
			writes = append(writes, VersionGraphColumnInfo{Column: column, Field: fd.Name, Relation: true})
			continue
		}
		info := VersionGraphColumnInfo{Column: column, Field: fd.Name}
		reads = append(reads, info)
		if fd.Key || fd.InternalMetadata || isAuditField(fd.Name) || column == graphdesc.TombstoneColumn {
			// The adapter mints a row id for each ref's row, and writes the
			// version, the tombstone and the audit columns.
			continue
		}
		writes = append(writes, info)
	}
	return writes, reads
}

// isAuditField reports whether a field is one of the audit fields the
// adapter writes on every row.
func isAuditField(name string) bool {
	switch name {
	case "createdAt", "createdBy", "updatedAt", "updatedBy":
		return true
	}
	return false
}

// rawString writes s as a Rust raw string literal with enough #s that no
// "#... sequence in s ends it.
func rawString(s string) string {
	hashes := "#"
	for strings.Contains(s, `"`+hashes) {
		hashes += "#"
	}
	return "r" + hashes + `"` + s + `"` + hashes
}

// SetVersionGraphPath computes the Cargo.toml path entry for the version
// graph's Rust engine crate relative to outputDir. An unset path emits no
// path entry, and the manifest names the crate's version.
func SetVersionGraphPath(output *ModuleOutput, paths naming.LocalPaths, outputDir string) error {
	rel, err := naming.RelPath(outputDir, paths.VersionGraphRust)
	if err != nil {
		return fmt.Errorf("version graph engine crate path: %w", err)
	}
	output.VersionGraphDepPath = rel
	return nil
}

// versionGraphFileName is the source file of a graph's facade under src/.
func versionGraphFileName(graph VersionGraphInfo) string {
	return "versiongraph_" + codegen.ToSnakeCase(graph.FileName) + ".rs"
}
