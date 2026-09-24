package registry

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// projectionDecorators returns @projection and @join on type declarations
// and @column on fields: the authoring surface of SQL projection views, all
// from @superschematic/db and all restricted to DB schemas.
//
// These decorators check the shape of their arguments. Whether the names
// they carry resolve (the source and joined tables, every alias.field, the
// column types) is a property of the IR and belongs to verify, which runs
// for the data forms too (internal/loader/verify/projection.go).
func projectionDecorators() []DecoratorSpec {
	db := []string{string(ir.SchemaKindDB)}
	return []DecoratorSpec{
		{
			Name: "projection", Packages: []string{pkgDB}, Target: TargetType, Kinds: db,
			typeArgs: 1,
			Apply: func(n Node, args []any, _ Site) error {
				return applyProjection(n.Type, args)
			},
		},
		{
			Name: "join", Packages: []string{pkgDB}, Target: TargetType, Kinds: db,
			typeArgs: 1,
			Apply: func(n Node, args []any, _ Site) error {
				return applyJoin(n.Type, args)
			},
		},
		{
			Name: "column", Packages: []string{pkgDB}, Target: TargetField, Kinds: db,
			Apply: func(n Node, args []any, _ Site) error {
				return applyColumn(n.Field, args)
			},
		},
	}
}

// applyProjection reads @projection<Source>({ pool, name, migration,
// where?, collapse? }). args[0] is the source class name the frontend
// resolved from the type argument. Joins a @join above the @projection
// already collected are kept.
func applyProjection(td *ir.TypeDef, args []any) error {
	if len(args) != 2 {
		return fmt.Errorf("@projection takes exactly one options object")
	}
	source, _ := args[0].(string)
	opts, ok := args[1].(map[string]any)
	if !ok {
		return ArgErrorf(1, "@projection options must be an object literal")
	}
	if td.Projection != nil && td.Projection.Source != "" {
		return fmt.Errorf("a class takes one @projection")
	}
	def := &ir.ProjectionDef{Source: source}
	if td.Projection != nil {
		def.Joins = td.Projection.Joins
	}
	for _, key := range sortedKeys(opts) {
		value := opts[key]
		switch key {
		case "pool", "name", "migration":
			s, ok := value.(string)
			if !ok {
				return ArgErrorf(1, "@projection %s must be a string literal", key)
			}
			switch key {
			case "pool":
				def.Pool = s
			case "name":
				def.Name = s
			case "migration":
				def.Migration = s
			}
		case "where":
			list, ok := value.([]any)
			if !ok {
				return ArgErrorf(1, "@projection where must be an array of row rules")
			}
			for i, entry := range list {
				pred, err := projectionPredicate(fmt.Sprintf("where[%d]", i), entry)
				if err != nil {
					return err
				}
				def.Predicates = append(def.Predicates, pred)
			}
		case "collapse":
			collapse, err := projectionCollapse(value)
			if err != nil {
				return err
			}
			def.Collapse = collapse
		default:
			return ArgErrorf(1, "@projection options has unknown key %q", key)
		}
	}
	for _, required := range []struct{ key, value string }{
		{"pool", def.Pool}, {"name", def.Name}, {"migration", def.Migration},
	} {
		if required.value == "" {
			return ArgErrorf(1, "@projection requires a non-empty %s", required.key)
		}
	}
	td.Role = ir.RoleProjection
	td.Projection = def
	return nil
}

// projectionPredicate reads one row rule: a binding { column, setting,
// optional? }, { anyOf: [binding, ...] }, a literal rule { column,
// isNull: true | notNull: true | equals: literal }, or a function rule
// { function, args, requiredSettings }, each with an optional
// when: { column, equals }.
func projectionPredicate(where string, value any) (*ir.ProjectionPredicate, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, ArgErrorf(1, "@projection %s must be an object literal", where)
	}
	pred := &ir.ProjectionPredicate{}
	for _, key := range sortedKeys(obj) {
		v := obj[key]
		switch key {
		case "column", "setting", "function":
			s, ok := v.(string)
			if !ok {
				return nil, ArgErrorf(1, "@projection %s.%s must be a string literal", where, key)
			}
			switch key {
			case "column":
				pred.Column = s
			case "setting":
				pred.Setting = s
			case "function":
				pred.Function = s
			}
		case "args", "requiredSettings":
			list, err := nonEmptyStrings(v)
			if err != nil {
				return nil, ArgErrorf(1, "@projection %s.%s %s", where, key, err)
			}
			if key == "args" {
				pred.Args = list
			} else {
				pred.RequiredSettings = list
			}
		case "optional":
			b, ok := v.(bool)
			if !ok {
				return nil, ArgErrorf(1, "@projection %s.optional must be a boolean literal", where)
			}
			pred.Optional = b
		case "isNull", "notNull":
			if b, ok := v.(bool); !ok || !b {
				return nil, ArgErrorf(1, "@projection %s.%s must be the literal true", where, key)
			}
			if key == "isNull" {
				pred.IsNull = true
			} else {
				pred.NotNull = true
			}
		case "equals":
			lit := &ir.ProjectionLiteral{}
			switch typed := v.(type) {
			case string:
				lit.String = &typed
			case float64:
				lit.Number = &typed
			case bool:
				lit.Bool = &typed
			default:
				return nil, ArgErrorf(1, "@projection %s.equals must be a string, number or boolean literal", where)
			}
			pred.Equals = lit
		case "anyOf":
			list, ok := v.([]any)
			if !ok {
				return nil, ArgErrorf(1, "@projection %s.anyOf must be an array of { column, setting } objects", where)
			}
			for i, entry := range list {
				alt, err := projectionPredicate(fmt.Sprintf("%s.anyOf[%d]", where, i), entry)
				if err != nil {
					return nil, err
				}
				if !alt.IsBinding() || alt.When != nil {
					return nil, ArgErrorf(1, "@projection %s.anyOf entries carry only column, setting and optional", where)
				}
				pred.AnyOf = append(pred.AnyOf, alt)
			}
		case "when":
			cond, err := projectionCondition(where, v)
			if err != nil {
				return nil, err
			}
			pred.When = cond
		default:
			return nil, ArgErrorf(1, "@projection %s has unknown key %q", where, key)
		}
	}
	return pred, checkPredicateShape(where, pred)
}

