// Package versiongraph expands the version graph declarations of a verified
// schema into ordinary types, as the frontends copy a trait's fields onto a
// type. A @versionGraph root gets its ref, commit, patch, release and
// snapshot entry tables and two enums; each @graphMember gets its
// entityKey, ref and deletedOnRef fields, a unique (entityKey, ref) index
// and, when it prunes history, pins that keep every row version a patch or
// a snapshot entry names.
//
// The sql, orm and types generators emit the result with no graph-specific
// code. Every definition the expansion adds carries
// ir.OriginVersionGraph, so the schema writer can skip it and write the
// declarations instead.
package versiongraph

import (
	"sort"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// TypeSuffixes are the suffixes of the types and enums a version graph
// generates, appended to the graph's name.
var TypeSuffixes = []string{"Ref", "Commit", "Patch", "Release", "SnapshotEntry", "EntityKind", "PatchOperation"}

// MemberFields are the fields the expansion adds to every graph member.
var MemberFields = []string{"entityKey", "ref", "deletedOnRef"}

// Scalars the generated fields use beside the root key's UUID scalar.
const (
	dateTimeScalar = "Temporal.DateTime"
	int64Scalar    = "Generic.Int64"
)

const restrict = "RESTRICT"

// The indexes the expansion adds: a ref's name is unique per root, a
// commit's sequence is unique per root, a patch and a snapshot entry each
// name one entity of their commit and are found by the row version they
// pin, a root has one release pointer, and a member holds one row per
// (entityKey, ref). Each call returns fresh key slices, so no schema shares
// them.
func refIndex() ir.IndexDef {
	return ir.IndexDef{Keys: []string{"root", "name"}, Unique: true, Name: "root_name"}
}

func commitIndex() ir.IndexDef {
	return ir.IndexDef{Keys: []string{"root", "sequence"}, Unique: true, Name: "root_sequence"}
}

func patchIndexes() []ir.IndexDef {
	return []ir.IndexDef{
		{Keys: []string{"commit", "entityKind", "entityKey"}, Unique: true, Name: "entity"},
		{Keys: []string{"entityId", "entityVersion"}, Name: "entity_version"},
	}
}

func releaseIndex() ir.IndexDef {
	return ir.IndexDef{Keys: []string{"root"}, Unique: true, Name: "root"}
}

func memberIndex() ir.IndexDef {
	return ir.IndexDef{Keys: []string{"entityKey", "ref"}, Unique: true, Name: "entity_ref", Origin: ir.OriginVersionGraph}
}

// GeneratedIndex is an index the expansion adds: the type whose table
// holds it, and the index.
type GeneratedIndex struct {
	Type  string
	Index ir.IndexDef
}

// Indexes returns the indexes the expansion of the graph rooted at root
// adds, to its generated types and to each of its members, so
// verification can refuse an authored index of the same name.
func Indexes(schema *ir.Schema, root *ir.TypeDef) []GeneratedIndex {
	g := newGraph(schema, root, ir.TypeRef{})
	out := []GeneratedIndex{{g.refType(), refIndex()}, {g.commitType(), commitIndex()}}
	for _, idx := range patchIndexes() {
		out = append(out, GeneratedIndex{g.patchType(), idx})
	}
	out = append(out, GeneratedIndex{g.releaseType(), releaseIndex()})
	for _, idx := range patchIndexes() {
		out = append(out, GeneratedIndex{g.snapshotType(), idx})
	}
	for _, member := range g.members {
		out = append(out, GeneratedIndex{member.Name, memberIndex()})
	}
	return out
}

// Expand adds the generated definitions of every version graph in schema.
// It assumes the declarations passed verification. It returns the scalar
// names the generated fields use, so the caller can hydrate any the schema
// did not already reference.
func Expand(schema *ir.Schema) []string {
	var roots []*ir.TypeDef
	for _, td := range schema.Types {
		if td.VersionGraph != nil {
			roots = append(roots, td)
		}
	}
	if len(roots) == 0 {
		return nil
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Name < roots[j].Name })

	actor := actorType(schema)
	var scalars []string
	for _, root := range roots {
		g := newGraph(schema, root, actor)
		g.expand()
		scalars = append(scalars, g.uuid, actor.Name)
	}
	scalars = append(scalars, dateTimeScalar, int64Scalar)
	for _, name := range scalars {
		if schema.Scalars[name] == nil {
			schema.Scalars[name] = &ir.ScalarDef{Name: name}
		}
	}
	return scalars
}

