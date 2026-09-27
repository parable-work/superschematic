package verify

import (
	"regexp"
	"strings"

	"github.com/parable-work/superschematic/internal/loader/versiongraph"
	ir "github.com/parable-work/superschematic/ir"
)

// graphNamePattern matches a version graph name: it prefixes generated
// types (PascalCase) and tables (snake_case).
var graphNamePattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)

// checkVersionGraphs validates the @versionGraph, @graphMember and
// @conflictUnit declarations before the loader expands them
// (versiongraph.Expand). Everything the expansion and the generated tables
// rely on is checked here, so the expansion itself cannot fail.
func checkVersionGraphs(schema *ir.Schema, r *Result) {
	members := make(map[string][]string)
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td.GraphMember != nil {
			members[td.GraphMember.Graph] = append(members[td.GraphMember.Graph], td.Name)
		}
	}

	generated := make(map[string]string)
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td.VersionGraph != nil {
			checkGraphRoot(schema, td, len(members[td.Name]), generated, r)
		}
	}
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td.GraphMember != nil {
			checkGraphMember(schema, td, r)
		}
		checkConflictUnits(td, r)
	}
}

// checkGraphRoot validates one @versionGraph root. generated collects the
// type names every graph so far generates, so two graphs cannot generate
// the same one.
func checkGraphRoot(schema *ir.Schema, td *ir.TypeDef, memberCount int, generated map[string]string, r *Result) {
	if td.Role != ir.RoleDBTable {
		r.errorf(td.Owner, "%s: @versionGraph is only allowed on DB table types (this type has role %s)", td.Name, td.Role)
		return
	}
	if td.GraphMember != nil {
		r.errorf(td.Owner, "%s: a type belongs to at most one version graph; a @versionGraph root cannot also be a @graphMember", td.Name)
	}
	checkGraphKey(schema, td, "@versionGraph", r)
	if name := td.VersionGraph.Name; name != "" && !graphNamePattern.MatchString(name) {
		r.errorf(td.Owner, "%s: @versionGraph name %q must be a PascalCase identifier", td.Name, name)
		return
	}
	if td.VersionGraph.SchemaEpoch < 0 {
		r.errorf(td.Owner, "%s: @versionGraph schemaEpoch must not be negative", td.Name)
	}
	if memberCount == 0 {
		r.errorf(td.Owner, "%s: @versionGraph needs at least one @graphMember type", td.Name)
	}
	graphName := td.VersionGraphName()
	for _, suffix := range versiongraph.TypeSuffixes {
		generatedName := graphName + suffix
		if other, ok := generated[generatedName]; ok {
			r.errorf(td.Owner, "%s: version graph %q generates %s, which version graph %q also generates; give one a different @versionGraph name", td.Name, graphName, generatedName, other)
			continue
		}
		generated[generatedName] = graphName
		if definedName(schema, generatedName) {
			r.errorf(td.Owner, "%s: version graph %q generates %s, which the schema already defines; rename it or set @versionGraph({ name })", td.Name, graphName, generatedName)
		}
	}
}

// definedName reports whether the schema defines a type, enum, union or
// scalar of that name.
func definedName(schema *ir.Schema, name string) bool {
	return schema.Types[name] != nil || schema.Enums[name] != nil ||
		schema.Unions[name] != nil || schema.Scalars[name] != nil
}

// checkGraphMember validates one @graphMember entity kind.
func checkGraphMember(schema *ir.Schema, td *ir.TypeDef, r *Result) {
	cfg := td.GraphMember
	if td.Role != ir.RoleDBTable {
		r.errorf(td.Owner, "%s: @graphMember is only allowed on DB table types (this type has role %s)", td.Name, td.Role)
		return
	}
	if !td.Versioned {
		r.errorf(td.Owner, "%s: @graphMember requires @versioned: a commit's rows live in the member's history", td.Name)
	}
	checkGraphKey(schema, td, "@graphMember", r)

	root := schema.Types[cfg.Graph]
	if root == nil || root.VersionGraph == nil {
		r.errorf(td.Owner, "%s: @graphMember graph %q is not a @versionGraph type of this schema", td.Name, cfg.Graph)
		return
	}

	rootRelations := 0
	for _, fd := range td.Fields {
		if relatesTo(fd, root.Name) {
			rootRelations++
		}
	}
	if rootRelations != 1 {
		r.errorf(td.Owner, "%s: @graphMember requires exactly one relation to the graph root %s, found %d", td.Name, root.Name, rootRelations)
	}
	if fieldNamed(td, "deletedAt") != nil {
		r.errorf(td.Owner, "%s: a @graphMember cannot have deletedAt: a delete on a ref is a row that holds its (entityKey, ref) slot", td.Name)
	}
	for _, name := range versiongraph.MemberFields {
		if fieldNamed(td, name) != nil {
			r.errorf(td.Owner, "%s: field %s collides with the field @graphMember adds", td.Name, name)
		}
	}

	if parent := cfg.Parent; parent != nil {
		of := schema.Types[parent.Of]
		if of == nil || of.GraphMember == nil || of.GraphMember.Graph != cfg.Graph {
			r.errorf(td.Owner, "%s: @graphMember parent.of %q is not a @graphMember type of graph %s", td.Name, parent.Of, cfg.Graph)
		}
		key := fieldNamed(td, parent.Key)
		switch {
		case key == nil:
			r.errorf(td.Owner, "%s: @graphMember parent.key %q is not a field of %s", td.Name, parent.Key, td.Name)
		case key.Key || !isUUIDField(schema, key):
			r.errorf(td.Owner, "%s: @graphMember parent.key %q must be a UUID field that holds the parent's entityKey", td.Name, parent.Key)
		}
	}
	if cfg.Order != "" {
		order := fieldNamed(td, cfg.Order)
		if order == nil || order.TypeRef.Name != "Generic.Int64" || order.TypeRef.IsArray || order.TypeRef.IsMap {
			r.errorf(td.Owner, "%s: @graphMember order %q must name a Generic.Int64 field", td.Name, cfg.Order)
		}
	}
}

