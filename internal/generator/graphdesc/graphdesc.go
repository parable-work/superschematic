// Package graphdesc builds the descriptor of each version graph a schema
// declares (D17, D19): the JSON that tells the version-graph core which
// column of a member kind's rows plays which role, how each column merges,
// and which columns are not content, and tells a storage adapter which
// tables hold the graph and the value class of every column.
// runtime/versiongraph/README.md is its contract.
//
// The ORM generator writes a graph's descriptor as a constant beside its
// generated facade, and the Go types generator writes it as
// versiongraph/<name>.json beside the types, so a browser or another
// runtime drives the core with the same descriptor. Both read it from here.
package graphdesc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The role columns every member kind has once the loader has expanded its
// graph (internal/loader/versiongraph).
const (
	EntityKeyColumn = "entity_key"
	RefColumn       = "ref_id"
	TombstoneColumn = "deleted_on_ref"
	VersionColumn   = "_version"
)

// Version is the descriptor format this package writes and the core reads.
const Version = 2

// Descriptor is a graph descriptor as the core reads it.
type Descriptor struct {
	Version     int    `json:"version"`
	Graph       string `json:"graph"`
	Root        Root   `json:"root"`
	RefTable    string `json:"refTable"`
	CommitTable string `json:"commitTable"`
	PatchTable  string `json:"patchTable"`
	Kinds       []Kind `json:"kinds"`
}

// Root names the graph root's table and its key column.
type Root struct {
	Table string `json:"table"`
	Key   string `json:"key"`
}

// Kind describes one member kind's rows.
type Kind struct {
	Kind         string `json:"kind"`
	Table        string `json:"table"`
	HistoryTable string `json:"historyTable"`
	Key          string `json:"key"`
	ID           string `json:"id"`
	Ref          string `json:"ref"`
	// Root is the column holding the graph root's key, which a storage
	// adapter writes on every row.
	Root      string            `json:"root"`
	Tombstone string            `json:"tombstone"`
	Version   string            `json:"version"`
	Author    string            `json:"author,omitempty"`
	Parent    *Parent           `json:"parent,omitempty"`
	Order     string            `json:"order,omitempty"`
	Singleton bool              `json:"singleton,omitempty"`
	Units     map[string]string `json:"units,omitempty"`
	Excluded  []string          `json:"excluded,omitempty"`
	// Columns gives every column of the kind's table its value class.
	Columns map[string]string `json:"columns"`
}

// Parent is a kind's containment edge: the column holding the parent row's
// entity key, and the parent's kind.
type Parent struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
}

// Graph is one version graph of a schema, as the generators need it.
type Graph struct {
	// Name is the PascalCase name the generated types carry ("Recipe").
	Name string
	// FileName is the snake_case name: the table prefix, the descriptor's
	// graph name and the stem of the files written for it ("recipe").
	FileName string
	// Root is the @versionGraph type.
	Root *ir.TypeDef
	// Members are the @graphMember types, in type name order, which is
	// descriptor order.
	Members []Member
	// Descriptor is the graph's descriptor.
	Descriptor Descriptor
}

// Member is one member type and the columns the generated facade writes
// through besides its own content.
type Member struct {
	Type *ir.TypeDef
	// Kind is the member's kind name in the descriptor and the
	// <Name>EntityKind enum ("step").
	Kind string
	// IDColumn is the column of the member's @key.
	IDColumn string
	// RootColumn is the column of the member's relation to the root.
	RootColumn string
}

// JSON is the descriptor as written to versiongraph/<name>.json and into the
// ORM constant: two-space indented, with a trailing newline.
func (d Descriptor) JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return nil, fmt.Errorf("graphdesc: encode the %s descriptor: %w", d.Graph, err)
	}
	return buf.Bytes(), nil
}

