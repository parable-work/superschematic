package verify

import (
	"fmt"
	"regexp"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

var (
	// projectionIdentifierPattern covers pool names, view names, join
	// aliases and the parts of a function name: they are interpolated into
	// DDL.
	projectionIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

	// migrationStampPattern is the golang-migrate version prefix.
	migrationStampPattern = regexp.MustCompile(`^[0-9]{14}$`)

	// settingNamePattern is a custom Postgres setting name: dotless names
	// are reserved for server settings, so a dot is required.
	settingNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
)

// checkProjections verifies every @projection declaration: the role and
// kind pairing, the identifiers the DDL is built from, that every
// alias.field reference resolves to a field of a DB table the view reads,
// that each column keeps its source column's type and nullability, and the
// shape of every row rule and of the collapse. A projection that fails any
// of these is refused before generation, so no view is emitted over a
// column that does not exist.
//
// Which row rules a view must carry is not checked here: that is a
// distribution's policy, registered as a check (Registry.RegisterCheck).
func checkProjections(schema *ir.Schema, r *Result) {
	seenRelations := map[string]string{}
	seenMigrations := map[string]string{}

	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td.Role != ir.RoleProjection {
			if td.Projection != nil {
				r.errorf(td.Owner, "%s: @join and a projection declaration need @projection on the class (this type has role %s)", td.Name, td.Role)
			}
			for _, fd := range td.Fields {
				if fd.ProjectedFrom != "" || fd.ProjectedFunction != nil {
					r.errorf(td.Owner, "%s.%s: @column is only valid on a @projection class", td.Name, fd.Name)
				}
			}
			continue
		}

		if schema.Kind != ir.SchemaKindDB {
			r.errorf(td.Owner, "%s: @projection declarations are only allowed in DB schemas (this service is kind %s)", td.Name, schema.Kind)
			continue
		}
		def := td.Projection
		if def == nil {
			r.errorf(td.Owner, "%s: a Projection-role type has no @projection declaration", td.Name)
			continue
		}
		checkProjectionClass(td, r)

		checkProjectionIdentifiers(td, def, r)
		relation := def.Pool + "." + def.Name
		if other, dup := seenRelations[relation]; dup {
			r.errorf(td.Owner, "%s: projection %s is already declared by %s", td.Name, relation, other)
		}
		seenRelations[relation] = td.Name
		if other, dup := seenMigrations[def.Migration]; dup && def.Migration != "" {
			r.errorf(td.Owner, "%s: migration stamp %s is already used by %s; every projection migration needs its own version", td.Name, def.Migration, other)
		}
		seenMigrations[def.Migration] = td.Name

		scope := resolveProjectionScope(schema, td, def, r)
		if scope == nil {
			continue
		}
		checkProjectionColumns(schema, td, scope, r)
		for i, pred := range def.Predicates {
			checkProjectionPredicate(td, fmt.Sprintf("where[%d]", i), pred, scope, r)
		}
		checkProjectionCollapse(td, def, scope, r)
	}
}

// checkProjectionClass refuses the class-level surface a view cannot carry:
// heritage, traits and table or payload decorators.
func checkProjectionClass(td *ir.TypeDef, r *Result) {
	if td.Extends != "" || len(td.Implements) > 0 || td.IsTrait {
		r.errorf(td.Owner, "%s: a @projection class cannot extend or implement anything; list its columns directly", td.Name)
	}
	if len(td.Indexes) > 0 || td.JsonField || td.Versioned || td.EnvVars || td.Source != nil ||
		td.StrictJSON || td.DenyUnknownFields {
		r.errorf(td.Owner, "%s: a @projection class carries only @projection and @join", td.Name)
	}
}

