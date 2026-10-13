package behaviortypes

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Type is one type of a schema that composes behaviors, as the types
// generators write its behaviors' fields: the <Name>Behaviors type, which
// holds what a read returns under behaviors, one member per behavior that
// declares a field.
type Type struct {
	// Name is the type's name.
	Name string
	// BehaviorsName is <Name>Behaviors.
	BehaviorsName string
	// Behaviors are the type's behaviors in list order, each narrowed by
	// its config.
	Behaviors []Behavior
	// Aliases are the types the behaviors' fields refer to ({"$ts": name}
	// nodes), in the order they are first referred to: a Workflow's
	// <Name>State. TypeScript writes them as they are.
	Aliases []Alias
	// Structs are <Name>Behaviors, then each behavior's fields as
	// <Name><Member>, then every object the fields hold, each after the
	// struct that holds it: what a language that names its object types
	// (Go, Rust, Python) writes.
	Structs []Struct
	// Enums are the Aliases as enums of their string values, in order.
	Enums []Enum
}

// Struct is a named object type: <T>Behaviors, a behavior's fields, or an
// object one of them holds, named by the path to it.
type Struct struct {
	Name string `json:"name"`
	// Doc is a sentence that starts with the name.
	Doc     string   `json:"doc"`
	Members []Member `json:"members"`
}

// Member is one member of a Struct.
type Member struct {
	// JSON is the member's key in the JSON, exact: a behavior's name
	// (acme.Rating), a field's, or a property's, which may be a key of
	// the config (a deadline's or a link's name).
	JSON string `json:"json"`
	// Name is JSON as a PascalCase identifier (MemberName): a Go field's
	// name, and the source of a Rust or Python field's snake_case one.
	Name string `json:"name"`
	// Doc is the member's description, or "".
	Doc   string `json:"doc,omitempty"`
	Shape Shape  `json:"shape"`
	// Required is whether every read holds the member; one that is not
	// is absent when it has no value.
	Required bool `json:"required,omitempty"`
}

// Kind is what a Shape holds.
type Kind int

// The kinds of a Shape.
const (
	// KindJSON is any JSON value: a field a behavior's declaration names
	// without a narrowing (an extension's), or a schema a language's
	// types cannot say, such as a union (anyOf).
	KindJSON Kind = iota
	KindString
	KindInteger
	KindNumber
	KindBoolean
	// KindArray holds Elem's values.
	KindArray
	// KindMap holds Elem's values by string key.
	KindMap
	// KindStruct is the Struct Ref names.
	KindStruct
	// KindEnum is the Enum Ref names.
	KindEnum
)

// kindNames are the kinds' names, as String writes them.
var kindNames = [...]string{"json", "string", "integer", "number", "boolean", "array", "map", "struct", "enum"}

// String names the kind.
func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// MarshalText writes the kind's name, as a model's JSON shows it.
func (k Kind) MarshalText() ([]byte, error) {
	return []byte(k.String()), nil
}

// Shape is a member's type.
type Shape struct {
	Kind Kind `json:"kind"`
	// Nullable is whether null is a value beside the kind's.
	Nullable bool `json:"nullable,omitempty"`
	// Values are the values a KindString takes, when the schema lists
	// them (an enum the narrowing writes in place, which no alias names).
	Values []string `json:"values,omitempty"`
	// Elem is a KindArray's element or a KindMap's value.
	Elem *Shape `json:"elem,omitempty"`
	// Ref names a KindStruct's Struct or a KindEnum's Enum.
	Ref string `json:"ref,omitempty"`
}

// Enum is a named type of string values: an alias a narrowing names.
type Enum struct {
	Name string `json:"name"`
	// Doc is a sentence that starts with the name.
	Doc     string       `json:"doc"`
	Members []EnumMember `json:"members"`
}

// EnumMember is one value of an Enum.
type EnumMember struct {
	// Name is Value as a PascalCase identifier (MemberName).
	Name  string `json:"name"`
	Value string `json:"value"`
}

// BehaviorsName names the type that holds a type's behaviors' fields.
func BehaviorsName(typeName string) string {
	return typeName + "Behaviors"
}

// MemberName writes a name as a PascalCase identifier, as the types
// generators name a behavior's member of <T>Behaviors, a field, a key of a
// config and an enum's value in a language whose names cannot hold what
// the JSON key holds: each run of letters and digits between other
// characters (a dot, a dash, an underscore) starts with a capital, the
// rest of its letters as they are, and the runs are joined. A core
// behavior's name is already one (Workflow); an extension's, qualified by
// its extension's name, loses its dot (acme.Rating is AcmeRating), a dashed
// state its dashes (in-progress is InProgress), and a camelCase key only
// changes its first letter (invalidOutput is InvalidOutput). A name that
// would start with a digit starts with N; one with no letter or digit is
// Value. The JSON keeps the exact name: Go writes it in the field's tag,
// Rust in serde's rename and Python in the field's alias.
func MemberName(name string) string {
	var b strings.Builder
	upper := true
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) || r > unicode.MaxASCII {
			upper = true
			continue
		}
		if upper {
			b.WriteRune(unicode.ToUpper(r))
			upper = false
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	switch {
	case out == "":
		return "Value"
	case out[0] >= '0' && out[0] <= '9':
		return "N" + out
	}
	return out
}