// Graphs returns every version graph schema declares, by root type name. It
// reads the expanded schema, so each member already has its graph fields.
// It fails when a member has a column no value class describes.
func Graphs(schema *ir.Schema) ([]Graph, error) {
	var roots []*ir.TypeDef
	for _, td := range schema.Types {
		if td.VersionGraph != nil {
			roots = append(roots, td)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Name < roots[j].Name })

	sqlTypes := sqlutil.ScalarSQLTypes(schema)
	graphs := make([]Graph, 0, len(roots))
	for _, root := range roots {
		name := root.VersionGraphName()
		g := Graph{Name: name, FileName: codegen.ToSnakeCase(name), Root: root}
		var members []*ir.TypeDef
		for _, td := range schema.Types {
			if td.GraphMember != nil && td.GraphMember.Graph == root.Name {
				members = append(members, td)
			}
		}
		sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
		g.Descriptor = Descriptor{
			Version:     Version,
			Graph:       g.FileName,
			Root:        Root{Table: codegen.ToSnakeCase(root.Name), Key: keyColumn(schema, root)},
			RefTable:    codegen.ToSnakeCase(name + "Ref"),
			CommitTable: codegen.ToSnakeCase(name + "Commit"),
			PatchTable:  codegen.ToSnakeCase(name + "Patch"),
		}
		for _, td := range members {
			member, kind, err := describe(schema, root, td, sqlTypes)
			if err != nil {
				return nil, fmt.Errorf("graphdesc: version graph %s: %w", name, err)
			}
			g.Members = append(g.Members, member)
			g.Descriptor.Kinds = append(g.Descriptor.Kinds, kind)
		}
		graphs = append(graphs, g)
	}
	return graphs, nil
}

// describe builds one member's descriptor entry. sqlTypes is the schema's
// sqlutil.ScalarSQLTypes.
func describe(schema *ir.Schema, root, td *ir.TypeDef, sqlTypes map[string]string) (Member, Kind, error) {
	member := Member{Type: td, Kind: codegen.ToSnakeCase(td.Name)}
	table := codegen.ToSnakeCase(td.Name)
	kind := Kind{
		Kind:         member.Kind,
		Table:        table,
		HistoryTable: table + "_history",
		Key:          EntityKeyColumn,
		Ref:          RefColumn,
		Tombstone:    TombstoneColumn,
		Version:      VersionColumn,
		Singleton:    td.GraphMember.Singleton,
		Columns:      map[string]string{VersionColumn: ClassInteger},
	}
	columns := map[string]string{}
	for _, fd := range td.Fields {
		columns[fd.Name] = Column(schema, fd)
		if !hasColumn(schema, fd) {
			continue
		}
		class, err := valueClass(schema, fd, sqlTypes)
		if err != nil {
			return Member{}, Kind{}, fmt.Errorf("%s.%s: %w", td.Name, fd.Name, err)
		}
		kind.Columns[columns[fd.Name]] = class
	}
	if _, ok := columns["updatedBy"]; ok {
		kind.Author = columns["updatedBy"]
	}
	if p := td.GraphMember.Parent; p != nil {
		kind.Parent = &Parent{Key: columns[p.Key], Kind: codegen.ToSnakeCase(p.Of)}
	}
	if td.GraphMember.Order != "" {
		kind.Order = columns[td.GraphMember.Order]
	}
	for _, fd := range td.Fields {
		column := columns[fd.Name]
		switch {
		case fd.Key:
			member.IDColumn = column
			kind.ID = column
		case fd.Origin == ir.OriginVersionGraph:
			// entityKey, ref and deletedOnRef are role columns.
		case fd.TypeRef.Name == root.Name:
			// The graph's own column: every row of the graph names its root.
			member.RootColumn = column
			kind.Root = column
			kind.Excluded = append(kind.Excluded, column)
		case isAudit(fd.Name):
			if column != kind.Author {
				kind.Excluded = append(kind.Excluded, column)
			}
		case fd.ConflictUnit == ir.ConflictUnitExcluded:
			kind.Excluded = append(kind.Excluded, column)
		case fd.ConflictUnit == ir.ConflictUnitKeyed || fd.ConflictUnit == ir.ConflictUnitJSONSchema:
			if kind.Units == nil {
				kind.Units = map[string]string{}
			}
			kind.Units[column] = fd.ConflictUnit
		}
	}
	return member, kind, nil
}