func checkProjectionIdentifiers(td *ir.TypeDef, def *ir.ProjectionDef, r *Result) {
	for _, part := range []struct{ key, value string }{{"pool", def.Pool}, {"name", def.Name}} {
		if !projectionIdentifierPattern.MatchString(part.value) {
			r.errorf(td.Owner, "%s: @projection %s must be a snake_case SQL identifier, got %q", td.Name, part.key, part.value)
		}
	}
	if !migrationStampPattern.MatchString(def.Migration) {
		r.errorf(td.Owner, "%s: @projection migration must be a 14-digit migration version stamp, got %q", td.Name, def.Migration)
	}
}

// projectionScope is the set of tables a projection can address, by alias.
type projectionScope struct {
	schema *ir.Schema

	// tables maps alias -> the DB table type behind it.
	tables map[string]*ir.TypeDef

	// nullable marks aliases whose columns are nullable in the view (left
	// joins).
	nullable map[string]bool

	// order lists aliases in FROM order: base first, then joins.
	order []string
}

// projectionTable resolves a type name to a DB table the view may read.
func projectionTable(schema *ir.Schema, name string) *ir.TypeDef {
	target := schema.Types[name]
	if target == nil || target.Role != ir.RoleDBTable || target.JsonField {
		return nil
	}
	return target
}

func resolveProjectionScope(schema *ir.Schema, td *ir.TypeDef, def *ir.ProjectionDef, r *Result) *projectionScope {
	source := projectionTable(schema, def.Source)
	if source == nil {
		r.errorf(td.Owner, "%s: @projection source %q is not a DB table type of this schema", td.Name, def.Source)
		return nil
	}
	scope := &projectionScope{
		schema:   schema,
		tables:   map[string]*ir.TypeDef{ir.ProjectionBaseAlias: source},
		nullable: map[string]bool{},
		order:    []string{ir.ProjectionBaseAlias},
	}

	ok := true
	for _, j := range def.Joins {
		if !projectionIdentifierPattern.MatchString(j.Alias) {
			r.errorf(td.Owner, "%s: @join alias must be a snake_case SQL identifier, got %q", td.Name, j.Alias)
			ok = false
			continue
		}
		if _, taken := scope.tables[j.Alias]; taken {
			r.errorf(td.Owner, "%s: @join alias %q is already in use (%q is the source table)", td.Name, j.Alias, ir.ProjectionBaseAlias)
			ok = false
			continue
		}
		table := projectionTable(schema, j.Type)
		if table == nil {
			r.errorf(td.Owner, "%s: @join table %q is not a DB table type of this schema", td.Name, j.Type)
			ok = false
			continue
		}
		if j.Kind != "inner" && j.Kind != "left" {
			r.errorf(td.Owner, "%s: @join %s kind must be \"inner\" or \"left\", got %q", td.Name, j.Alias, j.Kind)
			ok = false
		}
		scope.tables[j.Alias] = table
		scope.nullable[j.Alias] = j.Kind == "left"
		scope.order = append(scope.order, j.Alias)

		if len(j.On) == 0 {
			r.errorf(td.Owner, "%s: @join %s needs at least one alias.field equality", td.Name, j.Alias)
			ok = false
			continue
		}
		touchesAlias := false
		for _, key := range j.On {
			for _, side := range []string{key.Left, key.Right} {
				alias, _, _, problem := scope.resolve(side)
				if problem != "" {
					r.errorf(td.Owner, "%s: @join %s on %q: %s", td.Name, j.Alias, side, problem)
					ok = false
					continue
				}
				if alias == j.Alias {
					touchesAlias = true
				}
			}
		}
		if !touchesAlias {
			r.errorf(td.Owner, "%s: @join %s on-condition never references alias %q", td.Name, j.Alias, j.Alias)
			ok = false
		}
	}
	if !ok {
		return nil
	}
	return scope
}

