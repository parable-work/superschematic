package ir

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ReferenceDef is RefersTo<T, Target, { options, value, label }> as written
// (D61): the value of a field or an argument names an item of Target, as
// an operation of the same API lists it. Each member is as the author wrote
// it, and empty when unsaid; [ResolveReference] derives the rest.
type ReferenceDef struct {
	// Type is the target, spelled as a typeRef's name spells it: an object
	// type of the schema or of one of its dependencies.
	Type string `json:"type" yaml:"type" jsonschema:"minLength=1"`

	// Options names the operation that lists the targets: "listProducts",
	// or "ProductCatalog.listProducts" when the name alone is not unique.
	// Empty means the API's one operation that lists the target.
	Options string `json:"options,omitempty" yaml:"options,omitempty" jsonschema:"minLength=1"`

	// Value names the target's field the value holds. Empty means "id".
	Value string `json:"value,omitempty" yaml:"value,omitempty" jsonschema:"minLength=1"`

	// Label names the target's field that names an item. Empty means the
	// target's @display titleField, and none when it has none.
	Label string `json:"label,omitempty" yaml:"label,omitempty" jsonschema:"minLength=1"`
}

// DefaultReferenceValue is the target field a reference holds when it
// names none.
const DefaultReferenceValue = "id"

// ResolvedReference is a reference with every member derived.
type ResolvedReference struct {
	// Target is the type the reference names, from the schema or one of
	// its dependencies.
	Target *TypeDef

	// TargetName is the target's name as the reference spells it.
	TargetName string

	// TargetService is the dependency that declares Target, by its
	// service name; empty when the schema declares it.
	TargetService string

	// OptionsSet is the operation set that holds Options.
	OptionsSet *OperationSet

	// Options is the operation that lists the target: a GET returning
	// Target[] that takes no path argument and no required one.
	Options *FieldDef

	// Value is the target's field the reference's value holds.
	Value *FieldDef

	// Label is the target's field that names an item; nil when it has
	// none, and a UI shows the value.
	Label *FieldDef

	// Lookup is the options operation's argument that narrows its list to
	// the items with the values given: an optional list query argument
	// that refers to the same target by the same value. Nil when the
	// options operation has none.
	Lookup *ArgumentDef
}

// ResolveReference derives what ref leaves unsaid, in the API schema that
// holds it (its types, and its dependencies' by name), and reports the
// first rule it breaks with the loader's wording, without the owner the
// loader puts before it:
//
//   - the target is an object type of the schema or of a dependency: the
//     schema's own first, then the dependency whose package an import of
//     the schema lists the name in, then the dependencies by name;
//   - options names an operation of the schema, by name when the name is
//     unique among its operations, or as Set.operation; unsaid, it is the
//     schema's one operation that lists the target. An operation lists the
//     target when it is a GET returning Target[] (one level, not a map),
//     takes no path argument and no required one, is not
//     @manualRouteRegistration or a @webhook, and admits an end user (its
//     service clause is not @requireService);
//   - value names a field of the target holding one value, "id" unsaid;
//   - label names a field of the target holding one text value, neither
//     secret nor @uiHidden (the rule of @display's titleField, D48);
//     unsaid, it is the target's titleField, or none;
//   - the lookup is the options operation's optional list query argument
//     that refers to the same target by the same value; it has at most one.
//
// That the value's type is the type of the field or argument that holds
// the reference is the loader's check, which knows the holder.
func ResolveReference(schema *Schema, dependencies map[string]*Schema, ref *ReferenceDef) (ResolvedReference, error) {
	var out ResolvedReference
	if schema == nil || ref == nil {
		return out, errors.New("RefersTo: no schema or no reference")
	}
	if schema.Kind != SchemaKindAPI {
		return out, fmt.Errorf("RefersTo is an API's; %s is a %s schema", schema.Name, schema.Kind)
	}
	if strings.TrimSpace(ref.Type) == "" {
		return out, errors.New("RefersTo names no type")
	}
	target, owner, service, err := referenceTarget(schema, dependencies, ref.Type)
	if err != nil {
		return out, err
	}
	out.Target, out.TargetName, out.TargetService = target, ref.Type, service

	valueName := ref.Value
	if valueName == "" {
		valueName = DefaultReferenceValue
	}
	value := typeField(target, valueName)
	if value == nil {
		return out, fmt.Errorf("value %s is not a field of %s%s", valueName, ref.Type, typeFieldList(target))
	}
	if value.TypeRef.IsMap || value.TypeRef.IsArray {
		return out, fmt.Errorf("value %s of %s holds %s; a reference's value is one value, not a list or a map", value.Name, ref.Type, referenceTypeLabel(value.TypeRef))
	}
	out.Value = value

	switch {
	case ref.Label != "":
		label := typeField(target, ref.Label)
		if label == nil {
			return out, fmt.Errorf("label %s is not a field of %s%s", ref.Label, ref.Type, typeFieldList(target))
		}
		if err := checkLabelField(owner, schema, ref.Type, label); err != nil {
			return out, err
		}
		out.Label = label
	case target.Display != nil && target.Display.TitleField != "":
		// The loader holds a titleField to the same rule (checkDisplays).
		out.Label = typeField(target, target.Display.TitleField)
	}

	set, options, err := referenceOptions(schema, ref)
	if err != nil {
		return out, err
	}
	out.OptionsSet, out.Options = set, options

	for _, arg := range options.Arguments {
		if arg == nil || !arg.IsQuery || arg.Required || !arg.TypeRef.IsArray || arg.TypeRef.IsArrayOfArrays || arg.TypeRef.IsMap ||
			arg.References == nil || arg.References.Type != ref.Type {
			continue
		}
		argValue := arg.References.Value
		if argValue == "" {
			argValue = DefaultReferenceValue
		}
		if typeField(target, argValue) != value {
			continue
		}
		if out.Lookup != nil {
			return out, fmt.Errorf("options %s has two lookups, %s and %s", operationName(set, options), out.Lookup.Name, arg.Name)
		}
		out.Lookup = arg
	}
	return out, nil
}