// graph holds what one root's expansion reads.
type graph struct {
	schema  *ir.Schema
	root    *ir.TypeDef
	name    string // PascalCase prefix of the generated types
	table   string // snake_case prefix of the generated tables
	uuid    string // the root key's UUID scalar
	actor   ir.TypeRef
	members []*ir.TypeDef
}

func newGraph(schema *ir.Schema, root *ir.TypeDef, actor ir.TypeRef) *graph {
	g := &graph{
		schema: schema,
		root:   root,
		name:   root.VersionGraphName(),
		actor:  actor,
	}
	g.table = codegen.ToSnakeCase(g.name)
	for _, fd := range root.Fields {
		if fd.Key {
			g.uuid = fd.TypeRef.Name
		}
	}
	for _, td := range schema.Types {
		if td.GraphMember != nil && td.GraphMember.Graph == root.Name {
			g.members = append(g.members, td)
		}
	}
	sort.Slice(g.members, func(i, j int) bool { return g.members[i].Name < g.members[j].Name })
	return g
}

func (g *graph) refType() string       { return g.name + "Ref" }
func (g *graph) commitType() string    { return g.name + "Commit" }
func (g *graph) patchType() string     { return g.name + "Patch" }
func (g *graph) releaseType() string   { return g.name + "Release" }
func (g *graph) snapshotType() string  { return g.name + "SnapshotEntry" }
func (g *graph) kindEnum() string      { return g.name + "EntityKind" }
func (g *graph) operationEnum() string { return g.name + "PatchOperation" }

func (g *graph) expand() {
	g.addEnums()
	g.addRef()
	g.addCommit()
	g.addPatch()
	g.addRelease()
	g.addSnapshotEntry()
	for _, member := range g.members {
		g.extendMember(member)
	}
}

// addEnums adds <Name>EntityKind, one value per member type, and
// <Name>PatchOperation.
func (g *graph) addEnums() {
	kinds := &ir.EnumDef{
		Name:    g.kindEnum(),
		Owner:   g.schema.Name,
		Comment: "The entity kinds of the " + g.name + " version graph.",
		Origin:  ir.OriginVersionGraph,
	}
	for _, member := range g.members {
		kinds.Values = append(kinds.Values, ir.EnumValueDef{
			Name:         member.Name,
			SerializedAs: codegen.ToSnakeCase(member.Name),
		})
	}
	g.schema.Enums[kinds.Name] = kinds
	g.schema.Enums[g.operationEnum()] = &ir.EnumDef{
		Name:    g.operationEnum(),
		Owner:   g.schema.Name,
		Comment: "What a patch of the " + g.name + " version graph does to one entity.",
		Values: []ir.EnumValueDef{
			{Name: "Add", SerializedAs: "ADD"},
			{Name: "Update", SerializedAs: "UPDATE"},
			{Name: "Delete", SerializedAs: "DELETE"},
		},
		Origin: ir.OriginVersionGraph,
	}
}

// addRef adds <Name>Ref: a primary line (no parentRef) or a change set.
// Its _version fences every write through it; discarding a draft
// soft-deletes it, which frees its name.
func (g *graph) addRef() {
	g.addType(&ir.TypeDef{
		Name:      g.refType(),
		Comment:   "A line of the " + g.name + " version graph: a primary line when parentRef is null, else a change set.",
		Versioned: true,
		Fields: []*ir.FieldDef{
			g.idField(),
			g.relation("root", g.root.Name, true),
			g.relation("parentRef", g.refType(), false),
			g.relation("baseCommit", g.commitType(), false),
			g.relation("headCommit", g.commitType(), false),
			scalarField("name", "string", true),
			scalarField("sealedAt", dateTimeScalar, false),
			scalarField("createdAt", dateTimeScalar, true),
			g.actorField("createdBy", true),
			scalarField("updatedAt", dateTimeScalar, true),
			g.actorField("updatedBy", true),
			scalarField("deletedAt", dateTimeScalar, false),
			g.actorField("deletedBy", false),
		},
		// A soft-deletable table's unique index covers live rows only.
		Indexes: []ir.IndexDef{refIndex()},
	})
}