// resolve splits an alias.field reference and finds the field on the
// aliased table. The reference is the schema field name, never a SQL column.
// An array of arrays is refused: no column, join key, row rule or collapse
// key reads one.
func (s *projectionScope) resolve(ref string) (alias string, table *ir.TypeDef, field *ir.FieldDef, problem string) {
	alias, fieldName, found := strings.Cut(ref, ".")
	if !found || alias == "" || fieldName == "" || strings.Contains(fieldName, ".") {
		return "", nil, nil, "expected an alias.field reference"
	}
	table = s.tables[alias]
	if table == nil {
		return "", nil, nil, fmt.Sprintf("unknown alias %q (declared: %s)", alias, strings.Join(s.order, ", "))
	}
	for _, fd := range table.Fields {
		if fd.Name != fieldName {
			continue
		}
		if fd.TypeRef.IsArrayOfArrays {
			return "", nil, nil, fmt.Sprintf("%s.%s is an array of arrays, which a projection cannot read", table.Name, fieldName)
		}
		return alias, table, fd, ""
	}
	return "", nil, nil, fmt.Sprintf("%s has no field %q", table.Name, fieldName)
}

// keyTypeName returns the type of a table's primary key, which is what a
// relation column physically stores: the @key field, else the field named
// id. "" when the table has neither.
func keyTypeName(table *ir.TypeDef) string {
	for _, fd := range table.Fields {
		if fd.Key {
			return fd.TypeRef.Name
		}
	}
	for _, fd := range table.Fields {
		if fd.Name == "id" {
			return fd.TypeRef.Name
		}
	}
	return ""
}

// relationKeyType returns the key type a relation field stores, or "" when
// the field is not a relation to a DB table of this schema.
func relationKeyType(schema *ir.Schema, field *ir.FieldDef) string {
	if target := projectionTable(schema, field.TypeRef.Name); target != nil {
		return keyTypeName(target)
	}
	if field.Relation != nil {
		if target := projectionTable(schema, field.Relation.Type); target != nil {
			return keyTypeName(target)
		}
	}
	return ""
}

func checkProjectionColumns(schema *ir.Schema, td *ir.TypeDef, scope *projectionScope, r *Result) {
	if len(td.Fields) == 0 {
		r.errorf(td.Owner, "%s: a @projection must expose at least one column", td.Name)
		return
	}
	for _, fd := range td.Fields {
		owner := td.Name + "." + fd.Name
		if fd.Key || fd.Unique || fd.SearchField || fd.JsonField || fd.AutoGenerated ||
			fd.HasMany || fd.ManyToMany || fd.Relation != nil || fd.Virtual || fd.SourceMustProject ||
			fd.Secret || fd.Encrypted || fd.Default != nil || fd.PlatformDefault != "" {
			r.errorf(td.Owner, "%s: projection columns carry only a name, a scalar or enum type, nullability and @column; table decorators and wrappers are not allowed", owner)
			continue
		}
		if fd.TypeRef.IsMap {
			r.errorf(td.Owner, "%s: projection columns cannot be maps", owner)
			continue
		}
		if fd.TypeRef.IsArrayOfArrays {
			r.errorf(td.Owner, "%s: projection columns cannot be arrays of arrays", owner)
			continue
		}
		if projectionTable(schema, fd.TypeRef.Name) != nil || isObjectType(schema, fd.TypeRef.Name) {
			r.errorf(td.Owner, "%s: projection columns must be scalars or enums; expose the key column instead of the %s object", owner, fd.TypeRef.Name)
			continue
		}

		if call := fd.ProjectedFunction; call != nil {
			if fd.ProjectedFrom != "" {
				r.errorf(td.Owner, "%s: @column carries either a source column or a function, not both", owner)
			}
			checkProjectionFunction(td, owner+" @column", call.Function, call.Args, scope, r)
			continue
		}
		ref := fd.ProjectionSource()
		alias, table, source, problem := scope.resolve(ref)
		if problem != "" {
			r.errorf(td.Owner, "%s: @column %q: %s", owner, ref, problem)
			continue
		}
		if source.JsonField || source.HasMany || source.ManyToMany || source.TypeRef.IsMap {
			r.errorf(td.Owner, "%s: %s.%s is not a scalar column of %s", owner, alias, source.Name, table.Name)
			continue
		}

		// A relation field stores the target's key; the projection exposes
		// that key's type, never the object.
		expected := source.TypeRef.Name
		if key := relationKeyType(schema, source); key != "" {
			expected = key
		}
		if fd.TypeRef.Name != expected || fd.TypeRef.IsArray != source.TypeRef.IsArray {
			r.errorf(td.Owner, "%s: declared as %s but %s.%s is %s; a projection column keeps its source column's type",
				owner, typeRefString(fd.TypeRef), alias, source.Name, typeRefString(ir.TypeRef{Name: expected, IsArray: source.TypeRef.IsArray}))
			continue
		}

		if fd.Required && (!source.Required || scope.nullable[alias]) {
			reason := "is nullable"
			if scope.nullable[alias] {
				reason = "comes through a left join"
			}
			r.errorf(td.Owner, "%s: must be Nullable because %s.%s %s", owner, alias, source.Name, reason)
		}
	}
}