// referenceTarget finds the type a reference names: the schema's own; else
// the dependency's whose package an import of the schema lists the name
// in, so the loader, which reads only that dependency, and a generator,
// which has every one, find the same type (D61, amended); else a
// dependency's, the dependencies in name order. It returns the schema that
// declares it, whose scalars type its fields, and that schema's service
// name among the dependencies, empty for the schema's own.
func referenceTarget(schema *Schema, dependencies map[string]*Schema, name string) (*TypeDef, *Schema, string, error) {
	type candidate struct {
		service string
		schema  *Schema
	}
	candidates := []candidate{{"", schema}}
	if service := importedService(schema, name); service != "" && service != schema.Name && dependencies[service] != nil {
		candidates = append(candidates, candidate{service, dependencies[service]})
	}
	for _, dep := range sortedStringMapKeys(dependencies) {
		if dependencies[dep] != nil {
			candidates = append(candidates, candidate{dep, dependencies[dep]})
		}
	}
	for _, c := range candidates {
		s := c.schema
		if td := s.Types[name]; td != nil {
			if td.IsTrait || td.Role == RoleTrait {
				return nil, nil, "", fmt.Errorf("RefersTo names %s, a trait; a reference names an object type", name)
			}
			return td, s, c.service, nil
		}
		switch {
		case s.Enums[name] != nil:
			return nil, nil, "", fmt.Errorf("RefersTo names %s, an enum; a reference names an object type", name)
		case s.Unions[name] != nil:
			return nil, nil, "", fmt.Errorf("RefersTo names %s, a union; a reference names an object type", name)
		case s.Scalars[name] != nil:
			return nil, nil, "", fmt.Errorf("RefersTo names %s, a scalar; a reference names an object type", name)
		}
	}
	return nil, nil, "", fmt.Errorf("RefersTo names %s, which is not a type of %s or its dependencies", name, schema.Name)
}

// importedService is the service of the first import of schema that lists
// name, or "" when none does.
func importedService(schema *Schema, name string) string {
	for _, imp := range schema.Imports {
		for _, t := range imp.Types {
			if t == name {
				return ServiceNameForPackage(imp.Package)
			}
		}
	}
	return ""
}

// ServiceNameForPackage is the service a schema package names: its last
// "/" segment, without the scope ("@schemas/web-db" is web-db).
func ServiceNameForPackage(pkg string) string {
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		return pkg[i+1:]
	}
	return pkg
}

// checkLabelField holds a named label to @display titleField's rule: one
// text value, neither secret nor @uiHidden. A field of a dependency's type
// reads its scalar from the dependency, then from the schema.
func checkLabelField(owner, schema *Schema, target string, label *FieldDef) error {
	if !TextValue(owner, label.TypeRef) && !TextValue(schema, label.TypeRef) {
		return fmt.Errorf("label %s of %s holds %s; a label is one text value: a string, or a scalar whose values are strings", label.Name, target, referenceTypeLabel(label.TypeRef))
	}
	switch {
	case label.Secret:
		return fmt.Errorf("label %s of %s is secret; a label is shown to everyone who lists %s", label.Name, target, target)
	case label.UIHidden:
		return fmt.Errorf("label %s of %s is @uiHidden; a label is shown wherever the reference is", label.Name, target)
	}
	return nil
}