// Build models the behaviors' fields of each type of the schema that
// composes behaviors, in the order of the types' names; it is nil when
// none does. reg declares the behaviors: each must be registered and its
// config one its declaration takes, as the loader checked. Build refuses a
// name it would write that the schema already declares or that two of its
// own would share, and two members of one struct or enum one name.
func Build(schema *ir.Schema, reg *registry.Registry) ([]Type, error) {
	var tds []*ir.TypeDef
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, td := range types {
			if td != nil && len(td.Behaviors) > 0 {
				tds = append(tds, td)
			}
		}
	}
	if len(tds) == 0 {
		return nil, nil
	}
	slices.SortFunc(tds, func(a, b *ir.TypeDef) int { return strings.Compare(a.Name, b.Name) })
	if reg == nil {
		return nil, fmt.Errorf("type %s composes behavior %s, and no registry declares the behaviors", tds[0].Name, tds[0].Behaviors[0].Name)
	}
	names := declaredNames(schema)
	out := make([]Type, 0, len(tds))
	for _, td := range tds {
		t, err := buildType(schema, td, reg)
		if err != nil {
			return nil, err
		}
		for _, name := range t.names() {
			if owner, taken := names[name]; taken {
				return nil, fmt.Errorf("the behaviors of type %s would name a type %s, which %s already names", td.Name, name, owner)
			}
			names[name] = "the behaviors of type " + td.Name
		}
		out = append(out, t)
	}
	return out, nil
}

// declaredNames are the names the schema gives its own definitions and
// imports, by what declares them: a scalar by its name without dots, as a
// language's alias of it is.
func declaredNames(schema *ir.Schema) map[string]string {
	names := map[string]string{}
	for _, defs := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for name := range defs {
			names[name] = "the schema's type " + name
		}
	}
	for name := range schema.Enums {
		names[name] = "the schema's enum " + name
	}
	for name := range schema.Unions {
		names[name] = "the schema's union " + name
	}
	for name := range schema.Scalars {
		names[strings.ReplaceAll(name, ".", "")] = "the schema's scalar " + name
	}
	for _, imp := range schema.Imports {
		for _, name := range imp.Types {
			names[name] = "the import " + name + " from " + imp.Package
		}
	}
	return names
}

// names are the type names t writes: its structs' and its enums'.
func (t Type) names() []string {
	out := make([]string, 0, len(t.Structs)+len(t.Enums))
	for _, s := range t.Structs {
		out = append(out, s.Name)
	}
	for _, e := range t.Enums {
		out = append(out, e.Name)
	}
	return out
}

// typeBuilder builds one Type.
type typeBuilder struct {
	t       *Type
	aliases map[string]Alias
}

func buildType(schema *ir.Schema, td *ir.TypeDef, reg *registry.Registry) (Type, error) {
	t := Type{Name: td.Name, BehaviorsName: BehaviorsName(td.Name)}
	target := Target{Schema: schema.Name, Base: td.Name, Document: schema, Instance: td}
	b := typeBuilder{t: &t, aliases: map[string]Alias{}}
	for _, ref := range td.Behaviors {
		declaration, ok := reg.Behavior(ref.Name)
		if !ok {
			return t, fmt.Errorf("type %s composes behavior %s, which the registry does not declare", td.Name, ref.Name)
		}
		if err := declaration.ValidateConfig(ref.Config); err != nil {
			return t, fmt.Errorf("type %s: %w", td.Name, err)
		}
		narrowed, err := Narrow(declaration, ref.Config, target)
		if err != nil {
			return t, fmt.Errorf("type %s: behavior %s: %w", td.Name, ref.Name, err)
		}
		for _, alias := range narrowed.Aliases {
			b.aliases[alias.Name] = alias
		}
		t.Behaviors = append(t.Behaviors, Behavior{Declaration: declaration, Narrowed: narrowed})
	}

	top := b.reserve(t.BehaviorsName, fmt.Sprintf("%s holds the fields of %s's behaviors as a read returns them, under behaviors: one member per behavior that declares a field, under the behavior's name.", t.BehaviorsName, An(td.Name)))
	var members []Member
	for _, behavior := range t.Behaviors {
		declaration := behavior.Declaration
		if len(declaration.Fields) == 0 {
			continue
		}
		member := MemberName(declaration.Name)
		name := td.Name + member
		index := b.reserve(name, fmt.Sprintf("%s holds the fields %s's %s adds, as a read returns them: a field with no value is absent.", name, An(td.Name), declaration.Name))
		var fields []Member
		for _, field := range declaration.Fields {
			shape := behavior.Narrowed.Fields[field.Name]
			fieldName := MemberName(field.Name)
			typed, err := b.shape(WithoutDescription(shape.Schema), name+fieldName, name+"."+field.Name)
			if err != nil {
				return t, fmt.Errorf("type %s: behavior %s: field %s: %w", td.Name, declaration.Name, field.Name, err)
			}
			fields = append(fields, Member{JSON: field.Name, Name: fieldName, Doc: DescriptionOf(shape.Schema), Shape: typed, Required: shape.Present})
		}
		if err := b.fill(index, fields); err != nil {
			return t, fmt.Errorf("type %s: behavior %s: %w", td.Name, declaration.Name, err)
		}
		members = append(members, Member{
			JSON:     declaration.Name,
			Name:     member,
			Doc:      fmt.Sprintf("The fields of %s.", declaration.Name),
			Shape:    Shape{Kind: KindStruct, Ref: name},
			Required: true,
		})
	}
	if err := b.fill(top, members); err != nil {
		return t, fmt.Errorf("type %s: %w", td.Name, err)
	}
	return t, nil
}

