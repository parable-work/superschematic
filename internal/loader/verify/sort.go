package verify

import (
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// checkSorts holds each SortBy argument (D60, amended) to its operation,
// in every form:
//
//   - an operation takes one, on a @paginated operation, as an optional
//     query parameter of text without a default, which is no search, refers
//     to nothing and has no Validate rules: its values are its fields;
//   - it names at least one field, each once, of the operation's item, the
//     element of its result, found as ir.LookupScopes finds a type so that
//     a dependency's item works (D66);
//   - each field holds one text, number or time (ir.SortValue), and is
//     neither Nullable nor Secret;
//   - the item declares an id of one required value, which breaks ties;
//   - @paginated's defaultSort is said exactly when a sort argument is,
//     and is one of its values (ir.SortValues).
//
// A field that is @virtual on an item sourced from a table is a warning:
// no column holds it, so the table cannot order by it, though an
// implementation that sorts in memory can. A field of text without a
// maxLength is a warning too: the ORM's page key is the row's id, but an
// implementation's own key may hold the value, and a cursor holds at most
// 1024 characters. A dependency's item reads from
// Input.DependencySchemas; one the loader did not read is not checked
// field by field.
func checkSorts(schema *ir.Schema, in Input, r *Result) {
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op != nil {
				checkSort(schema, in, set.Name+"."+op.Name, op, r)
			}
		}
	}
}

// checkSort is checkSorts for one operation; owner is Set.operation.
func checkSort(schema *ir.Schema, in Input, owner string, op *ir.FieldDef, r *Result) {
	var sort *ir.ArgumentDef
	for _, arg := range op.Arguments {
		if arg == nil || arg.Sort == nil {
			continue
		}
		if sort != nil {
			r.errorf("", "%s: has two sort arguments, %s and %s", owner, sort.Name, arg.Name)
			continue
		}
		sort = arg
	}
	if sort == nil {
		if op.Page != nil && op.Page.DefaultSort != "" {
			r.errorf("", "%s: @paginated defaultSort %s orders a sort argument, and the operation declares none (SortBy)", owner, op.Page.DefaultSort)
		}
		return
	}

	if op.Page == nil {
		r.errorf("", "%s: sort argument %s orders a page, and the operation has no @paginated", owner, sort.Name)
	}
	if !sort.IsQuery || sort.Required {
		r.errorf("", "%s: sort argument %s must be an optional query parameter", owner, sort.Name)
	}
	if sort.Default != nil {
		r.errorf("", "%s: sort argument %s has a default; @paginated's defaultSort is the order of a request that sends none", owner, sort.Name)
	}
	if sort.TypeRef != (ir.TypeRef{Name: "string"}) {
		r.errorf("", "%s: sort argument %s holds %s; a sort's value is text", owner, sort.Name, typeLabel(sort.TypeRef))
	}
	if len(sort.Sort) == 0 {
		r.errorf("", "%s: sort argument %s names no field", owner, sort.Name)
	}
	if sort.Search {
		r.errorf("", "%s: sort argument %s is also a search (SearchQuery); a page's order and its search are two arguments", owner, sort.Name)
	}
	if sort.References != nil {
		r.errorf("", "%s: sort argument %s refers to %s (RefersTo); a sort's value names a field of its item", owner, sort.Name, sort.References.Type)
	}
	if rules := validateRules(sort); len(rules) > 0 {
		r.errorf("", "%s: sort argument %s has Validate's %s; a sort's values are its fields, which the sort checks itself", owner, sort.Name, strings.Join(rules, ", "))
	}

	itemName := op.TypeRef.Name
	item, itemSchema, found := sortItem(schema, in, itemName)
	switch {
	case found:
		checkSortFields(schema, owner, sort, item, itemSchema, r)
	case !importedType(schema, itemName):
		// A primitive, an enum or a union declares no field. A type an
		// import names that the loader did not read is not checked here.
		seen := map[string]bool{}
		for _, field := range sort.Sort {
			if !seen[field] {
				seen[field] = true
				r.errorf("", "%s: sort argument %s names %s, which %s does not declare", owner, sort.Name, field, itemName)
			}
		}
	}

	if op.Page == nil {
		return
	}
	values := ir.SortValues(sort.Sort)
	switch {
	case op.Page.DefaultSort == "":
		r.errorf("", "%s: sort argument %s needs @paginated's defaultSort, the order of a request that sends none", owner, sort.Name)
	case len(values) > 0 && !slices.Contains(values, op.Page.DefaultSort):
		r.errorf("", "%s: @paginated defaultSort %s is not one of %s's values: %s", owner, op.Page.DefaultSort, sort.Name, strings.Join(values, ", "))
	}
}

