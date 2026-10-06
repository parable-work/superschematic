package topcoat

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// record is a Topcoat record mirroring an object type an operation
// returns, as the API sends it: each field of the type's JSON that a page
// may show, in a type a record can hold.
type record struct {
	Name     string // TenantViewRecord
	TypeName string // TenantView, the types crate's
	Doc      string
	Fields   []recordField
}

// recordField is one field of a record: its Rust name and type, and the
// expression that reads it from the type's JSON (`json`, a serde_json
// Value).
type recordField struct {
	Name     string
	JSONName string
	Type     string
	Read     string
}

// reservedFields are names a Topcoat record cannot give a field: the
// browser runtime's own members. A field so named gets a trailing
// underscore.
var reservedFields = map[string]bool{
	"clone": true, "constructor": true, "dehydrate": true, "deref": true,
	"deref_mut": true, "then": true, "to_json": true, "to_string": true, "value_of": true,
}

// leaf is how a record holds one JSON value that is not a list, a map or
// an object: its Rust type and the wire helper that reads it.
type leaf struct {
	rustType string
	read     string
}

var (
	stringLeaf  = leaf{"String", "wire::string"}
	integerLeaf = leaf{"i64", "wire::integer"}
	numberLeaf  = leaf{"f64", "wire::number"}
	booleanLeaf = leaf{"bool", "wire::boolean"}
	// jsonLeaf holds a value a record has no type for (a union, any JSON
	// value, a JSON object or array scalar) as its JSON text.
	jsonLeaf = leaf{"String", "wire::json_text"}
)

// schemaSet is the service's schema and its dependencies', where a type
// an operation names is declared.
type schemaSet []*ir.Schema

// recordBuilder walks the result types of the service's operations and
// the object types they nest.
type recordBuilder struct {
	schemaSet
	records map[string]*record
}

// recordsOf is a record per object type an operation of schemas[0]
// returns, and per object type such a type nests, sorted by name.
func recordsOf(schemas schemaSet) ([]record, error) {
	b := &recordBuilder{schemaSet: schemas, records: map[string]*record{}}
	for _, set := range schemas[0].OperationSets {
		for _, op := range set.Operations {
			if b.objectType(op.TypeRef.Name) != nil {
				if err := b.add(op.TypeRef.Name); err != nil {
					return nil, err
				}
			}
		}
	}
	out := make([]record, 0, len(b.records))
	for _, name := range sortedKeys(b.records) {
		out = append(out, *b.records[name])
	}
	return out, nil
}

func (b *recordBuilder) add(name string) error {
	if _, done := b.records[name]; done {
		return nil
	}
	typeDef := b.objectType(name)
	rec := &record{Name: name + "Record", TypeName: name, Doc: firstLine(typeDef.Description)}
	b.records[name] = rec
	seen := map[string]string{}
	for _, field := range typeDef.Fields {
		if field.UIHidden {
			continue
		}
		rustName := registry.RustIdentifier(field.Name, "value")
		if reservedFields[rustName] {
			rustName += "_"
		}
		if other, taken := seen[rustName]; taken {
			return fmt.Errorf("type %s: fields %s and %s are both the record field %s", name, other, field.Name, rustName)
		}
		seen[rustName] = field.Name
		rustType, read, err := b.fieldShape(field.TypeRef, !field.Required, fmt.Sprintf("&json[%s]", rustString(field.Name)))
		if err != nil {
			return fmt.Errorf("type %s field %s: %w", name, field.Name, err)
		}
		rec.Fields = append(rec.Fields, recordField{Name: rustName, JSONName: field.Name, Type: rustType, Read: read})
	}
	return nil
}

// fieldShape is the record type of a field of type ref, in Option when
// optional, and the expression that reads it from arg.
func (b *recordBuilder) fieldShape(ref ir.TypeRef, optional bool, arg string) (string, string, error) {
	elemType, elemRead, err := b.element(ref.Name)
	if err != nil {
		return "", "", err
	}
	// Each level wraps the one inside it: a list, a map's entries, then
	// Option.
	rustType, read := elemType, elemRead
	for range ref.ArrayDepth() {
		rustType, read = "Vec<"+rustType+">", closure("wire::list", read)
	}
	if ref.IsMap {
		rustType, read = "Vec<(String, "+rustType+")>", closure("wire::entries", read)
	}
	if optional {
		rustType, read = "Option<"+rustType+">", closure("wire::optional", read)
	}
	return rustType, apply(read, arg), nil
}

// element is the record type of a single value of the named type and the
// function (a path, or a closure over `v`) that reads one.
func (b *recordBuilder) element(name string) (string, string, error) {
	if typeDef := b.objectType(name); typeDef != nil {
		if err := b.add(name); err != nil {
			return "", "", err
		}
		return name + "Record", name + "Record::from_wire", nil
	}
	l := b.leafOf(name)
	return l.rustType, l.read, nil
}

// leafOf is how a record holds a value of a primitive, a scalar, an enum
// or a union, by the JSON the API sends for it.
func (s schemaSet) leafOf(name string) leaf {
	switch name {
	case "string":
		return stringLeaf
	case "number":
		return numberLeaf
	case "boolean":
		return booleanLeaf
	}
	for _, schema := range s {
		if _, ok := schema.Enums[name]; ok {
			return stringLeaf
		}
		if scalar, ok := schema.Scalars[name]; ok {
			switch scalar.TypeMappings["json_schema"] {
			case "string":
				return stringLeaf
			case "integer":
				return integerLeaf
			case "number":
				return numberLeaf
			case "boolean":
				return booleanLeaf
			case "":
				switch scalar.LanguagePrimitive {
				case ir.LanguageString:
					return stringLeaf
				case ir.LanguageNumber:
					return numberLeaf
				case ir.LanguageBoolean:
					return booleanLeaf
				}
			}
			return jsonLeaf
		}
	}
	return jsonLeaf
}

// objectType is the object type the schemas declare as name, or nil.
func (s schemaSet) objectType(name string) *ir.TypeDef {
	for _, schema := range s {
		if typeDef := schema.Types[name]; typeDef != nil && !typeDef.IsTrait {
			return typeDef
		}
	}
	return nil
}

// closure is the function that reads a value with wrap (a wire helper
// taking a value and an element reader) around read.
func closure(wrap, read string) string {
	return "|v| " + wrap + "(v, " + read + ")"
}

// apply is the expression that calls read, a path or a closure over `v`,
// on arg.
func apply(read, arg string) string {
	if body, ok := strings.CutPrefix(read, "|v| "); ok {
		return strings.Replace(body, "(v, ", "("+arg+", ", 1)
	}
	return read + "(" + arg + ")"
}

// enum is the enum the schemas declare as name, or nil.
func (s schemaSet) enum(name string) *ir.EnumDef {
	for _, schema := range s {
		if enum := schema.Enums[name]; enum != nil {
			return enum
		}
	}
	return nil
}

// scalar is the scalar the schemas declare as name, or nil.
func (s schemaSet) scalar(name string) *ir.ScalarDef {
	for _, schema := range s {
		if scalar := schema.Scalars[name]; scalar != nil {
			return scalar
		}
	}
	return nil
}