// reserve adds a struct with no members yet, so it comes before the
// structs its members hold, and returns its index.
func (b *typeBuilder) reserve(name, doc string) int {
	b.t.Structs = append(b.t.Structs, Struct{Name: name, Doc: doc})
	return len(b.t.Structs) - 1
}

// fill sets a reserved struct's members, refusing two of one name.
func (b *typeBuilder) fill(index int, members []Member) error {
	s := &b.t.Structs[index]
	seen := map[string]string{}
	for _, member := range members {
		if prev, taken := seen[member.Name]; taken {
			return fmt.Errorf("%s and %s would both be the member %s of %s", prev, member.JSON, member.Name, s.Name)
		}
		seen[member.Name] = member.JSON
	}
	s.Members = append([]Member{}, members...)
	return nil
}

// shape types one narrowed schema: name is the struct an object there
// becomes, and where says what the schema types, for the struct's doc
// (TicketLinks.targets, an item of TicketLinksTargets.reviewers).
func (b *typeBuilder) shape(schema any, name, where string) (Shape, error) {
	s, ok := schema.(map[string]any)
	if !ok {
		return Shape{Kind: KindJSON}, nil
	}
	if ref, ok := s[RefKey].(string); ok {
		nullable, _ := s["nullable"].(bool)
		if err := b.enum(ref); err != nil {
			return Shape{}, err
		}
		return Shape{Kind: KindEnum, Ref: ref, Nullable: nullable}, nil
	}
	if _, ok := s["const"]; ok {
		return Shape{Kind: KindJSON}, nil
	}
	for _, union := range []string{"anyOf", "oneOf", "allOf", "not"} {
		if _, ok := s[union]; ok {
			return Shape{Kind: KindJSON}, nil
		}
	}
	nullable, kinds := typesOf(s)
	if values, ok := s["enum"].([]any); ok {
		strs, ok := stringValues(values)
		if !ok || len(kinds) > 1 || len(kinds) == 1 && kinds[0] != "string" {
			return Shape{Kind: KindJSON}, nil
		}
		return Shape{Kind: KindString, Values: strs, Nullable: nullable}, nil
	}
	if len(kinds) == 0 {
		if _, ok := s["properties"]; !ok {
			return Shape{Kind: KindJSON}, nil
		}
		kinds = []string{"object"}
	}
	if len(kinds) > 1 {
		return Shape{Kind: KindJSON}, nil
	}
	var out Shape
	switch kinds[0] {
	case "string":
		out = Shape{Kind: KindString}
	case "integer":
		out = Shape{Kind: KindInteger}
	case "number":
		out = Shape{Kind: KindNumber}
	case "boolean":
		out = Shape{Kind: KindBoolean}
	case "array":
		var items any = true
		if declared, ok := s["items"]; ok {
			items = declared
		}
		elem, err := b.shape(items, name+"Item", "an item of "+where)
		if err != nil {
			return Shape{}, err
		}
		out = Shape{Kind: KindArray, Elem: &elem}
	case "object":
		properties, _ := s["properties"].(map[string]any)
		additional := s["additionalProperties"]
		switch {
		case len(properties) > 0 || additional == false:
			if err := b.object(s, properties, name, where); err != nil {
				return Shape{}, err
			}
			out = Shape{Kind: KindStruct, Ref: name}
		case isSchema(additional):
			elem, err := b.shape(additional, name+"Value", "a value of "+where)
			if err != nil {
				return Shape{}, err
			}
			out = Shape{Kind: KindMap, Elem: &elem}
		default:
			out = Shape{Kind: KindJSON}
		}
	default:
		return Shape{Kind: KindJSON}, nil
	}
	out.Nullable = nullable
	return out, nil
}