// checkPredicateShape rejects a rule that mixes forms or misses a part of
// its own form.
func checkPredicateShape(where string, pred *ir.ProjectionPredicate) error {
	readsSetting := pred.Setting != "" || pred.Optional || len(pred.AnyOf) > 0
	switch {
	case pred.IsLiteral():
		forms := 0
		for _, set := range []bool{pred.IsNull, pred.NotNull, pred.Equals != nil} {
			if set {
				forms++
			}
		}
		if forms != 1 {
			return ArgErrorf(1, "@projection %s carries exactly one of isNull, notNull and equals", where)
		}
		if pred.Column == "" {
			return ArgErrorf(1, "@projection %s literal rule requires column", where)
		}
		if readsSetting || pred.Function != "" || len(pred.Args) > 0 || len(pred.RequiredSettings) > 0 {
			return ArgErrorf(1, "@projection %s literal rule carries only column and when; it reads no setting", where)
		}
	case pred.Function != "":
		if pred.Column != "" || readsSetting || len(pred.Args) == 0 || len(pred.RequiredSettings) == 0 {
			return ArgErrorf(1, "@projection %s function rule takes function, args and requiredSettings only", where)
		}
	case len(pred.Args) > 0 || len(pred.RequiredSettings) > 0:
		return ArgErrorf(1, "@projection %s args and requiredSettings require function", where)
	case len(pred.AnyOf) > 0:
		if pred.Column != "" || pred.Setting != "" || pred.Optional {
			return ArgErrorf(1, "@projection %s carries either anyOf or a column and setting, not both", where)
		}
		if len(pred.AnyOf) < 2 {
			return ArgErrorf(1, "@projection %s.anyOf needs at least two alternatives; a single binding is a plain rule", where)
		}
	case pred.Column == "" || pred.Setting == "":
		return ArgErrorf(1, "@projection %s requires column and setting", where)
	}
	return nil
}

func projectionCondition(where string, value any) (*ir.ProjectionCondition, error) {
	guard, ok := value.(map[string]any)
	if !ok {
		return nil, ArgErrorf(1, "@projection %s.when must be an object literal with column and equals", where)
	}
	cond := &ir.ProjectionCondition{}
	for _, key := range sortedKeys(guard) {
		s, ok := guard[key].(string)
		if !ok {
			return nil, ArgErrorf(1, "@projection %s.when.%s must be a string literal", where, key)
		}
		switch key {
		case "column":
			cond.Column = s
		case "equals":
			cond.Equals = s
		default:
			return nil, ArgErrorf(1, "@projection %s.when has unknown key %q", where, key)
		}
	}
	if cond.Column == "" || cond.Equals == "" {
		return nil, ArgErrorf(1, "@projection %s.when requires column and equals", where)
	}
	return cond, nil
}

// projectionCollapse reads collapse: { by: [alias.field, ...], order?: [{
// column, rank? | direction?, nulls? }, ...] }.
func projectionCollapse(value any) (*ir.ProjectionCollapse, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, ArgErrorf(1, "@projection collapse must be an object literal with by and an optional order")
	}
	collapse := &ir.ProjectionCollapse{}
	for _, key := range sortedKeys(obj) {
		v := obj[key]
		switch key {
		case "by":
			list, err := nonEmptyStrings(v)
			if err != nil {
				return nil, ArgErrorf(1, "@projection collapse.by %s", err)
			}
			collapse.By = list
		case "order":
			list, ok := v.([]any)
			if !ok {
				return nil, ArgErrorf(1, "@projection collapse.order must be an array of order objects")
			}
			for i, entry := range list {
				term, err := projectionOrder(i, entry)
				if err != nil {
					return nil, err
				}
				collapse.Order = append(collapse.Order, term)
			}
		default:
			return nil, ArgErrorf(1, "@projection collapse has unknown key %q", key)
		}
	}
	if len(collapse.By) == 0 {
		return nil, ArgErrorf(1, "@projection collapse requires by")
	}
	return collapse, nil
}