// addCommit adds <Name>Commit, written once. A commit with a sequence is a
// published version.
func (g *graph) addCommit() {
	g.addType(&ir.TypeDef{
		Name:    g.commitType(),
		Comment: "A commit of the " + g.name + " version graph: the exact row versions one ref sealed.",
		Fields: []*ir.FieldDef{
			g.idField(),
			g.relation("root", g.root.Name, true),
			g.relation("ref", g.refType(), true),
			g.relation("parentCommit", g.commitType(), false),
			scalarField("message", "string", false),
			scalarField("schemaEpoch", int64Scalar, true),
			scalarField("contentHash", "string", true),
			g.sequenceField(),
			scalarField("createdAt", dateTimeScalar, true),
			g.actorField("createdBy", true),
		},
		Indexes: []ir.IndexDef{commitIndex()},
	})
}

// addPatch adds <Name>Patch, written once: one entity a commit changed and
// the member row version (entityId, entityVersion) it sealed.
func (g *graph) addPatch() {
	g.addType(&ir.TypeDef{
		Name:    g.patchType(),
		Comment: "One entity a commit of the " + g.name + " version graph changed, pinned to the row version it sealed.",
		Fields: []*ir.FieldDef{
			g.idField(),
			g.relation("commit", g.commitType(), true),
			scalarField("entityKind", g.kindEnum(), true),
			scalarField("entityKey", g.uuid, true),
			scalarField("entityId", g.uuid, true),
			scalarField("entityVersion", int64Scalar, true),
			scalarField("operation", g.operationEnum(), true),
		},
		Indexes: patchIndexes(),
	})
}

// addRelease adds <Name>Release: a root's released pointer, which names a
// tagged commit. It is versioned, so its _version fences every move and its
// history is the release log.
func (g *graph) addRelease() {
	g.addType(&ir.TypeDef{
		Name:      g.releaseType(),
		Comment:   "The released commit of one root of the " + g.name + " version graph; its history is the release log.",
		Versioned: true,
		Fields: []*ir.FieldDef{
			g.idField(),
			g.relation("root", g.root.Name, true),
			g.relation("commit", g.commitType(), true),
			scalarField("createdAt", dateTimeScalar, true),
			g.actorField("createdBy", true),
			scalarField("updatedAt", dateTimeScalar, true),
			g.actorField("updatedBy", true),
		},
		Indexes: []ir.IndexDef{releaseIndex()},
	})
}

// addSnapshotEntry adds <Name>SnapshotEntry, written once: one entity of a
// snapshotted commit's tree and the member row version (entityId,
// entityVersion) it pins. A commit's entries are its full pin set.
func (g *graph) addSnapshotEntry() {
	g.addType(&ir.TypeDef{
		Name:    g.snapshotType(),
		Comment: "One entity of a snapshotted commit of the " + g.name + " version graph, pinned to the row version its tree holds.",
		Fields: []*ir.FieldDef{
			g.idField(),
			g.relation("commit", g.commitType(), true),
			scalarField("entityKind", g.kindEnum(), true),
			scalarField("entityKey", g.uuid, true),
			scalarField("entityId", g.uuid, true),
			scalarField("entityVersion", int64Scalar, true),
		},
		Indexes: patchIndexes(),
	})
}