// object adds the struct an object schema is, its members in the order of
// their keys, as TypeScript writes them.
func (b *typeBuilder) object(s map[string]any, properties map[string]any, name, where string) error {
	index := b.reserve(name, fmt.Sprintf("%s types %s.", name, where))
	required := map[string]bool{}
	if list, ok := s["required"].([]any); ok {
		for _, each := range list {
			if text, ok := each.(string); ok {
				required[text] = true
			}
		}
	}
	members := make([]Member, 0, len(properties))
	for _, property := range SortedKeys(properties) {
		memberName := MemberName(property)
		typed, err := b.shape(WithoutDescription(properties[property]), name+memberName, name+"."+property)
		if err != nil {
			return err
		}
		members = append(members, Member{
			JSON:     property,
			Name:     memberName,
			Doc:      DescriptionOf(properties[property]),
			Shape:    typed,
			Required: required[property],
		})
	}
	return b.fill(index, members)
}

// enum adds the enum an alias names, once, refusing a reference to a name
// no behavior of the type names, or to one that is not of strings.
func (b *typeBuilder) enum(name string) error {
	if slices.ContainsFunc(b.t.Enums, func(e Enum) bool { return e.Name == name }) {
		return nil
	}
	alias, ok := b.aliases[name]
	if !ok {
		return fmt.Errorf("its schema refers to %s, which no behavior of the type names", name)
	}
	node, _ := alias.Schema.(map[string]any)
	values, _ := node["enum"].([]any)
	strs, ok := stringValues(values)
	if !ok {
		return fmt.Errorf("its schema refers to %s, which is not an enum of strings", name)
	}
	enum := Enum{Name: name, Doc: name + " is " + lowerFirst(alias.Doc)}
	seen := map[string]string{}
	for _, value := range strs {
		member := MemberName(value)
		if prev, taken := seen[member]; taken {
			return fmt.Errorf("the values %s and %s of %s would both be named %s", prev, value, name, member)
		}
		seen[member] = value
		enum.Members = append(enum.Members, EnumMember{Name: member, Value: value})
	}
	b.t.Aliases = append(b.t.Aliases, alias)
	b.t.Enums = append(b.t.Enums, enum)
	return nil
}

// typesOf reads a schema's type: whether it takes null, and its other
// types.
func typesOf(s map[string]any) (nullable bool, kinds []string) {
	switch t := s["type"].(type) {
	case string:
		kinds = []string{t}
	case []any:
		for _, each := range t {
			if name, ok := each.(string); ok {
				kinds = append(kinds, name)
			}
		}
	}
	if i := slices.Index(kinds, "null"); i >= 0 {
		return true, slices.Delete(kinds, i, i+1)
	}
	return false, kinds
}

// stringValues reads a list of JSON values that are all strings.
func stringValues(values []any) ([]string, bool) {
	if len(values) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out = append(out, text)
	}
	return out, true
}

// isSchema reports whether additionalProperties holds a schema: an object,
// or true, which allows any value.
func isSchema(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		return true
	case bool:
		return v
	}
	return false
}

// lowerFirst lowers a sentence's first letter.
func lowerFirst(text string) string {
	if text == "" {
		return text
	}
	return strings.ToLower(text[:1]) + text[1:]
}

// UsesKind reports whether any struct of the types holds a value of the
// kind, at any depth.
func UsesKind(types []Type, kind Kind) bool {
	for _, t := range types {
		for _, s := range t.Structs {
			for _, member := range s.Members {
				for shape := &member.Shape; shape != nil; shape = shape.Elem {
					if shape.Kind == kind {
						return true
					}
				}
			}
		}
	}
	return false
}

// Comment is a member's doc comment on one line: its description, and the
// values a string takes when the schema lists them in place, for a
// language whose type says only that it is a string.
func (m Member) Comment() string {
	doc := strings.Join(strings.Fields(m.Doc), " ")
	shape := m.Shape
	for shape.Kind == KindArray && shape.Elem != nil {
		shape = *shape.Elem
	}
	if shape.Kind != KindString || len(shape.Values) == 0 {
		return doc
	}
	quoted := make([]string, len(shape.Values))
	for i, value := range shape.Values {
		quoted[i] = fmt.Sprintf("%q", value)
	}
	values := "One of " + strings.Join(quoted, ", ") + "."
	if len(quoted) == 1 {
		values = "Always " + quoted[0] + "."
	}
	if doc == "" {
		return values
	}
	return doc + " " + values
}