func projectionOrder(index int, value any) (*ir.ProjectionOrder, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, ArgErrorf(1, "@projection collapse.order[%d] must be an object literal with column", index)
	}
	term := &ir.ProjectionOrder{}
	for _, key := range sortedKeys(obj) {
		v := obj[key]
		switch key {
		case "column", "direction", "nulls":
			s, ok := v.(string)
			if !ok {
				return nil, ArgErrorf(1, "@projection collapse.order[%d].%s must be a string literal", index, key)
			}
			switch key {
			case "column":
				term.Column = s
			case "direction":
				term.Direction = s
			case "nulls":
				term.Nulls = s
			}
		case "rank":
			list, err := nonEmptyStrings(v)
			if err != nil {
				return nil, ArgErrorf(1, "@projection collapse.order[%d].rank %s", index, err)
			}
			term.Rank = list
		default:
			return nil, ArgErrorf(1, "@projection collapse.order[%d] has unknown key %q", index, key)
		}
	}
	if term.Column == "" {
		return nil, ArgErrorf(1, "@projection collapse.order[%d] requires column", index)
	}
	return term, nil
}

// applyJoin reads @join<Table>(alias, { "a.field": "b.field", ... }, kind?).
// args[0] is the joined class name the frontend resolved from the type
// argument. A @join written above the @projection lands on a placeholder
// declaration that applyProjection completes.
func applyJoin(td *ir.TypeDef, args []any) error {
	if len(args) < 3 || len(args) > 4 {
		return fmt.Errorf("@join takes an alias, an on-object and an optional kind")
	}
	table, _ := args[0].(string)
	alias, ok := args[1].(string)
	if !ok || alias == "" {
		return ArgErrorf(1, "@join alias must be a non-empty string literal")
	}
	on, ok := args[2].(map[string]any)
	if !ok || len(on) == 0 {
		return ArgErrorf(2, "@join on must be a non-empty object literal of alias.field equalities")
	}
	j := &ir.ProjectionJoin{Type: table, Alias: alias, Kind: "inner"}
	// Object literal keys arrive as an unordered map; the join condition is
	// a conjunction, so sorted order is canonical.
	for _, left := range sortedKeys(on) {
		right, ok := on[left].(string)
		if !ok {
			return ArgErrorf(2, "@join on values must be alias.field string literals")
		}
		j.On = append(j.On, &ir.ProjectionJoinKey{Left: left, Right: right})
	}
	if len(args) == 4 {
		kind, ok := args[3].(string)
		if !ok || (kind != "inner" && kind != "left") {
			return ArgErrorf(3, `@join kind must be "inner" or "left"`)
		}
		j.Kind = kind
	}
	if td.Projection == nil {
		td.Projection = &ir.ProjectionDef{}
	}
	td.Projection.Joins = append(td.Projection.Joins, j)
	return nil
}

// applyColumn reads @column("alias.field") or @column({ function, args }).
func applyColumn(fd *ir.FieldDef, args []any) error {
	if fd.ProjectedFrom != "" || fd.ProjectedFunction != nil {
		return fmt.Errorf("a projection column takes one @column")
	}
	if len(args) != 1 {
		return fmt.Errorf("@column takes exactly one alias.field string or function object")
	}
	if s, ok := args[0].(string); ok {
		if s == "" {
			return ArgErrorf(0, "@column requires a non-empty alias.field")
		}
		fd.ProjectedFrom = s
		return nil
	}
	obj, ok := args[0].(map[string]any)
	if !ok || len(obj) != 2 {
		return ArgErrorf(0, "@column takes an alias.field string or an object with only function and args")
	}
	name, ok := obj["function"].(string)
	if !ok || name == "" {
		return ArgErrorf(0, "@column function must be a non-empty schema.function string")
	}
	refs, err := nonEmptyStrings(obj["args"])
	if err != nil {
		return ArgErrorf(0, "@column args %s", err)
	}
	fd.ProjectedFunction = &ir.ProjectionFunctionCall{Function: name, Args: refs}
	return nil
}

// nonEmptyStrings reads a non-empty array of non-empty string literals.
func nonEmptyStrings(value any) ([]string, error) {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("must be a non-empty array of string literals")
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		s, ok := entry.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("must contain non-empty string literals")
		}
		out = append(out, s)
	}
	return out, nil
}