// checkGraphKey checks the one UUID @key a graph root and a graph member
// need: the generated tables reference it and pin its versions.
func checkGraphKey(schema *ir.Schema, td *ir.TypeDef, decorator string, r *Result) {
	var keys []*ir.FieldDef
	for _, fd := range td.Fields {
		if fd.Key {
			keys = append(keys, fd)
		}
	}
	if len(keys) != 1 {
		r.errorf(td.Owner, "%s: %s requires exactly one @key field, found %d", td.Name, decorator, len(keys))
		return
	}
	if !isUUIDField(schema, keys[0]) {
		r.errorf(td.Owner, "%s: %s requires a UUID @key, and %s is %s", td.Name, decorator, keys[0].Name, keys[0].TypeRef.Name)
	}
}

// checkConflictUnits validates each @conflictUnit on a type's fields.
func checkConflictUnits(td *ir.TypeDef, r *Result) {
	for _, fd := range td.Fields {
		if fd.ConflictUnit == "" {
			continue
		}
		switch fd.ConflictUnit {
		case ir.ConflictUnitAtomic, ir.ConflictUnitExcluded:
		case ir.ConflictUnitKeyed, ir.ConflictUnitJSONSchema:
			if !isJSONObjectField(fd) {
				r.errorf(td.Owner, "%s.%s: @conflictUnit(%q) needs a JSON object field (Generic.JSON or @jsonField)", td.Name, fd.Name, fd.ConflictUnit)
			}
		default:
			r.errorf(td.Owner, "%s.%s: @conflictUnit %q is not a strategy (atomic, keyed, jsonSchema, excluded)", td.Name, fd.Name, fd.ConflictUnit)
		}
		if td.GraphMember == nil {
			r.errorf(td.Owner, "%s.%s: @conflictUnit is only allowed on the fields of a @graphMember type", td.Name, fd.Name)
		}
	}
}

// relatesTo reports whether fd is a to-one relation to the named table.
func relatesTo(fd *ir.FieldDef, table string) bool {
	if fd.TypeRef.IsArray || fd.TypeRef.IsMap || fd.JsonField {
		return false
	}
	if fd.Relation != nil && fd.Relation.Type != "" {
		return fd.Relation.Type == table
	}
	return fd.TypeRef.Name == table
}

func fieldNamed(td *ir.TypeDef, name string) *ir.FieldDef {
	for _, fd := range td.Fields {
		if fd.Name == name {
			return fd
		}
	}
	return nil
}

// isUUIDField reports whether fd is a single value of a scalar stored as a
// Postgres UUID.
func isUUIDField(schema *ir.Schema, fd *ir.FieldDef) bool {
	if fd.TypeRef.IsArray || fd.TypeRef.IsMap || fd.JsonField || fd.Relation != nil {
		return false
	}
	scalar := schema.Scalars[fd.TypeRef.Name]
	return scalar != nil && strings.EqualFold(scalar.TypeMappings["sql"], "UUID")
}

// isJSONObjectField reports whether fd holds one JSON object: a
// Generic.JSON value or a @jsonField payload, not a list of them.
func isJSONObjectField(fd *ir.FieldDef) bool {
	if fd.TypeRef.IsArray || fd.TypeRef.IsMap {
		return false
	}
	return fd.JsonField || fd.TypeRef.Name == "Generic.JSON"
}