// referenceOptions finds the operation a reference's options names, or,
// unsaid, the schema's one operation that lists the target.
func referenceOptions(schema *Schema, ref *ReferenceDef) (*OperationSet, *FieldDef, error) {
	if ref.Options == "" {
		var eligible []setOperation
		for _, set := range schema.OperationSets {
			if set == nil {
				continue
			}
			for _, op := range set.Operations {
				if op != nil && listsTarget(op, ref.Type) && optionsRefusal(set, op) == "" {
					eligible = append(eligible, setOperation{set, op})
				}
			}
		}
		switch len(eligible) {
		case 0:
			return nil, nil, fmt.Errorf("no operation of %s lists %s (a GET returning %s[] with no path argument and no required one, that an end user may call); declare one or name it with options", schema.Name, ref.Type, ref.Type)
		case 1:
			return eligible[0].set, eligible[0].op, nil
		}
		return nil, nil, fmt.Errorf("%s is listed by %s and %s; name one with options", ref.Type,
			operationName(eligible[0].set, eligible[0].op), operationName(eligible[1].set, eligible[1].op))
	}

	var found []setOperation
	setName, opName, qualified := strings.Cut(ref.Options, ".")
	if !qualified {
		setName, opName = "", ref.Options
	}
	for _, set := range schema.OperationSets {
		if set == nil || qualified && set.Name != setName {
			continue
		}
		for _, op := range set.Operations {
			if op != nil && op.Name == opName {
				found = append(found, setOperation{set, op})
			}
		}
	}
	switch {
	case len(found) == 0:
		return nil, nil, fmt.Errorf("options %s is not an operation of %s", ref.Options, schema.Name)
	case len(found) > 1:
		return nil, nil, fmt.Errorf("options %s names an operation of %s and of %s; name one as %s", ref.Options,
			found[0].set.Name, found[1].set.Name, found[0].set.Name+"."+opName)
	}
	set, op := found[0].set, found[0].op
	if !listsTarget(op, ref.Type) {
		method, path := op.HTTPMethod, op.RestPath
		if method == "" {
			method = "an operation with no method"
		}
		if path == "" {
			path = "(no path)"
		}
		return nil, nil, fmt.Errorf("options %s does not list %s: it is %s %s returning %s", ref.Options, ref.Type, method, path, referenceTypeLabel(op.TypeRef))
	}
	if refusal := optionsRefusal(set, op); refusal != "" {
		return nil, nil, fmt.Errorf("options %s %s", ref.Options, refusal)
	}
	return set, op, nil
}

// setOperation is one operation and the set that holds it.
type setOperation struct {
	set *OperationSet
	op  *FieldDef
}

// listsTarget reports whether op is a GET returning a list of target, one
// level, not a map.
func listsTarget(op *FieldDef, target string) bool {
	return strings.EqualFold(op.HTTPMethod, "GET") && op.TypeRef.Name == target &&
		op.TypeRef.IsArray && !op.TypeRef.IsArrayOfArrays && !op.TypeRef.IsMap
}

// optionsRefusal says why an operation that lists a target cannot be its
// options, or "" when it can: a UI calls it with no argument, as an end
// user.
func optionsRefusal(set *OperationSet, op *FieldDef) string {
	for _, arg := range op.Arguments {
		if arg != nil && !arg.IsQuery {
			return "has the path argument " + arg.Name
		}
	}
	for _, arg := range op.Arguments {
		if arg != nil && arg.Required {
			return "has the required argument " + arg.Name
		}
	}
	if strings.Contains(op.RestPath, "{") {
		return "has a path argument in " + op.RestPath
	}
	switch {
	case op.ManualRouteRegistration:
		return "is @manualRouteRegistration"
	case op.Webhook:
		return "is a @webhook"
	}
	if callers := EffectiveServiceCallers(set, op); callers != nil && callers.Mode == ServiceCallersRequire {
		return "admits only service callers (@requireService)"
	}
	return ""
}

// operationName writes an operation as Set.operation.
func operationName(set *OperationSet, op *FieldDef) string {
	return set.Name + "." + op.Name
}

// typeField finds a type's own field by its name or its JSON key.
func typeField(td *TypeDef, name string) *FieldDef {
	for _, f := range td.Fields {
		if f != nil && (f.Name == name || f.JSONTag != "" && f.JSONTag == name) {
			return f
		}
	}
	return nil
}