// extendMember adds a member's graph fields, its (entityKey, ref) slot and,
// when it prunes history, the pins that keep every row version a patch or
// a snapshot entry names.
func (g *graph) extendMember(member *ir.TypeDef) {
	entityKey := &ir.FieldDef{
		Name:          "entityKey",
		Comment:       "The entity's logical identity, shared by its rows on every ref.",
		TypeRef:       ir.TypeRef{Name: g.uuid},
		Required:      true,
		AutoGenerated: true,
		Origin:        ir.OriginVersionGraph,
	}
	ref := g.relation("ref", g.refType(), true)
	ref.Comment = "The ref this row overrides the entity on."
	ref.Origin = ir.OriginVersionGraph
	notFalse := "false"
	deleted := &ir.FieldDef{
		Name:     "deletedOnRef",
		Comment:  "True when the row deletes the entity on its ref.",
		TypeRef:  ir.TypeRef{Name: "boolean"},
		Required: true,
		Default:  &notFalse,
		Origin:   ir.OriginVersionGraph,
	}
	member.Fields = append(member.Fields, entityKey, ref, deleted)
	member.Indexes = append(member.Indexes, memberIndex())
	if cfg := member.VersionedConfig; cfg != nil && cfg.RetentionDays != nil {
		for _, table := range []string{g.table + "_patch", g.table + "_snapshot_entry"} {
			cfg.PruneKeepReferencedBy = append(cfg.PruneKeepReferencedBy, &ir.PruneReference{
				Table:         table,
				KeyColumn:     "entity_id",
				VersionColumn: "entity_version",
				Origin:        ir.OriginVersionGraph,
			})
		}
	}
}

func (g *graph) addType(td *ir.TypeDef) {
	td.Owner = g.root.Owner
	td.Role = ir.RoleDBTable
	td.Origin = ir.OriginVersionGraph
	g.schema.Types[td.Name] = td
}

func (g *graph) idField() *ir.FieldDef {
	return &ir.FieldDef{Name: "id", TypeRef: ir.TypeRef{Name: g.uuid}, Required: true, Key: true, AutoGenerated: true}
}

// relation returns a to-one relation field whose foreign key refuses to
// delete a row the graph still references.
func (g *graph) relation(name, target string, required bool) *ir.FieldDef {
	return &ir.FieldDef{
		Name:     name,
		TypeRef:  ir.TypeRef{Name: target},
		Required: required,
		Relation: &ir.RelationDef{Type: target, OnDelete: restrict},
	}
}

// sequenceField is a commit's published version number. Its null (not a
// published version) is distinct from any number, so the Go types give it a
// pointer rather than reading null back as zero.
func (g *graph) sequenceField() *ir.FieldDef {
	fd := scalarField("sequence", int64Scalar, false)
	fd.DistinctNull = true
	return fd
}

func (g *graph) actorField(name string, required bool) *ir.FieldDef {
	return &ir.FieldDef{Name: name, TypeRef: g.actor, Required: required}
}

func scalarField(name, typeName string, required bool) *ir.FieldDef {
	return &ir.FieldDef{Name: name, TypeRef: ir.TypeRef{Name: typeName}, Required: required}
}

// actorType resolves the type of the generated createdBy, updatedBy and
// deletedBy fields as the ORM resolves its user id: the scalar of the first
// such audit field of a table, in type name order, else the UUID key of the
// first graph root.
func actorType(schema *ir.Schema) ir.TypeRef {
	names := make([]string, 0, len(schema.Types))
	for name, td := range schema.Types {
		if td.Role == ir.RoleDBTable && !td.JsonField {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		for _, fd := range schema.Types[name].Fields {
			switch fd.Name {
			case "createdBy", "updatedBy", "deletedBy":
				if schema.Scalars[fd.TypeRef.Name] != nil && !fd.TypeRef.IsArray && !fd.TypeRef.IsMap {
					return ir.TypeRef{Name: fd.TypeRef.Name}
				}
			}
		}
	}
	for _, name := range names {
		td := schema.Types[name]
		if td.VersionGraph == nil {
			continue
		}
		for _, fd := range td.Fields {
			if fd.Key {
				return ir.TypeRef{Name: fd.TypeRef.Name}
			}
		}
	}
	return ir.TypeRef{}
}
