package ir

// RoleProjection marks a @projection declaration in a DB schema: a read-only
// relation over tables of the same schema, generated as a Postgres view.
// A projection is never a table and never a language type; the type
// generators do not emit it.
const RoleProjection Role = "Projection"

// ProjectionBaseAlias is the alias every projection addresses its source
// table by in alias.field references.
const ProjectionBaseAlias = "base"

// ProjectionDef is the @projection declaration of a RoleProjection type. The
// sql generator builds the view, its migration and its Arrow schema from it;
// nothing about the view is written by hand.
//
// Every alias.field reference names a schema field (camelCase) of the table
// behind the alias, never a SQL column: "base" is the source table and each
// join adds its own alias. The generator resolves a reference to the column
// (a relation field to its foreign-key column).
type ProjectionDef struct {
	// Pool is the Postgres schema the view is created in; readers address
	// the view as pool.name.
	Pool string `json:"pool" yaml:"pool"`

	// Name is the view's name inside Pool.
	Name string `json:"name" yaml:"name"`

	// Migration is the 14-digit version stamp of the generated migration
	// (golang-migrate style). A changed declaration takes a new stamp: an
	// applied migration is never rewritten.
	Migration string `json:"migration" yaml:"migration"`

	// Source is the DB table type the view reads, addressed as "base".
	Source string `json:"source" yaml:"source"`

	// Joins lists the other tables the view reads, in declaration order.
	Joins []*ProjectionJoin `json:"joins,omitempty" yaml:"joins,omitempty"`

	// Predicates lists the row rules, all ANDed, in declaration order (the
	// `where` option of @projection).
	Predicates []*ProjectionPredicate `json:"predicates,omitempty" yaml:"predicates,omitempty"`

	// Collapse keeps one row per key: the view is generated with
	// DISTINCT ON (By...) ORDER BY By..., Order..., so the first row in that
	// order wins. Nil serves every row the predicates admit.
	Collapse *ProjectionCollapse `json:"collapse,omitempty" yaml:"collapse,omitempty"`
}

// ProjectionJoin is one joined table of a projection view.
type ProjectionJoin struct {
	// Type is the joined DB table type.
	Type string `json:"type" yaml:"type"`

	// Alias is the name columns and predicates address the table by.
	Alias string `json:"alias" yaml:"alias"`

	// Kind is "inner" or "left". A left join makes every column read
	// through the alias nullable.
	Kind string `json:"kind" yaml:"kind"`

	// On lists the equalities the join is made on, ANDed.
	On []*ProjectionJoinKey `json:"on" yaml:"on"`
}

// ProjectionJoinKey is one alias.field = alias.field equality of a join.
type ProjectionJoinKey struct {
	Left  string `json:"left" yaml:"left"`
	Right string `json:"right" yaml:"right"`
}

// ProjectionPredicate is one row rule of a projection view. It takes one of
// four forms:
//
//   - a setting binding: Column = current_setting(Setting), cast to the
//     column's type. An unset setting makes current_setting raise, so the
//     view fails closed. Optional reads the setting with
//     current_setting(Setting, true) and turns an empty value into NULL, so
//     an unset or empty setting matches no row;
//   - AnyOf: two or more bindings of which one must hold (ORed);
//   - a literal rule: Column IS NULL, IS NOT NULL, or equals a typed
//     literal. It reads no setting;
//   - a function rule: a named SQL function over alias.field arguments must
//     return true, and every RequiredSettings entry must be set.
//
// When narrows any form to the rows whose When.Column equals When.Equals;
// every other row passes the rule.
type ProjectionPredicate struct {
	// Function names a schema-qualified SQL function (schema.function) the
	// migrations own. Args are alias.field references, never SQL or
	// literals. RequiredSettings lists the settings the function reads; the
	// view refuses rows while any of them is unset or empty.
	Function         string   `json:"function,omitempty" yaml:"function,omitempty"`
	Args             []string `json:"args,omitempty" yaml:"args,omitempty"`
	RequiredSettings []string `json:"requiredSettings,omitempty" yaml:"requiredSettings,omitempty"`

	// Column is the alias.field the rule reads. Empty for AnyOf and
	// function rules.
	Column string `json:"column,omitempty" yaml:"column,omitempty"`

	// Setting is the dotted Postgres setting a binding compares Column to.
	Setting string `json:"setting,omitempty" yaml:"setting,omitempty"`

	// Optional lets the caller leave Setting unset: no row matches instead
	// of the view raising.
	Optional bool `json:"optional,omitempty" yaml:"optional,omitempty"`

	// AnyOf lists alternative bindings of which one must hold. An
	// alternative carries Column, Setting and Optional only; the When guard
	// belongs to the enclosing rule.
	AnyOf []*ProjectionPredicate `json:"anyOf,omitempty" yaml:"anyOf,omitempty"`

	// IsNull, NotNull and Equals are the literal forms: exactly one is set,
	// with Column and an optional When.
	IsNull  bool               `json:"isNull,omitempty" yaml:"isNull,omitempty"`
	NotNull bool               `json:"notNull,omitempty" yaml:"notNull,omitempty"`
	Equals  *ProjectionLiteral `json:"equals,omitempty" yaml:"equals,omitempty"`

	// When applies the rule only to rows whose When.Column equals
	// When.Equals; every other row passes. Nil applies it to every row.
	When *ProjectionCondition `json:"when,omitempty" yaml:"when,omitempty"`
}

