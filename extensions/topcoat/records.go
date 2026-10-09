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

// recordField is one field of a record: its Rust name and type, the
// expression that reads it from the type's JSON (`json`, a serde_json
// Value), and the one that writes it back (Put applied to the field, or to
// `value`, its value when Optional).
type recordField struct {
	Name     string
	JSONName string
	Type     string
	Read     string
	Put      string
	Optional bool
}

// PutExpr is the expression that writes arg, the field's value (inside
// its Option when Optional), as JSON.
func (f recordField) PutExpr(arg string) string { return apply(f.Put, arg) }

// reservedFields are names a Topcoat record cannot give a field: the
// browser runtime's own members. A field so named gets a trailing
// underscore.
var reservedFields = map[string]bool{
	"clone": true, "constructor": true, "dehydrate": true, "deref": true,
	"deref_mut": true, "then": true, "to_json": true, "to_string": true, "value_of": true,
}

// leaf is how a record holds one JSON value that is not a list, a map or
// an object: its Rust type and the wire helpers that read and write it.
type leaf struct {
	rustType string
	read     string
	put      string
}

var (
	stringLeaf  = leaf{"String", "wire::string", "wire::put_string"}
	integerLeaf = leaf{"i64", "wire::integer", "wire::put_integer"}
	numberLeaf  = leaf{"f64", "wire::number", "wire::put_number"}
	booleanLeaf = leaf{"bool", "wire::boolean", "wire::put_boolean"}
	// jsonLeaf holds a value a record has no type for (a union, any JSON
	// value, a JSON object or array scalar) as its JSON text.
	jsonLeaf = leaf{"String", "wire::json_text", "wire::put_json_text"}
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

func newRecordBuilder(schemas schemaSet) *recordBuilder {
	return &recordBuilder{schemaSet: schemas, records: map[string]*record{}}
}

// addResults adds a record per object type an operation of the service
// returns, and per object type such a type nests. The user model's
// operations (D50) add none: the identity runtime serves them, so they have
// no in-process call or procedure that answers a record, and their results
// carry what a page has no use for, such as login's session token.
func (b *recordBuilder) addResults() error {
	for _, set := range b.schemaSet[0].OperationSets {
		for _, op := range set.Operations {
			if op.IdentityOperation != "" {
				continue
			}
			if b.objectType(op.TypeRef.Name) != nil {
				if err := b.add(op.TypeRef.Name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// sorted is every record added, by name.
func (b *recordBuilder) sorted() []record {
	out := make([]record, 0, len(b.records))
	for _, name := range sortedKeys(b.records) {
		out = append(out, *b.records[name])
	}
	return out
}

func (b *recordBuilder) add(name string) error {
	if _, done := b.records[name]; done {
		return nil
	}
	typeDef := b.objectType(name)
	rec := &record{Name: name + "Record", TypeName: name, Doc: firstLine(typeDef.Description)}
	b.records[name] = rec
	seen := map[string]string{}
	// A @uiHidden or secret field is left out, since everything in a record
	// reaches the browser.
	for _, field := range typeDef.Fields {
		if field.UIHidden || field.Secret {
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
		shape, err := b.fieldShape(field.TypeRef, !field.Required)
		if err != nil {
			return fmt.Errorf("type %s field %s: %w", name, field.Name, err)
		}
		rec.Fields = append(rec.Fields, recordField{
			Name:     rustName,
			JSONName: field.Name,
			Type:     shape.rustType,
			Read:     apply(shape.read, fmt.Sprintf("&json[%s]", rustString(field.Name))),
			Put:      shape.put,
			Optional: !field.Required,
		})
	}
	return nil
}

// shape is how a record holds a value of a field's type: its Rust type,
// the function that reads it from JSON (Option included) and the one that
// writes its value (Option excluded) as JSON. A function is a path, or a
// closure over `v`.
type shape struct {
	rustType string
	read     string
	put      string
}

// fieldShape is the shape of a field of type ref, in Option when optional.
func (b *recordBuilder) fieldShape(ref ir.TypeRef, optional bool) (shape, error) {
	s, err := b.element(ref.Name)
	if err != nil {
		return shape{}, err
	}
	// Each level wraps the one inside it: a list, a map's entries, then
	// Option.
	for range ref.ArrayDepth() {
		s = shape{"Vec<" + s.rustType + ">", closure("wire::list", s.read), closure("wire::put_list", s.put)}
	}
	if ref.IsMap {
		s = shape{"Vec<(String, " + s.rustType + ")>", closure("wire::entries", s.read), closure("wire::put_entries", s.put)}
	}
	if optional {
		s.rustType, s.read = "Option<"+s.rustType+">", closure("wire::optional", s.read)
	}
	return s, nil
}

// element is the shape of a single value of the named type.
func (b *recordBuilder) element(name string) (shape, error) {
	if typeDef := b.objectType(name); typeDef != nil {
		if err := b.add(name); err != nil {
			return shape{}, err
		}
		return shape{name + "Record", name + "Record::from_wire", name + "Record::to_wire"}, nil
	}
	l := b.leafOf(name)
	return shape(l), nil
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