func isObjectType(schema *ir.Schema, name string) bool {
	t := schema.Types[name]
	return t != nil && (t.Role == ir.RoleEmbeddedStruct || t.Role == ir.RoleAPIView || t.Role == ir.RoleAPIInput || t.Role == ir.RoleTrait)
}

func checkProjectionPredicate(td *ir.TypeDef, label string, pred *ir.ProjectionPredicate, scope *projectionScope, r *Result) {
	switch {
	case pred.IsLiteral():
		checkProjectionLiteral(td, label, pred, scope, r)
	case pred.Function != "":
		checkProjectionFunction(td, "@projection "+label, pred.Function, pred.Args, scope, r)
		if pred.Column != "" || pred.Setting != "" || pred.Optional || len(pred.AnyOf) > 0 || len(pred.Args) == 0 || len(pred.RequiredSettings) == 0 {
			r.errorf(td.Owner, "%s: @projection %s function rule takes function, args and requiredSettings only", td.Name, label)
		}
		for _, setting := range pred.RequiredSettings {
			if !settingNamePattern.MatchString(setting) {
				r.errorf(td.Owner, "%s: @projection %s required setting must be a dotted custom Postgres setting name, got %q", td.Name, label, setting)
			}
		}
	case len(pred.AnyOf) > 0:
		if len(pred.Args) > 0 || len(pred.RequiredSettings) > 0 {
			r.errorf(td.Owner, "%s: @projection %s args and requiredSettings require function", td.Name, label)
		}
		if pred.Column != "" || pred.Setting != "" || pred.Optional {
			r.errorf(td.Owner, "%s: @projection %s carries either anyOf or a column and setting, not both", td.Name, label)
		}
		if len(pred.AnyOf) < 2 {
			r.errorf(td.Owner, "%s: @projection %s anyOf needs at least two alternatives", td.Name, label)
		}
		for i, alt := range pred.AnyOf {
			if alt.When != nil || len(alt.AnyOf) > 0 || alt.Function != "" || alt.IsLiteral() {
				r.errorf(td.Owner, "%s: @projection %s anyOf[%d] carries only column, setting and optional", td.Name, label, i)
				continue
			}
			checkProjectionBinding(td, fmt.Sprintf("%s anyOf[%d]", label, i), alt, scope, r)
		}
	default:
		if len(pred.Args) > 0 || len(pred.RequiredSettings) > 0 {
			r.errorf(td.Owner, "%s: @projection %s args and requiredSettings require function", td.Name, label)
		}
		checkProjectionBinding(td, label, pred, scope, r)
	}
	if pred.When != nil {
		if _, _, _, problem := scope.resolve(pred.When.Column); problem != "" {
			r.errorf(td.Owner, "%s: @projection %s when.column %q: %s", td.Name, label, pred.When.Column, problem)
		}
	}
}