// IsLiteral reports whether the predicate is a literal rule (isNull,
// notNull or equals) rather than a setting binding or a function rule.
func (p *ProjectionPredicate) IsLiteral() bool {
	return p != nil && (p.IsNull || p.NotNull || p.Equals != nil)
}

// IsBinding reports whether the predicate is a single column = setting
// binding: not an AnyOf, a literal or a function rule. Optional and When
// may still be set.
func (p *ProjectionPredicate) IsBinding() bool {
	return p != nil && !p.IsLiteral() && p.Function == "" && len(p.AnyOf) == 0 && p.Column != "" && p.Setting != ""
}

// ProjectionLiteral is the typed literal of an equals rule; exactly one
// field is set. Typed pointers keep the JSON and YAML forms stable: a bare
// number decodes as an int in YAML and a float64 in JSON.
type ProjectionLiteral struct {
	String *string  `json:"string,omitempty" yaml:"string,omitempty"`
	Number *float64 `json:"number,omitempty" yaml:"number,omitempty"`
	Bool   *bool    `json:"bool,omitempty" yaml:"bool,omitempty"`
}

// Kind names the literal's type: "string", "number", "boolean", or "" when
// no field is set.
func (l *ProjectionLiteral) Kind() string {
	switch {
	case l == nil:
		return ""
	case l.String != nil:
		return "string"
	case l.Number != nil:
		return "number"
	case l.Bool != nil:
		return "boolean"
	}
	return ""
}

// Set reports how many of the literal's fields are set.
func (l *ProjectionLiteral) Set() int {
	if l == nil {
		return 0
	}
	n := 0
	for _, set := range []bool{l.String != nil, l.Number != nil, l.Bool != nil} {
		if set {
			n++
		}
	}
	return n
}

// ProjectionCondition is the alias.field = 'literal' guard of a rule.
type ProjectionCondition struct {
	Column string `json:"column" yaml:"column"`
	Equals string `json:"equals" yaml:"equals"`
}

// ProjectionCollapse is the DISTINCT ON declaration of a projection.
type ProjectionCollapse struct {
	// By lists the alias.field references that identify one row.
	By []string `json:"by" yaml:"by"`

	// Order ranks the rows that share a key; the first wins. The By
	// columns always lead the ORDER BY, as DISTINCT ON requires.
	Order []*ProjectionOrder `json:"order,omitempty" yaml:"order,omitempty"`
}

// ProjectionOrder is one ORDER BY term of a collapse: either Rank, the
// column's literal values in winning order (every other value ranks last),
// or a Direction ("asc", the default, or "desc") with an optional Nulls
// placement ("first" or "last").
type ProjectionOrder struct {
	Column    string   `json:"column" yaml:"column"`
	Rank      []string `json:"rank,omitempty" yaml:"rank,omitempty"`
	Direction string   `json:"direction,omitempty" yaml:"direction,omitempty"`
	Nulls     string   `json:"nulls,omitempty" yaml:"nulls,omitempty"`
}

// ProjectionFunctionCall is the computed form of a projection column: a
// schema-qualified SQL function the migrations own, called with alias.field
// arguments. The column's declared type and nullability are the function's
// result contract; the generator does not read the function's signature.
type ProjectionFunctionCall struct {
	Function string   `json:"function" yaml:"function"`
	Args     []string `json:"args" yaml:"args"`
}

// ProjectionSource returns the alias.field a projection column reads: the
// @column reference, or base.<field name>.
func (f *FieldDef) ProjectionSource() string {
	if f.ProjectedFrom != "" {
		return f.ProjectedFrom
	}
	return ProjectionBaseAlias + "." + f.Name
}

// Projections returns the schema's RoleProjection types sorted by name.
func (s *Schema) Projections() []*TypeDef {
	var defs []*TypeDef
	for _, name := range sortedStringMapKeys(s.Types) {
		if td := s.Types[name]; td != nil && td.Role == RoleProjection {
			defs = append(defs, td)
		}
	}
	return defs
}