// typeFieldList names a type's fields for a message, or nothing for a type
// without any.
func typeFieldList(td *TypeDef) string {
	names := make([]string, 0, len(td.Fields))
	for _, f := range td.Fields {
		if f != nil {
			names = append(names, f.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return " (fields: " + strings.Join(names, ", ") + ")"
}

// referenceTypeLabel writes a type as a message names it: Name, Name[] or
// a map of Name.
func referenceTypeLabel(ref TypeRef) string {
	label := ref.Name + strings.Repeat("[]", ref.ArrayDepth())
	if ref.IsMap {
		return "a map of " + label
	}
	return label
}

// TextValue reports whether ref, read in schema, is a single text value:
// the string primitive, or a scalar of the schema whose values are JSON
// strings, not a list or a map. The catalog gives a scalar that holds any
// JSON value, a JSON object or a JSON array the string primitive too, so
// its json_schema mapping rules it out. It is the rule of @display's
// titleField (D48), a reference's label (D61) and a search argument (D62).
func TextValue(schema *Schema, ref TypeRef) bool {
	if ref.IsArray || ref.IsMap {
		return false
	}
	if ref.Name == "string" {
		return true
	}
	if schema == nil {
		return false
	}
	scalar := schema.Scalars[ref.Name]
	return scalar != nil && scalar.LanguagePrimitive == LanguageString && !scalar.IsAnyJSON() && scalar.StructuredJSONType() == ""
}

// ReferencesOf lists every reference the schema's fields and operation
// arguments hold, each with the name of its holder (Type.field or
// Set.operation(argument)), types by name and then operations in
// declaration order. An operation's own reference, which the loader
// refuses, is listed as Set.operation.
func ReferencesOf(schema *Schema) []HeldReference {
	var out []HeldReference
	if schema == nil {
		return out
	}
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		td := schema.Types[name]
		if td == nil {
			continue
		}
		for _, f := range td.Fields {
			if f != nil && f.References != nil {
				out = append(out, HeldReference{Holder: td.Name + "." + f.Name, Type: td, Field: f, Ref: f.References, TypeRef: f.TypeRef})
			}
		}
	}
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil {
				continue
			}
			if op.References != nil {
				out = append(out, HeldReference{Holder: operationName(set, op), Set: set, Field: op, Ref: op.References, TypeRef: op.TypeRef})
			}
			for _, arg := range op.Arguments {
				if arg != nil && arg.References != nil {
					out = append(out, HeldReference{
						Holder: fmt.Sprintf("%s(%s)", operationName(set, op), arg.Name),
						Set:    set, Field: op, Argument: arg, Ref: arg.References, TypeRef: arg.TypeRef,
					})
				}
			}
		}
	}
	return out
}

// HeldReference is one reference and what holds it: a type's field
// (Type and Field), an operation's argument (Set, Field the operation and
// Argument), or an operation itself (Set and Field, no Argument).
type HeldReference struct {
	// Holder names it: Type.field, Set.operation(argument) or
	// Set.operation.
	Holder string

	Type     *TypeDef
	Set      *OperationSet
	Field    *FieldDef
	Argument *ArgumentDef

	// Ref is the reference as written, or as derived when Derived is set.
	Ref *ReferenceDef

	// TypeRef is the holder's type.
	TypeRef TypeRef

	// Derived reports a reference the field does not write: a relation's
	// key on a type with @source, which refers to the API's one type with
	// the same @source as the relation (D61, amended). DerivedReferences
	// lists these; ReferencesOf lists only the written ones.
	Derived bool
}

// FieldReference is the reference that field of holder holds: its
// RefersTo, as written; or, when it writes none, the one derived from
// holder's @source when the field is a relation's key (D61, amended) and
// the derived reference resolves. derived reports the second. It is nil
// when the field refers to nothing.
//
// The derived reference names the one object type W of the schema whose
// @source is the relation's type U, by its id, with every other member
// unsaid: { type: W }. With no such type, or two or more, or when
// [ResolveReference] refuses it (no operation lists W, or two do), or when
// W's value is not of the key's type, the key refers to nothing. A key
// that is a list or a map derives nothing; the loader refuses it.
//
// holder's @source target and the relation's type are read from the schema
// or from dependencies, by the target's service name.
func FieldReference(schema *Schema, dependencies map[string]*Schema, holder *TypeDef, field *FieldDef) (ref *ReferenceDef, derived bool) {
	if field == nil {
		return nil, false
	}
	if field.References != nil {
		return field.References, false
	}
	if schema == nil || schema.Kind != SchemaKindAPI || holder == nil || holder.Source == nil ||
		field.TypeRef.IsArray || field.TypeRef.IsMap {
		return nil, false
	}
	service, _ := splitSourceTarget(holder.Source.Target)
	relation, ok := RelationKey(sourceType(schema, dependencies, holder.Source.Target), field)
	if !ok {
		return nil, false
	}
	related := relation.TypeRef.Name
	if relation.Relation.Type != "" {
		related = relation.Relation.Type
	}
	_, related = splitSourceTarget(related)
	target := related
	if service != "" {
		target = service + "." + related
	}
	candidates := SourcedBy(schema, target)
	if len(candidates) != 1 {
		return nil, false
	}
	ref = &ReferenceDef{Type: candidates[0].Name}
	resolved, err := ResolveReference(schema, dependencies, ref)
	if err != nil || resolved.Value.TypeRef.Name != field.TypeRef.Name {
		return nil, false
	}
	return ref, true
}