// checkProjectionFunction checks the restricted call language computed
// columns and function rules share: a schema.function name and alias.field
// arguments. The function's return type and nullability are the migration
// owner's contract; the declared column type states it.
func checkProjectionFunction(td *ir.TypeDef, label, name string, args []string, scope *projectionScope, r *Result) {
	parts := strings.Split(name, ".")
	if len(parts) != 2 || !projectionIdentifierPattern.MatchString(parts[0]) || !projectionIdentifierPattern.MatchString(parts[1]) {
		r.errorf(td.Owner, "%s: %s function must name schema.function, got %q", td.Name, label, name)
	}
	if len(args) == 0 {
		r.errorf(td.Owner, "%s: %s function requires non-empty alias.field args", td.Name, label)
	}
	for _, arg := range args {
		if _, _, _, problem := scope.resolve(arg); problem != "" {
			r.errorf(td.Owner, "%s: %s argument %q: %s", td.Name, label, arg, problem)
		}
	}
}

// checkProjectionLiteral checks a literal rule: exactly one of isNull,
// notNull and equals; a column that resolves to a scalar-like field; and,
// for equals, a literal whose type agrees with the column (an enum column
// takes one of its members, a boolean column a boolean, a numeric column a
// number, a text-like column a string).
func checkProjectionLiteral(td *ir.TypeDef, label string, pred *ir.ProjectionPredicate, scope *projectionScope, r *Result) {
	forms := 0
	for _, set := range []bool{pred.IsNull, pred.NotNull, pred.Equals != nil} {
		if set {
			forms++
		}
	}
	if forms != 1 {
		r.errorf(td.Owner, "%s: @projection %s carries exactly one of isNull, notNull and equals", td.Name, label)
	}
	if pred.Setting != "" || pred.Optional || len(pred.AnyOf) > 0 || pred.Function != "" || len(pred.Args) > 0 || len(pred.RequiredSettings) > 0 {
		r.errorf(td.Owner, "%s: @projection %s literal rule carries only column and when; it reads no setting", td.Name, label)
	}
	_, _, field, problem := scope.resolve(pred.Column)
	if problem != "" {
		r.errorf(td.Owner, "%s: @projection %s column %q: %s", td.Name, label, pred.Column, problem)
		return
	}
	if field.TypeRef.IsArray || field.TypeRef.IsMap || field.JsonField || field.HasMany || field.ManyToMany {
		r.errorf(td.Owner, "%s: @projection %s column %q must be a scalar, enum, boolean or relation column", td.Name, label, pred.Column)
		return
	}
	lit := pred.Equals
	if lit == nil {
		return
	}
	if lit.Set() != 1 {
		r.errorf(td.Owner, "%s: @projection %s equals must be exactly one string, number or boolean literal", td.Name, label)
		return
	}
	want := scope.literalKind(field)
	if want == "" {
		r.errorf(td.Owner, "%s: @projection %s column %q has type %s, which no literal can equal", td.Name, label, pred.Column, field.TypeRef.Name)
		return
	}
	if lit.Kind() != want {
		r.errorf(td.Owner, "%s: @projection %s equals must be a %s literal to match column %q (%s)", td.Name, label, want, pred.Column, field.TypeRef.Name)
		return
	}
	if enum := scope.schema.Enums[field.TypeRef.Name]; enum != nil && !enumHasSerializedValue(enum, *lit.String) {
		r.errorf(td.Owner, "%s: @projection %s equals %q is not a member of enum %s", td.Name, label, *lit.String, field.TypeRef.Name)
	}
}

