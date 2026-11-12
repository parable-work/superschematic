package verify

import (
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// checkFilters holds each FilterBy (D106) to its operation, in every
// form, and fills what the argument leaves unsaid:
//
//   - a filter is the argument of a GET whose result is a list, under a
//     name no other argument has; no other argument is named filter, the
//     query parameter every filter travels under;
//   - it names at least one field, each once, of the operation's item, the
//     element of its result, found as ir.LookupScopes finds a type so that
//     a dependency's item works (D66);
//   - each field holds one value a filter can compare (FilterFieldKind):
//     not a list or a map, not Secret, not @decrypted, and on an item
//     sourced from a table not @virtual and neither deletedAt nor
//     deletedBy, which no row a list holds has a value in, so a list
//     filters by nothing its view does not show. createdBy and updatedBy
//     filter as any id does, and createdAt and updatedAt as any time
//     (D110).
//
// Each field's Kind, Operators and TypeRef are then the item's field's
// (ir.FilterOperators); a data form that writes another kind, other
// operators or another type is refused. An item the loader did not read cannot be held to,
// and is an error: a filter's kinds are what every generator reads.
func checkFilters(schema *ir.Schema, in Input, r *Result) {
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op != nil && op.Filter != nil {
				checkFilter(schema, in, set.Name+"."+op.Name, op, r)
			}
		}
	}
}

// checkFilter is checkFilters for one operation; owner is Set.operation.
func checkFilter(schema *ir.Schema, in Input, owner string, op *ir.FieldDef, r *Result) {
	filter := op.Filter
	name := filter.Argument
	if name == "" {
		r.errorf("", "%s: its filter names no argument; a filter is an argument of the operation, filter: FilterBy<Item, Fields>", owner)
		name = ir.FilterParam
	}
	if !strings.EqualFold(op.HTTPMethod, "GET") || !op.TypeRef.IsArray || op.TypeRef.IsMap || op.TypeRef.IsArrayOfArrays {
		r.errorf("", "%s: filter argument %s filters a list, and the operation is not a GET whose result is a list", owner, name)
	}
	for _, arg := range op.Arguments {
		switch {
		case arg == nil:
		case arg.Name == name:
			r.errorf("", "%s: filter argument %s has the name of another argument", owner, name)
		case arg.Name == ir.FilterParam:
			r.errorf("", "%s: argument %s has the name of the query parameter the filter %s travels under, %s[field]; give it another name", owner, arg.Name, name, ir.FilterParam)
		}
	}
	if op.Page != nil && slices.Contains(ir.PageParameterNames, name) {
		r.errorf("", "%s: filter argument %s has the name of a page's parameter (%s)", owner, name, strings.Join(ir.PageParameterNames, ", "))
	}
	if len(filter.Fields) == 0 {
		r.errorf("", "%s: filter argument %s names no field", owner, name)
		return
	}

	itemName := op.TypeRef.Name
	item, itemSchema, found := sortItem(schema, in, itemName)
	if !found {
		if importedType(schema, itemName) {
			r.errorf("", "%s: filter argument %s filters %s, which the loader could not read from the service that declares it; a filter's fields are held to their item", owner, name, itemName)
			return
		}
		// A primitive, an enum or a union declares no field.
		seen := map[string]bool{}
		for _, field := range filter.Fields {
			if field != nil && !seen[field.Name] {
				seen[field.Name] = true
				r.errorf("", "%s: filter argument %s names %s, which %s does not declare", owner, name, field.Name, itemName)
			}
		}
		return
	}

	seen := map[string]bool{}
	for _, field := range filter.Fields {
		if field == nil {
			r.errorf("", "%s: filter argument %s holds an empty field", owner, name)
			continue
		}
		if seen[field.Name] {
			r.errorf("", "%s: filter argument %s names %s twice", owner, name, field.Name)
			continue
		}
		seen[field.Name] = true
		declared := ownField(item, field.Name)
		if declared == nil {
			r.errorf("", "%s: filter argument %s names %s, which %s does not declare", owner, name, field.Name, item.Name)
			continue
		}
		kind, problem := FilterFieldKind(schema, itemSchema, in, item, declared)
		if problem != "" {
			r.errorf("", "%s: filter argument %s names %s.%s, %s", owner, name, item.Name, declared.Name, problem)
			continue
		}
		operators := ir.FilterOperators(kind, !declared.Required)
		if field.Kind != "" && field.Kind != kind {
			r.errorf("", "%s: filter argument %s gives %s the kind %s, and %s.%s is %s; leave the kind out, the loader fills it", owner, name, field.Name, field.Kind, item.Name, declared.Name, kind)
			continue
		}
		if field.Operators != nil && !slices.Equal(field.Operators, operators) {
			r.errorf("", "%s: filter argument %s gives %s the operators %s, and a field of kind %s takes %s; leave them out, the loader fills them", owner, name, field.Name, strings.Join(field.Operators, ", "), kind, strings.Join(operators, ", "))
			continue
		}
		if field.TypeRef != nil && *field.TypeRef != (ir.TypeRef{Name: declared.TypeRef.Name}) {
			r.errorf("", "%s: filter argument %s gives %s the type %s, and %s.%s holds %s; leave the type out, the loader fills it", owner, name, field.Name, typeLabel(*field.TypeRef), item.Name, declared.Name, declared.TypeRef.Name)
			continue
		}
		field.Kind = kind
		field.Operators = operators
		field.TypeRef = &ir.TypeRef{Name: declared.TypeRef.Name}
	}
}