// RelationKey reports the relation whose key field is, on a type sourced
// from source (D61, amended): source's field r, one Relation (not HasMany,
// not many-to-many, not a list or a map), when field is named r+"Id" and
// source declares no field of that name. The key reads r's foreign key,
// the column sqlgen names r_id. It does not look at field's type, which
// the loader holds to the type of the related type's id.
func RelationKey(source *TypeDef, field *FieldDef) (relation *FieldDef, ok bool) {
	if source == nil || field == nil {
		return nil, false
	}
	name, isKey := strings.CutSuffix(field.Name, "Id")
	if !isKey || name == "" {
		return nil, false
	}
	for _, f := range source.Fields {
		if f == nil {
			continue
		}
		switch f.Name {
		case field.Name:
			return nil, false
		case name:
			relation = f
		}
	}
	if relation == nil || relation.Relation == nil || relation.HasMany || relation.ManyToMany ||
		relation.TypeRef.IsArray || relation.TypeRef.IsMap {
		return nil, false
	}
	return relation, true
}

// SourcedBy lists schema's own object types whose @source is target, a
// service-qualified type ("shop-db.Product"), sorted by name. An
// unqualified @source, or one qualified with the schema's own name, is the
// schema's own type, and matches either spelling.
func SourcedBy(schema *Schema, target string) []*TypeDef {
	if schema == nil {
		return nil
	}
	want := qualifiedSourceTarget(schema, target)
	var out []*TypeDef
	for _, name := range sortedStringMapKeys(schema.Types) {
		td := schema.Types[name]
		if td == nil || td.Source == nil || td.IsTrait || td.Role == RoleTrait {
			continue
		}
		if qualifiedSourceTarget(schema, td.Source.Target) == want {
			out = append(out, td)
		}
	}
	return out
}

// DerivedReferences lists every reference [FieldReference] derives on the
// schema's types, as [ReferencesOf] lists the written ones: types by name,
// fields in declaration order, each with Derived set.
func DerivedReferences(schema *Schema, dependencies map[string]*Schema) []HeldReference {
	var out []HeldReference
	if schema == nil || schema.Kind != SchemaKindAPI {
		return out
	}
	for _, name := range sortedStringMapKeys(schema.Types) {
		td := schema.Types[name]
		if td == nil || td.Source == nil {
			continue
		}
		for _, f := range td.Fields {
			if ref, derived := FieldReference(schema, dependencies, td, f); derived {
				out = append(out, HeldReference{Holder: td.Name + "." + f.Name, Type: td, Field: f, Ref: ref, TypeRef: f.TypeRef, Derived: true})
			}
		}
	}
	return out
}

// sourceType finds a @source target ("shop-db.OrderLine"): the schema's own
// type when the target is unqualified or names the schema, else the type
// of the dependency the target names. Nil when neither declares it.
func sourceType(schema *Schema, dependencies map[string]*Schema, target string) *TypeDef {
	service, name := splitSourceTarget(target)
	if service == "" || service == schema.Name {
		return schema.Types[name]
	}
	if dep := dependencies[service]; dep != nil {
		return dep.Types[name]
	}
	return nil
}

// splitSourceTarget splits "service.Type" at its last dot; an unqualified
// target has no service.
func splitSourceTarget(target string) (service, name string) {
	if i := strings.LastIndex(target, "."); i >= 0 {
		return target[:i], target[i+1:]
	}
	return "", target
}

// qualifiedSourceTarget writes a @source target with its service, the
// schema's own name for an unqualified one.
func qualifiedSourceTarget(schema *Schema, target string) string {
	if service, _ := splitSourceTarget(target); service == "" {
		return schema.Name + "." + target
	}
	return target
}