// The value classes of a descriptor's columns (D19): one normalization rule
// each, which reads what Postgres renders for the SQL type the column is
// stored as and what the schema runtime's JSON for the field's type is. A
// list adds "[]" to its element's class, and a list of lists "[][]".
const (
	ClassString   = "string"
	ClassInteger  = "integer"
	ClassNumber   = "number"
	ClassBoolean  = "boolean"
	ClassUUID     = "uuid"
	ClassDateTime = "dateTime"
	ClassDate     = "date"
	ClassTime     = "time"
	ClassDuration = "duration"
	ClassEnum     = "enum"
	ClassJSON     = "json"
)

// ValueClass is the value class of the column that holds fd, from the SQL
// type the sql generator stores it as (sqlutil.ColumnType) and the JSON the
// schema runtime holds for its type. It fails when no rule reads that pair.
func ValueClass(schema *ir.Schema, fd *ir.FieldDef) (string, error) {
	return valueClass(schema, fd, sqlutil.ScalarSQLTypes(schema))
}

// valueClass is ValueClass with the schema's scalar SQL types. A to-one
// relation holds the target's key and has its class. Any other field's
// column type is sqlutil.ColumnType's. A map's column must be JSONB, and
// the map is "json", whole. A field whose whole column is JSONB (a
// @jsonField value or a list of lists) holds the schema runtime's JSON: an
// object type or JSON value there is "json", and any other element has the
// class it would have in a column of its own. Every other element's class
// comes from its own SQL type.
func valueClass(schema *ir.Schema, fd *ir.FieldDef, sqlTypes map[string]string) (string, error) {
	ref := fd.TypeRef
	if target := schema.Types[ref.Name]; target != nil && isTable(target) && !fd.JsonField && !ref.IsArray && !ref.IsMap {
		key := keyField(target)
		if key == nil {
			return "", fmt.Errorf("the relation to %s needs a key field", target.Name)
		}
		return valueClass(schema, key, sqlTypes)
	}
	column := sqlutil.ColumnType(fd, sqlTypes)
	inJSONB := column == "JSONB"
	if ref.IsMap {
		if !inJSONB {
			return "", fmt.Errorf("a map of %s is stored as %s, which no value class reads", ref.Name, column)
		}
		return ClassJSON, nil
	}
	holds, err := jsonOf(schema, ref.Name)
	if err != nil {
		return "", err
	}
	lists := strings.Repeat("[]", ref.ArrayDepth())
	if inJSONB && holds == ClassJSON {
		return ClassJSON + lists, nil
	}
	class := classOf(holds, sqlutil.ElementType(ref.Name, sqlTypes))
	if class == "" {
		return "", fmt.Errorf("%s holds a JSON %s but is stored as %s, which no value class reads",
			ref.Name, jsonNoun(holds), column)
	}
	return class + lists, nil
}

// jsonOf is what the schema runtime's JSON for one value of the named type
// is: ClassEnum for an enum member's string, ClassJSON for an object or any
// JSON value, ClassInteger, ClassNumber or ClassBoolean for those, and
// ClassString for any other string. A scalar holds what its json_schema
// type mapping names (D14), else what its primitive is.
func jsonOf(schema *ir.Schema, name string) (string, error) {
	if _, ok := schema.Enums[name]; ok {
		return ClassEnum, nil
	}
	if _, ok := schema.Types[name]; ok {
		return ClassJSON, nil
	}
	if _, ok := schema.Inputs[name]; ok {
		return ClassJSON, nil
	}
	if scalar := schema.Scalars[name]; scalar != nil {
		if scalar.IsAnyJSON() || scalar.StructuredJSONType() != "" {
			return ClassJSON, nil
		}
		switch scalar.TypeMappings["json_schema"] {
		case "integer":
			return ClassInteger, nil
		case "number":
			return ClassNumber, nil
		case "boolean":
			return ClassBoolean, nil
		case "":
			switch scalar.LanguagePrimitive {
			case ir.LanguageNumber:
				if strings.EqualFold(scalar.Primitive, "Int") {
					return ClassInteger, nil
				}
				return ClassNumber, nil
			case ir.LanguageBoolean:
				return ClassBoolean, nil
			case ir.LanguageObject:
				return ClassJSON, nil
			}
		}
		return ClassString, nil
	}
	switch name {
	case codegen.PrimitiveString, "String", "ID":
		return ClassString, nil
	case codegen.PrimitiveNumber, "Float":
		return ClassNumber, nil
	case "Int":
		return ClassInteger, nil
	case codegen.PrimitiveBoolean, "Boolean":
		return ClassBoolean, nil
	}
	return "", fmt.Errorf("type %s has no value class", name)
}

