package registry

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// queryDecorator returns @query, from @superschematic/db and only in DB
// schemas: @query<Params, Row>({ name, sql, ... }) declares a named SQL
// statement of the database on a class of its own (D86). Its two type
// arguments name the types of its parameters and of its rows, a class or
// an object type alias each. The class becomes a RoleQuery type, which no
// generator makes a table or a language type of; the walker makes its
// Params and Row classes embedded structs.
//
// Apply checks the shape of the options. Whether the SQL uses each
// parameter, and what the types hold, is verification's, which reads the
// fields (internal/loader/verify/queries.go).
func queryDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "query", Packages: []string{pkgDB}, Target: TargetType,
		Kinds:          []string{string(ir.SchemaKindDB)},
		typeArgs:       2,
		typeArgAliases: true,
		Apply: func(n Node, args []any, _ Site) error {
			def, err := queryOf(n.Type, args)
			if err != nil {
				return err
			}
			n.Type.Role = ir.RoleQuery
			n.Type.Query = def
			return nil
		},
	}
}

// queryOptions names @query's options, for its errors.
const queryOptions = "name, sql, notNull, orderBy, page and dialects"

// queryOf reads @query<Params, Row>({ name?, sql, notNull?, orderBy?, page?,
// dialects? }). args[0] and args[1] are the type arguments' names; an
// ArgError's index counts them first.
func queryOf(td *ir.TypeDef, args []any) (*ir.QueryDef, error) {
	if len(args) != 3 {
		return nil, fmt.Errorf("@query takes one options object: { name?, sql, notNull?, orderBy?, page?, dialects? }")
	}
	if td.Query != nil {
		return nil, fmt.Errorf("a class takes one @query")
	}
	params, _ := args[0].(string)
	row, _ := args[1].(string)
	opts, ok := args[2].(map[string]any)
	if !ok {
		return nil, ArgErrorf(2, "@query takes one options object: { name?, sql, notNull?, orderBy?, page?, dialects? }")
	}
	def := &ir.QueryDef{Params: params, Row: row, Name: lowerFirstRune(td.Name)}
	for _, key := range sortedKeys(opts) {
		value := opts[key]
		switch key {
		case "name", "sql":
			s, ok := value.(string)
			if !ok || s == "" {
				return nil, ArgErrorf(2, "@query %s must be a non-empty string literal", key)
			}
			if key == "name" {
				def.Name = s
			} else {
				def.SQL = s
			}
		case "notNull", "dialects":
			list, err := stringList(value)
			if err != nil {
				return nil, ArgErrorf(2, "@query %s must be a list of strings", key)
			}
			if key == "notNull" {
				def.NotNull = list
			} else {
				def.Dialects = list
			}
		case "orderBy":
			orderBy, err := queryOrderBy(value)
			if err != nil {
				return nil, err
			}
			def.OrderBy = orderBy
		case "page":
			page, err := queryPage(value)
			if err != nil {
				return nil, err
			}
			def.Page = page
		default:
			return nil, ArgErrorf(2, "@query has no option %q; it takes %s", key, queryOptions)
		}
	}
	if def.SQL == "" {
		return nil, ArgErrorf(2, "@query needs its sql")
	}
	return def, nil
}

// queryOrderBy reads orderBy: { param, options: { value: fragment }, default? }.
func queryOrderBy(value any) (*ir.QueryOrderBy, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, ArgErrorf(2, "@query orderBy must be an object: { param, options, default? }")
	}
	out := &ir.QueryOrderBy{Options: map[string]string{}}
	for _, key := range sortedKeys(obj) {
		switch key {
		case "param", "default":
			s, ok := obj[key].(string)
			if !ok || s == "" {
				return nil, ArgErrorf(2, "@query orderBy.%s must be a non-empty string literal", key)
			}
			if key == "param" {
				out.Param = s
			} else {
				out.Default = s
			}
		case "options":
			options, ok := obj[key].(map[string]any)
			if !ok || len(options) == 0 {
				return nil, ArgErrorf(2, "@query orderBy.options must map each value of the parameter to an ORDER BY fragment")
			}
			for option, fragment := range options {
				s, ok := fragment.(string)
				if !ok || s == "" {
					return nil, ArgErrorf(2, "@query orderBy.options.%s must be a non-empty string literal, an ORDER BY fragment", option)
				}
				out.Options[option] = s
			}
		default:
			return nil, ArgErrorf(2, "@query orderBy has no option %q; it takes param, options and default", key)
		}
	}
	if out.Param == "" || len(out.Options) == 0 {
		return nil, ArgErrorf(2, "@query orderBy needs a param and its options")
	}
	return out, nil
}

// queryPage reads page: { key: [field, ...], desc? }.
func queryPage(value any) (*ir.QueryPage, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, ArgErrorf(2, "@query page must be an object: { key, desc? }")
	}
	out := &ir.QueryPage{}
	for _, key := range sortedKeys(obj) {
		switch key {
		case "key":
			list, err := stringList(obj[key])
			if err != nil || len(list) == 0 {
				return nil, ArgErrorf(2, "@query page.key must be a non-empty list of row fields")
			}
			out.Key = list
		case "desc":
			b, ok := obj[key].(bool)
			if !ok {
				return nil, ArgErrorf(2, "@query page.desc must be a boolean")
			}
			out.Desc = b
		default:
			return nil, ArgErrorf(2, "@query page has no option %q; it takes key and desc", key)
		}
	}
	if len(out.Key) == 0 {
		return nil, ArgErrorf(2, "@query page needs its key")
	}
	return out, nil
}

// lowerFirstRune is name with its first letter in lower case: a query's
// default name, categoryTotals for the class CategoryTotals.
func lowerFirstRune(name string) string {
	if name == "" {
		return name
	}
	b := []byte(name)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 'a' - 'A'
	}
	return string(b)
}