// checkSortFields holds a sort's fields, and the id that breaks its ties,
// to the item that declares them. itemSchema declares the item, whose
// scalars type its fields; a field's scalar is read from it, then from
// schema.
func checkSortFields(schema *ir.Schema, owner string, sort *ir.ArgumentDef, item *ir.TypeDef, itemSchema *ir.Schema, r *Result) {
	seen := map[string]bool{}
	for _, name := range sort.Sort {
		if seen[name] {
			r.errorf("", "%s: sort argument %s names %s twice", owner, sort.Name, name)
			continue
		}
		seen[name] = true
		field := ownField(item, name)
		switch {
		case field == nil:
			r.errorf("", "%s: sort argument %s names %s, which %s does not declare", owner, sort.Name, name, item.Name)
			continue
		case !ir.SortValue(itemSchema, field.TypeRef) && !ir.SortValue(schema, field.TypeRef):
			r.errorf("", "%s: sort argument %s names %s.%s, which holds %s; a page is sorted by one text, number or time", owner, sort.Name, item.Name, field.Name, typeLabel(field.TypeRef))
		case !field.Required:
			r.errorf("", "%s: sort argument %s names %s.%s, which is Nullable; a page's order has no place for a null", owner, sort.Name, item.Name, field.Name)
		case field.Secret:
			r.errorf("", "%s: sort argument %s names %s.%s, which is Secret; a page's order would show it", owner, sort.Name, item.Name, field.Name)
		case unboundedText(schema, itemSchema, field):
			r.warnf("", "%s: sort argument %s names %s.%s, text without a maxLength: a page key that holds its value, as an implementation's own may, can pass the cursor's limit of 1024 characters with a long one", owner, sort.Name, item.Name, field.Name)
		}
		if field.Virtual && item.Source != nil {
			r.warnf("", "%s: sort argument %s names %s.%s, which is @virtual: no column of %s holds it, so the table cannot order by it", owner, sort.Name, item.Name, field.Name, item.Source.Target)
		}
	}
	switch id := ownField(item, "id"); {
	case id == nil:
		r.errorf("", "%s: a sort breaks ties by id, and %s declares no id", owner, item.Name)
	case id.TypeRef.IsArray || id.TypeRef.IsMap || !id.Required:
		label := typeLabel(id.TypeRef)
		if !id.Required {
			label = "Nullable<" + label + ">"
		}
		r.errorf("", "%s: a sort breaks ties by id, and %s.id holds %s; it must be one required value", owner, item.Name, label)
	}
}

// validateRules names the Validate rules an argument declares, in the
// order Validate's config lists them.
func validateRules(arg *ir.ArgumentDef) []string {
	var rules []string
	for _, rule := range []struct {
		name string
		set  bool
	}{
		{"min", arg.ValidateMin != nil},
		{"max", arg.ValidateMax != nil},
		{"minLength", arg.ValidateMinLength != nil},
		{"maxLength", arg.ValidateMaxLength != nil},
		{"listMin", arg.ValidateListMin != nil},
		{"listMax", arg.ValidateListMax != nil},
		{"pattern", arg.ValidatePattern != ""},
	} {
		if rule.set {
			rules = append(rules, rule.name)
		}
	}
	return rules
}

// unboundedText reports whether a sort field holds text whose length
// nothing bounds: the string primitive, or a text scalar (ir.TextValue)
// the catalog stores as TEXT or CITEXT, without a maxLength of the field's
// (Validate) or of its scalar's. A time, a date and a UUID are text of a
// bounded length, as their SQL types say. The ORM's page key is the row's id
// alone, but an implementation's own key may hold the sort field's value
// (D60, amended). The field's scalar is read from itemSchema, then from
// schema.
func unboundedText(schema, itemSchema *ir.Schema, field *ir.FieldDef) bool {
	if field.ValidateMaxLength != nil || field.TypeRef.IsArray || field.TypeRef.IsMap {
		return false
	}
	if field.TypeRef.Name == "string" {
		return true
	}
	for _, in := range []*ir.Schema{itemSchema, schema} {
		if in == nil || in.Scalars[field.TypeRef.Name] == nil {
			continue
		}
		scalar := in.Scalars[field.TypeRef.Name]
		if !ir.TextValue(in, field.TypeRef) || scalar.MaxLength > 0 {
			return false
		}
		switch strings.ToUpper(strings.TrimSpace(scalar.TypeMappings["sql"])) {
		case "TEXT", "CITEXT":
			return true
		}
		return false
	}
	return false
}

// sortItem finds the object type an operation lists, by its name, as
// ir.LookupScopes finds a type: the schema's own, then its dependencies'.
// It returns the schema that declares it.
func sortItem(schema *ir.Schema, in Input, name string) (*ir.TypeDef, *ir.Schema, bool) {
	for _, scope := range ir.LookupScopes(schema, in.DependencySchemas, name) {
		if td := scope.Schema.Types[name]; td != nil && !td.IsTrait && td.Role != ir.RoleTrait {
			return td, scope.Schema, true
		}
	}
	return nil, nil, false
}

// importedType reports whether one of the schema's imports lists name.
func importedType(schema *ir.Schema, name string) bool {
	for _, imp := range schema.Imports {
		if slices.Contains(imp.Types, name) {
			return true
		}
	}
	return false
}

// ownField finds a type's field by its name, which is its JSON name.
func ownField(td *ir.TypeDef, name string) *ir.FieldDef {
	for _, f := range td.Fields {
		if f != nil && f.Name == name {
			return f
		}
	}
	return nil
}