// literalKind returns the literal kind ("string", "number", "boolean") a
// field's type compares against, or "" when no literal can.
func (s *projectionScope) literalKind(field *ir.FieldDef) string {
	name := field.TypeRef.Name
	if key := relationKeyType(s.schema, field); key != "" {
		name = key
	} else if field.Relation != nil {
		// A relation to a table outside this schema stores its key; keys
		// compare as strings.
		return "string"
	}
	if s.schema.Enums[name] != nil {
		return "string"
	}
	if scalar := s.schema.Scalars[name]; scalar != nil {
		switch scalar.LanguagePrimitive {
		case ir.LanguageString:
			return "string"
		case ir.LanguageNumber:
			return "number"
		case ir.LanguageBoolean:
			return "boolean"
		}
		return ""
	}
	switch strings.ToLower(name) {
	case "string":
		return "string"
	case "boolean", "bool":
		return "boolean"
	case "number", "int", "int32", "int64", "float", "float32", "float64", "integer", "double":
		return "number"
	}
	// A named type that is not declared here is an enum from a dependency
	// schema (the loader does not merge those); it compares as its wire
	// string, and the generator, which sees the dependency, checks the
	// membership.
	if s.schema.Types[name] == nil {
		return "string"
	}
	return ""
}

// enumHasSerializedValue reports whether value is the serialized form of one
// of the enum's members.
func enumHasSerializedValue(enum *ir.EnumDef, value string) bool {
	for _, member := range enum.Values {
		serialized := member.SerializedAs
		if serialized == "" {
			serialized = member.Name
		}
		if serialized == value {
			return true
		}
	}
	return false
}

// checkProjectionBinding checks one column = setting binding.
func checkProjectionBinding(td *ir.TypeDef, label string, pred *ir.ProjectionPredicate, scope *projectionScope, r *Result) {
	if !settingNamePattern.MatchString(pred.Setting) {
		r.errorf(td.Owner, "%s: @projection %s setting must be a dotted custom Postgres setting name such as app.account_id, got %q", td.Name, label, pred.Setting)
	}
	if _, _, _, problem := scope.resolve(pred.Column); problem != "" {
		r.errorf(td.Owner, "%s: @projection %s column %q: %s", td.Name, label, pred.Column, problem)
	}
}

// checkProjectionCollapse checks the DISTINCT ON declaration: every key and
// order column resolves, and each order term is either a rank list or a
// direction and nulls pair.
func checkProjectionCollapse(td *ir.TypeDef, def *ir.ProjectionDef, scope *projectionScope, r *Result) {
	collapse := def.Collapse
	if collapse == nil {
		return
	}
	if len(collapse.By) == 0 {
		r.errorf(td.Owner, "%s: @projection collapse.by must name at least one column", td.Name)
	}
	for i, ref := range collapse.By {
		if _, _, _, problem := scope.resolve(ref); problem != "" {
			r.errorf(td.Owner, "%s: @projection collapse.by[%d] %q: %s", td.Name, i, ref, problem)
		}
	}
	for i, term := range collapse.Order {
		label := fmt.Sprintf("collapse.order[%d]", i)
		if _, _, _, problem := scope.resolve(term.Column); problem != "" {
			r.errorf(td.Owner, "%s: @projection %s column %q: %s", td.Name, label, term.Column, problem)
		}
		if len(term.Rank) > 0 && (term.Direction != "" || term.Nulls != "") {
			r.errorf(td.Owner, "%s: @projection %s uses either rank or direction/nulls, not both", td.Name, label)
		}
		if term.Direction != "" && term.Direction != "asc" && term.Direction != "desc" {
			r.errorf(td.Owner, "%s: @projection %s direction must be \"asc\" or \"desc\", got %q", td.Name, label, term.Direction)
		}
		if term.Nulls != "" && term.Nulls != "first" && term.Nulls != "last" {
			r.errorf(td.Owner, "%s: @projection %s nulls must be \"first\" or \"last\", got %q", td.Name, label, term.Nulls)
		}
		seen := map[string]bool{}
		for _, value := range term.Rank {
			if seen[value] {
				r.errorf(td.Owner, "%s: @projection %s rank lists %q twice", td.Name, label, value)
			}
			seen[value] = true
		}
	}
}