// FilterFieldKind is the kind of filter a field of item takes (D106), one
// of ir.FilterKinds, or the reason it takes none, worded to follow "names
// Item.field, ". The kind is the one both ORMs give a column of the
// field's type (sqlutil.ColumnClass.FilterKind), with an enum its own, so
// a view's field filters as its column does. itemSchema declares item,
// whose scalars and enums type its fields; a type it lacks is read from
// schema and the compiler-resolved enums.
func FilterFieldKind(schema, itemSchema *ir.Schema, in Input, item *ir.TypeDef, field *ir.FieldDef) (kind, problem string) {
	ref := field.TypeRef
	label := typeLabel(ref)
	switch {
	case ref.IsArray || ref.IsMap:
		return "", "which holds " + label + "; a filter compares one value"
	case field.Secret:
		return "", "which is Secret; a filter would show it"
	case field.Decrypted:
		return "", "which is @decrypted: its column holds ciphertext, which no filter compares"
	case item.Source != nil && field.Virtual:
		return "", "which is @virtual: no column of " + item.Source.Target + " holds it, so the table cannot filter by it"
	case item.Source != nil && (field.Name == sqlutil.AuditDeletedAt || field.Name == sqlutil.AuditDeletedBy):
		// Who created a row, who last wrote it and when are filtered by
		// as any id and time are (D108, D110): the repositories keep their
		// filters. A deleted row is left out of every read.
		return "", "which says a row is deleted, and a list holds no deleted row to compare it on"
	case field.JsonField:
		return "", "which holds JSON; a filter compares one text, number, time or boolean"
	}
	isEnum, isUnion, isObject := false, false, false
	var scalar sqlutil.ScalarClass
	isScalar := false
	for _, declaring := range []*ir.Schema{itemSchema, schema} {
		if declaring == nil {
			continue
		}
		if _, ok := declaring.Enums[ref.Name]; ok {
			isEnum = true
		}
		if _, ok := declaring.Unions[ref.Name]; ok {
			isUnion = true
		}
		if _, ok := declaring.Types[ref.Name]; ok {
			isObject = true
		}
		if def := declaring.Scalars[ref.Name]; def != nil && !isScalar {
			scalar, isScalar = sqlutil.ScalarClasses(declaring)[ref.Name], true
		}
	}
	if _, ok := in.ExternalEnums[ref.Name]; ok {
		isEnum = true
	}
	if isUnion || isObject {
		return "", "which holds " + label + "; a filter compares one text, number, time or boolean"
	}
	class := sqlutil.ClassifyColumn(field, itemSchema, scalar, isScalar, isEnum, isUnion)
	if isEnum {
		return ir.FilterKindEnum, ""
	}
	switch class.FilterKind(false) {
	case sqlutil.FilterUUID:
		return ir.FilterKindUUID, ""
	case sqlutil.FilterString:
		if !isScalar && ref.Name != "string" {
			// A name the loader knows no type for compares as text in the
			// ORMs; a filter needs to know what it reads.
			return "", "which holds " + label + ", a type the loader cannot classify"
		}
		return ir.FilterKindString, ""
	case sqlutil.FilterInt:
		return ir.FilterKindInt, ""
	case sqlutil.FilterFloat:
		return ir.FilterKindFloat, ""
	case sqlutil.FilterDecimal:
		return ir.FilterKindDecimal, ""
	case sqlutil.FilterBool:
		return ir.FilterKindBool, ""
	case sqlutil.FilterDateTime:
		return ir.FilterKindDateTime, ""
	case sqlutil.FilterDate:
		return ir.FilterKindDate, ""
	}
	return "", "which holds " + label + "; a filter compares one text, number, time or boolean"
}