// classOf is the class whose rule reads a value the schema runtime holds as
// holds (jsonOf) stored as sqlType, or "" when no rule does. JSONB holds
// any JSON value. A string is a UUID, a date-time, a date, a time of day or
// a duration by the SQL type that stores it, and a plain string as text;
// a number is an integer or not by its SQL type.
func classOf(holds, sqlType string) string {
	sqlType = strings.ToUpper(strings.TrimSpace(sqlType))
	if sqlType == "JSONB" {
		return ClassJSON
	}
	switch holds {
	case ClassString:
		switch {
		case sqlType == "UUID":
			return ClassUUID
		case sqlType == "TIMESTAMPTZ":
			return ClassDateTime
		case sqlType == "DATE":
			return ClassDate
		case sqlType == "TIME":
			return ClassTime
		case sqlType == "INTERVAL":
			return ClassDuration
		case isTextType(sqlType):
			return ClassString
		}
	case ClassEnum:
		if sqlType == "TEXT" {
			return ClassEnum
		}
	case ClassInteger, ClassNumber:
		switch sqlType {
		case "BIGINT", "INTEGER", "INT", "SMALLINT":
			return ClassInteger
		case "DOUBLE PRECISION", "REAL":
			return ClassNumber
		}
		if strings.HasPrefix(sqlType, "NUMERIC") || strings.HasPrefix(sqlType, "DECIMAL") {
			return ClassNumber
		}
	case ClassBoolean:
		if sqlType == "BOOLEAN" {
			return ClassBoolean
		}
	}
	return ""
}

// isTextType reports whether Postgres renders a value of sqlType as the
// string that was stored.
func isTextType(sqlType string) bool {
	switch sqlType {
	case "TEXT", "CITEXT", "INET":
		return true
	}
	return strings.HasPrefix(sqlType, "VARCHAR")
}

// jsonNoun names what jsonOf returned, for an error.
func jsonNoun(holds string) string {
	switch holds {
	case ClassJSON:
		return "value"
	case ClassEnum:
		return "enum member"
	}
	return holds
}

// hasColumn reports whether fd is a column of its table: every field but a
// list of relations, which a join or the other table holds.
func hasColumn(schema *ir.Schema, fd *ir.FieldDef) bool {
	target := schema.Types[fd.TypeRef.Name]
	return target == nil || !isTable(target) || fd.JsonField || fd.TypeRef.IsMap || !fd.TypeRef.IsArray
}

func isTable(td *ir.TypeDef) bool {
	return td.Role == ir.RoleDBTable && !td.JsonField
}

// keyField is a table's @key field, else its field named id.
func keyField(td *ir.TypeDef) *ir.FieldDef {
	for _, fd := range td.Fields {
		if fd.Key {
			return fd
		}
	}
	for _, fd := range td.Fields {
		if fd.Name == "id" {
			return fd
		}
	}
	return nil
}

// keyColumn is the column of a table's key.
func keyColumn(schema *ir.Schema, td *ir.TypeDef) string {
	if key := keyField(td); key != nil {
		return Column(schema, key)
	}
	return "id"
}

// Column is the column that holds fd, as the sql generator names it: a
// to-one relation to a table is <field>_id, every other field its
// snake_case name.
func Column(schema *ir.Schema, fd *ir.FieldDef) string {
	name := codegen.ToSnakeCase(fd.Name)
	target := schema.Types[fd.TypeRef.Name]
	if target != nil && target.Role == ir.RoleDBTable && !target.JsonField && !fd.JsonField && !fd.TypeRef.IsArray && !fd.TypeRef.IsMap {
		return name + "_id"
	}
	return name
}

// isAudit reports whether a field is one of the audit fields D17 leaves out
// of a member's content without a decorator.
func isAudit(name string) bool {
	switch name {
	case "createdAt", "createdBy", "updatedAt", "updatedBy":
		return true
	}
	return false
}
