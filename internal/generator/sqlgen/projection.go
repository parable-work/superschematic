package sqlgen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// ProjectionView is one generated @projection: the Postgres view a reader
// queries, the migration that creates it, and the Arrow schema and docs
// file derived from the same declaration. Everything here is computed from
// the IR; nothing about the view is written by hand.
type ProjectionView struct {
	// TypeName is the declaring schema class; Owner its source file.
	TypeName string
	Owner    string
	Doc      string

	// Pool and Name address the view as pool.name; it is created as
	// "pool"."name", so the name a reader writes and the stored name match.
	Pool          string
	Name          string
	QuotedPool    string
	QuotedName    string
	Relation      string
	QualifiedView string

	// Migration is the version stamp; the file names derive from it, so a
	// changed projection is always a new migration.
	Migration    string
	UpFileName   string
	DownFileName string

	// ViewOwner is the Postgres role the migration creates the view as
	// (SET ROLE around the DDL); empty creates it as the migration runner.
	ViewOwner       string
	QuotedViewOwner string

	Columns []ProjectionColumn

	QuotedBaseTable string
	QuotedBaseAlias string
	Joins           []ProjectionJoinClause

	// Predicates are the WHERE conjuncts as SQL fragments, in declaration
	// order.
	Predicates []string

	// Settings lists every current_setting name the view reads, in
	// first-use order, so a reader knows what to SET before a scan.
	Settings []string

	// OptionalSettings is the subset of Settings a reader may leave unset
	// (read through NULLIF(current_setting(name, true), '')).
	OptionalSettings []string

	// DistinctOn and OrderBy render the collapse: DISTINCT ON (DistinctOn...)
	// with ORDER BY OrderBy..., led by the DistinctOn expressions as
	// Postgres requires. Both empty when the view serves every row.
	DistinctOn []string
	OrderBy    []string
	collapseBy []string
}

// ProjectionColumn is one column of a view.
type ProjectionColumn struct {
	Name       string
	QuotedName string
	// Expr is the resolved source column or function call the view selects.
	Expr string
	// Source is the declared alias.field or schema.function(alias.field...).
	Source   string
	Type     string
	Nullable bool
	// SelfNamed is true when the column keeps its source column's name, so
	// the view selects it without an alias.
	SelfNamed bool
	// Scalar is the canonical scalar name ("" for enums and primitives);
	// Enum is the enum name when the column is enum-typed.
	Scalar string
	Enum   string
	Doc    string
	arrow  any
	// enumDef is the resolved enum (local or from a dependency schema), so
	// the Arrow metadata can list its values.
	enumDef *ir.EnumDef
}

// ProjectionJoinClause is one rendered JOIN.
type ProjectionJoinClause struct {
	Kind        string
	QuotedTable string
	QuotedAlias string
	On          string
}

// lookupEnum finds an enum by name in the schema or any dependency schema:
// an enum a DB schema references often lives in a shared General schema,
// which the loader does not merge into schema.Enums.
func lookupEnum(schema *ir.Schema, deps map[string]*ir.Schema, name string) *ir.EnumDef {
	if enum := schema.Enums[name]; enum != nil {
		return enum
	}
	for _, dep := range deps {
		if dep == nil {
			continue
		}
		if enum := dep.Enums[name]; enum != nil {
			return enum
		}
	}
	return nil
}

// enumSerializedValues lists the wire values of an enum's members.
func enumSerializedValues(enum *ir.EnumDef) []string {
	values := make([]string, 0, len(enum.Values))
	for _, v := range enum.Values {
		if v.SerializedAs != "" {
			values = append(values, v.SerializedAs)
		} else {
			values = append(values, v.Name)
		}
	}
	return values
}

// buildProjections renders every projection against the converted tables.
// Verification has proven the declarations resolve; this pass refuses
// anything it still cannot render (a column it cannot find, a setting it
// cannot cast, an unsafe name) rather than emitting a view that is wrong,
// because a data-form IR can reach the generator without a load.
func buildProjections(schema *ir.Schema, deps map[string]*ir.Schema, tableMap map[string]*Table, scalarMapping map[string]string) ([]ProjectionView, error) {
	var views []ProjectionView
	for _, td := range schema.Projections() {
		view, err := buildProjection(schema, deps, td, tableMap, scalarMapping)
		if err != nil {
			return nil, fmt.Errorf("projection %s: %w", td.Name, err)
		}
		views = append(views, view)
	}
	return views, nil
}

type projectionAliases struct {
	tables   map[string]*Table
	nullable map[string]bool
}

// column resolves alias.field to the table column it names.
func (a *projectionAliases) column(ref string) (alias string, col *Column, err error) {
	alias, field, ok := strings.Cut(ref, ".")
	if !ok {
		return "", nil, fmt.Errorf("%q is not an alias.field reference", ref)
	}
	table := a.tables[alias]
	if table == nil {
		return "", nil, fmt.Errorf("%q names unknown alias %q", ref, alias)
	}
	col = findIndexColumn(table.Columns, field)
	if col == nil {
		return "", nil, fmt.Errorf("%q: table %s has no column for field %q", ref, table.Name, field)
	}
	return alias, col, nil
}

func (a *projectionAliases) expr(ref string) (string, *Column, bool, error) {
	alias, col, err := a.column(ref)
	if err != nil {
		return "", nil, false, err
	}
	return sqlutil.QuoteIdentifier(alias) + "." + col.QuotedName, col, a.nullable[alias], nil
}

var (
	projectionIdentifierPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	projectionFunctionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)
	projectionSettingPattern      = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	migrationStampPattern         = regexp.MustCompile(`^[0-9]{14}$`)
)

// functionExpr renders a call for a computed column or a function rule.
// Only a schema.function name and alias.field arguments are accepted, so no
// SQL expression or literal can enter the view.
func (a *projectionAliases) functionExpr(name string, refs []string) (string, error) {
	if !projectionFunctionNamePattern.MatchString(name) || len(refs) == 0 {
		return "", fmt.Errorf("a function needs a schema.function name and alias.field args")
	}
	args := make([]string, 0, len(refs))
	for _, ref := range refs {
		expr, _, _, err := a.expr(ref)
		if err != nil {
			return "", err
		}
		args = append(args, expr)
	}
	schemaName, function, _ := strings.Cut(name, ".")
	return fmt.Sprintf(`"%s"."%s"(%s)`, schemaName, function, strings.Join(args, ", ")), nil
}

func buildProjection(schema *ir.Schema, deps map[string]*ir.Schema, td *ir.TypeDef, tableMap map[string]*Table, scalarMapping map[string]string) (ProjectionView, error) {
	def := td.Projection
	if def == nil {
		return ProjectionView{}, fmt.Errorf("no @projection declaration")
	}
	for _, part := range []struct{ key, value string }{{"pool", def.Pool}, {"name", def.Name}} {
		if !projectionIdentifierPattern.MatchString(part.value) {
			return ProjectionView{}, fmt.Errorf("%s %q is not a snake_case SQL identifier", part.key, part.value)
		}
	}
	if !migrationStampPattern.MatchString(def.Migration) {
		return ProjectionView{}, fmt.Errorf("migration %q is not a 14-digit version stamp", def.Migration)
	}
	base := tableMap[def.Source]
	if base == nil {
		return ProjectionView{}, fmt.Errorf("source %q is not a generated table", def.Source)
	}

	aliases := &projectionAliases{
		tables:   map[string]*Table{ir.ProjectionBaseAlias: base},
		nullable: map[string]bool{},
	}
	view := ProjectionView{
		TypeName:        td.Name,
		Owner:           td.Owner,
		Doc:             codegen.DocText(td.Description, td.Comment),
		Pool:            def.Pool,
		Name:            def.Name,
		QuotedPool:      sqlutil.QuoteIdentifier(def.Pool),
		QuotedName:      sqlutil.QuoteIdentifier(def.Name),
		Relation:        def.Pool + "." + def.Name,
		Migration:       def.Migration,
		UpFileName:      fmt.Sprintf("%s_%s_%s_projection.up.sql", def.Migration, def.Pool, def.Name),
		DownFileName:    fmt.Sprintf("%s_%s_%s_projection.down.sql", def.Migration, def.Pool, def.Name),
		QuotedBaseTable: base.QuotedName,
		QuotedBaseAlias: sqlutil.QuoteIdentifier(ir.ProjectionBaseAlias),
	}
	view.QualifiedView = view.QuotedPool + "." + view.QuotedName

	for _, j := range def.Joins {
		if !projectionIdentifierPattern.MatchString(j.Alias) || j.Alias == ir.ProjectionBaseAlias || aliases.tables[j.Alias] != nil {
			return view, fmt.Errorf("join alias %q is not a free snake_case identifier", j.Alias)
		}
		table := tableMap[j.Type]
		if table == nil {
			return view, fmt.Errorf("join %s: %q is not a generated table", j.Alias, j.Type)
		}
		aliases.tables[j.Alias] = table
		aliases.nullable[j.Alias] = j.Kind == "left"
		var conds []string
		for _, key := range j.On {
			left, _, _, err := aliases.expr(key.Left)
			if err != nil {
				return view, fmt.Errorf("join %s: %w", j.Alias, err)
			}
			right, _, _, err := aliases.expr(key.Right)
			if err != nil {
				return view, fmt.Errorf("join %s: %w", j.Alias, err)
			}
			// The operand on the table earlier in FROM order leads (sqlfluff
			// ST09), whichever way the declaration wrote the pair.
			if leftAlias, _, _ := strings.Cut(key.Left, "."); leftAlias == j.Alias {
				left, right = right, left
			}
			conds = append(conds, left+" = "+right)
		}
		if len(conds) == 0 {
			return view, fmt.Errorf("join %s has no condition", j.Alias)
		}
		kind := "INNER"
		switch j.Kind {
		case "", "inner":
		case "left":
			kind = "LEFT"
		default:
			return view, fmt.Errorf("join %s: kind %q is not inner or left", j.Alias, j.Kind)
		}
		view.Joins = append(view.Joins, ProjectionJoinClause{
			Kind:        kind,
			QuotedTable: table.QuotedName,
			QuotedAlias: sqlutil.QuoteIdentifier(j.Alias),
			On:          strings.Join(conds, " AND "),
		})
	}

	seen := map[string]bool{}
	for _, fd := range td.Fields {
		name := codegen.ToSnakeCase(fd.Name)
		if seen[name] {
			return view, fmt.Errorf("column %s: two fields map to the same column name", name)
		}
		seen[name] = true
		source := fd.ProjectionSource()
		var expr string
		selfNamed, nullable := false, !fd.Required
		if call := fd.ProjectedFunction; call != nil {
			if fd.ProjectedFrom != "" {
				return view, fmt.Errorf("column %s: carries both a source and a function", name)
			}
			var err error
			expr, err = aliases.functionExpr(call.Function, call.Args)
			if err != nil {
				return view, fmt.Errorf("column %s: %w", name, err)
			}
			source = call.Function + "(" + strings.Join(call.Args, ", ") + ")"
		} else {
			resolved, col, viaLeftJoin, err := aliases.expr(source)
			if err != nil {
				return view, fmt.Errorf("column %s: %w", name, err)
			}
			expr, selfNamed = resolved, col.Name == name
			nullable = nullable || col.Nullable || viaLeftJoin
		}
		colType := columnType(fd, scalarMapping)
		arrow, err := arrowDataType(colType)
		if err != nil {
			return view, fmt.Errorf("column %s: %w", name, err)
		}
		column := ProjectionColumn{
			Name:       name,
			QuotedName: sqlutil.QuoteIdentifier(name),
			Expr:       expr,
			SelfNamed:  selfNamed,
			Source:     source,
			Type:       colType,
			Nullable:   nullable,
			Doc:        codegen.DocText(fd.Description, fd.Comment),
			arrow:      arrow,
		}
		if _, isScalar := schema.Scalars[fd.TypeRef.Name]; isScalar {
			column.Scalar = fd.TypeRef.Name
		} else if enum := lookupEnum(schema, deps, fd.TypeRef.Name); enum != nil {
			column.Enum = fd.TypeRef.Name
			column.enumDef = enum
		}
		view.Columns = append(view.Columns, column)
	}
	if len(view.Columns) == 0 {
		return view, fmt.Errorf("a projection must expose at least one column")
	}

	settings := map[string]bool{}
	optional := map[string]bool{}
	required := map[string]bool{}
	addSetting := func(name string, isOptional bool) {
		if !settings[name] {
			settings[name] = true
			view.Settings = append(view.Settings, name)
		}
		if !isOptional {
			required[name] = true
			if optional[name] {
				delete(optional, name)
				view.OptionalSettings = slices.DeleteFunc(view.OptionalSettings, func(s string) bool { return s == name })
			}
		} else if !required[name] && !optional[name] {
			optional[name] = true
			view.OptionalSettings = append(view.OptionalSettings, name)
		}
	}

	// renderBinding renders one column = setting comparison. An optional
	// setting reads through NULLIF(current_setting(name, true), ''), so an
	// unset or empty value compares as NULL (no row matches) instead of
	// raising; that is what lets a rule offer bindings of which the caller
	// supplies one.
	renderBinding := func(pred *ir.ProjectionPredicate) (string, error) {
		if !projectionSettingPattern.MatchString(pred.Setting) {
			return "", fmt.Errorf("%s: setting %q is not a dotted custom setting name", pred.Column, pred.Setting)
		}
		expr, col, _, err := aliases.expr(pred.Column)
		if err != nil {
			return "", err
		}
		cast, err := settingCast(col.Type)
		if err != nil {
			return "", fmt.Errorf("%s: %w", pred.Column, err)
		}
		addSetting(pred.Setting, pred.Optional)
		if pred.Optional {
			return fmt.Sprintf("%s = NULLIF(current_setting(%s, true), '')%s", expr, sqlLiteral(pred.Setting), cast), nil
		}
		return fmt.Sprintf("%s = current_setting(%s)%s", expr, sqlLiteral(pred.Setting), cast), nil
	}
	renderPredicate := func(pred *ir.ProjectionPredicate) (string, error) {
		var sql string
		switch {
		case pred.IsLiteral():
			if pred.Setting != "" || pred.Optional || len(pred.AnyOf) > 0 || pred.Function != "" || len(pred.Args) > 0 || len(pred.RequiredSettings) > 0 {
				return "", fmt.Errorf("a literal rule reads no setting")
			}
			expr, _, _, err := aliases.expr(pred.Column)
			if err != nil {
				return "", err
			}
			switch {
			case pred.IsNull && !pred.NotNull && pred.Equals == nil:
				sql = expr + " IS null"
			case pred.NotNull && !pred.IsNull && pred.Equals == nil:
				sql = expr + " IS NOT null"
			case pred.Equals != nil && !pred.IsNull && !pred.NotNull:
				lit, err := literalSQL(pred.Equals)
				if err != nil {
					return "", fmt.Errorf("%s: %w", pred.Column, err)
				}
				// Enum membership is checked here as well as in verify,
				// because an enum from a dependency schema is only visible
				// to the generator.
				if field := projectionFieldByColumn(schema, aliases, pred.Column); field != nil {
					if enum := lookupEnum(schema, deps, field.TypeRef.Name); enum != nil && !slices.Contains(enumSerializedValues(enum), literalString(pred.Equals)) {
						return "", fmt.Errorf("%s: %q is not a member of enum %s", pred.Column, literalString(pred.Equals), field.TypeRef.Name)
					}
				}
				sql = expr + " = " + lit
			default:
				return "", fmt.Errorf("a literal rule carries exactly one of isNull, notNull and equals")
			}
		case pred.Function != "":
			if len(pred.RequiredSettings) == 0 || pred.Column != "" || pred.Setting != "" || pred.Optional || len(pred.AnyOf) > 0 {
				return "", fmt.Errorf("a function rule takes function, args and requiredSettings only")
			}
			call, err := aliases.functionExpr(pred.Function, pred.Args)
			if err != nil {
				return "", err
			}
			guards := []string{}
			for _, setting := range pred.RequiredSettings {
				if !projectionSettingPattern.MatchString(setting) {
					return "", fmt.Errorf("required setting %q is not a dotted custom setting name", setting)
				}
				addSetting(setting, false)
				guards = append(guards, fmt.Sprintf("NULLIF(current_setting(%s), '') IS NOT null", sqlLiteral(setting)))
			}
			guards = append(guards, "("+call+") IS true")
			sql = "(" + strings.Join(guards, " AND ") + ")"
		case len(pred.AnyOf) > 0:
			if len(pred.AnyOf) < 2 {
				return "", fmt.Errorf("anyOf needs at least two alternatives")
			}
			alternatives := make([]string, 0, len(pred.AnyOf))
			for _, alt := range pred.AnyOf {
				if !alt.IsBinding() || alt.When != nil {
					return "", fmt.Errorf("an anyOf alternative carries only column, setting and optional")
				}
				rendered, err := renderBinding(alt)
				if err != nil {
					return "", err
				}
				alternatives = append(alternatives, rendered)
			}
			sql = "(" + strings.Join(alternatives, " OR ") + ")"
		default:
			rendered, err := renderBinding(pred)
			if err != nil {
				return "", err
			}
			sql = rendered
		}
		if pred.When != nil {
			guard, _, _, err := aliases.expr(pred.When.Column)
			if err != nil {
				return "", err
			}
			sql = fmt.Sprintf("(%s IS DISTINCT FROM %s OR %s)", guard, sqlLiteral(pred.When.Equals), sql)
		}
		return sql, nil
	}
	for i, pred := range def.Predicates {
		sql, err := renderPredicate(pred)
		if err != nil {
			return view, fmt.Errorf("where[%d]: %w", i, err)
		}
		view.Predicates = append(view.Predicates, sql)
	}

	if collapse := def.Collapse; collapse != nil {
		if len(collapse.By) == 0 {
			return view, fmt.Errorf("collapse.by must name at least one column")
		}
		for _, ref := range collapse.By {
			expr, _, _, err := aliases.expr(ref)
			if err != nil {
				return view, fmt.Errorf("collapse.by: %w", err)
			}
			view.DistinctOn = append(view.DistinctOn, expr)
			view.collapseBy = append(view.collapseBy, ref)
		}
		// DISTINCT ON expressions must lead the ORDER BY; the declared terms
		// then decide which row of each key survives.
		for _, expr := range view.DistinctOn {
			view.OrderBy = append(view.OrderBy, expr+" ASC")
		}
		for i, term := range collapse.Order {
			rendered, err := renderOrderTerm(aliases, term)
			if err != nil {
				return view, fmt.Errorf("collapse.order[%d]: %w", i, err)
			}
			view.OrderBy = append(view.OrderBy, rendered)
		}
	}
	return view, nil
}

// renderOrderTerm renders one collapse order term: a rank CASE that puts
// the listed literals first in order and everything else last, or a plain
// direction with an optional NULLS placement.
func renderOrderTerm(aliases *projectionAliases, term *ir.ProjectionOrder) (string, error) {
	expr, _, _, err := aliases.expr(term.Column)
	if err != nil {
		return "", err
	}
	if len(term.Rank) > 0 {
		if term.Direction != "" || term.Nulls != "" {
			return "", fmt.Errorf("%s: rank and direction/nulls are exclusive", term.Column)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "CASE %s", expr)
		for i, value := range term.Rank {
			fmt.Fprintf(&b, " WHEN %s THEN %d", sqlLiteral(value), i)
		}
		fmt.Fprintf(&b, " ELSE %d END ASC", len(term.Rank))
		return b.String(), nil
	}
	out := expr
	switch term.Direction {
	case "", "asc":
		out += " ASC"
	case "desc":
		out += " DESC"
	default:
		return "", fmt.Errorf("%s: direction must be asc or desc, got %q", term.Column, term.Direction)
	}
	switch term.Nulls {
	case "":
	case "first":
		out += " NULLS FIRST"
	case "last":
		out += " NULLS LAST"
	default:
		return "", fmt.Errorf("%s: nulls must be first or last, got %q", term.Column, term.Nulls)
	}
	return out, nil
}

// settingCast returns the cast that turns current_setting's text into the
// column's type. Text-like columns compare as text; any other type cannot
// be bound to a setting and is refused.
func settingCast(sqlType string) (string, error) {
	upper := strings.ToUpper(sqlType)
	switch {
	case upper == "UUID":
		return "::uuid", nil
	case upper == "BIGINT":
		return "::bigint", nil
	case upper == "INTEGER" || upper == "INT":
		return "::integer", nil
	case upper == "BOOLEAN":
		return "::boolean", nil
	case upper == "TIMESTAMPTZ":
		return "::timestamptz", nil
	case upper == "DATE":
		return "::date", nil
	case upper == "TEXT" || upper == "CITEXT" || strings.HasPrefix(upper, "VARCHAR"):
		return "", nil
	}
	return "", fmt.Errorf("a %s column cannot be bound to a setting", sqlType)
}

// sqlLiteral renders a single-quoted SQL string literal.
func sqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// literalString returns the string form of a literal ("" for non-strings).
func literalString(lit *ir.ProjectionLiteral) string {
	if lit != nil && lit.String != nil {
		return *lit.String
	}
	return ""
}

// projectionFieldByColumn finds the schema field behind an alias.field
// reference so the generator can consult its declared type.
func projectionFieldByColumn(schema *ir.Schema, aliases *projectionAliases, ref string) *ir.FieldDef {
	alias, fieldName, found := strings.Cut(ref, ".")
	if !found {
		return nil
	}
	table := aliases.tables[alias]
	if table == nil {
		return nil
	}
	td := schema.Types[table.OriginalName]
	if td == nil {
		return nil
	}
	for _, fd := range td.Fields {
		if fd.Name == fieldName {
			return fd
		}
	}
	return nil
}

// literalSQL renders a typed equals literal: quoted text, a bare number, or
// a lowercase boolean.
func literalSQL(lit *ir.ProjectionLiteral) (string, error) {
	if lit == nil || lit.Set() != 1 {
		return "", fmt.Errorf("equals must be exactly one string, number or boolean literal")
	}
	switch {
	case lit.String != nil:
		return sqlLiteral(*lit.String), nil
	case lit.Bool != nil:
		if *lit.Bool {
			return "true", nil
		}
		return "false", nil
	default:
		return strconv.FormatFloat(*lit.Number, 'f', -1, 64), nil
	}
}

// arrowDataType maps a generated SQL column type to the serde form of an
// Arrow DataType, the shape arrow-rs's arrow_schema::Schema deserializes.
// Strings, UUIDs and JSON travel as Utf8. A type with no mapping is refused
// rather than guessed.
func arrowDataType(sqlType string) (any, error) {
	isArray := strings.HasSuffix(sqlType, "[]")
	base := strings.ToUpper(strings.TrimSuffix(sqlType, "[]"))
	var inner any
	switch {
	case base == "UUID", base == "TEXT", base == "CITEXT", base == "JSONB", base == "JSON",
		base == "LTREE", strings.HasPrefix(base, "VARCHAR"), strings.HasPrefix(base, "CHAR"):
		inner = "Utf8"
	case base == "BIGINT":
		inner = "Int64"
	case base == "INTEGER", base == "INT":
		inner = "Int32"
	case base == "SMALLINT":
		inner = "Int16"
	case base == "DOUBLE PRECISION", base == "FLOAT8", base == "NUMERIC", base == "DECIMAL":
		inner = "Float64"
	case base == "REAL", base == "FLOAT4":
		inner = "Float32"
	case base == "BOOLEAN":
		inner = "Boolean"
	case base == "TIMESTAMPTZ":
		inner = map[string]any{"Timestamp": []any{"Microsecond", "UTC"}}
	case base == "TIMESTAMP":
		inner = map[string]any{"Timestamp": []any{"Microsecond", nil}}
	case base == "DATE":
		inner = "Date32"
	case base == "TIME":
		inner = map[string]any{"Time64": "Microsecond"}
	case base == "BYTEA":
		inner = "Binary"
	default:
		return nil, fmt.Errorf("no Arrow type for SQL type %s", sqlType)
	}
	if !isArray {
		return inner, nil
	}
	return map[string]any{"List": arrowField("item", inner, true, map[string]string{})}, nil
}

// arrowField renders one serde Field.
func arrowField(name string, dataType any, nullable bool, metadata map[string]string) map[string]any {
	return map[string]any{
		"name":            name,
		"data_type":       dataType,
		"nullable":        nullable,
		"dict_id":         0,
		"dict_is_ordered": false,
		"metadata":        metadata,
	}
}

// MetadataKeys are the Arrow metadata keys a projection's schema carries.
// Every key is Naming.MetadataKeyPrefix followed by a fixed suffix, so a
// deployment whose readers expect another namespace sets the prefix, not
// the keys.
type MetadataKeys struct {
	// Field metadata: the column's scalar identity, its enum, and the
	// alias.field (or function call) it reads.
	ScalarCanonicalName, ScalarFlatName, ScalarPrimitive, ScalarSQLType string
	ScalarJSONSchema, ScalarFormat, ScalarMaxLength, ScalarMinLength    string
	ScalarPattern, EnumName, EnumValues, ProjectionSource               string

	// Schema metadata: the view's address, its declaration, and the
	// settings a reader sets before a scan.
	ProjectionPool, ProjectionName, ProjectionRelation, ProjectionView       string
	ProjectionSchema, ProjectionType, ProjectionMigration, ProjectionSetting string
	ProjectionOptionalSettings, ProjectionRows                               string
}

// NewMetadataKeys returns the key set under prefix ("superschematic." for
// the defaults).
func NewMetadataKeys(prefix string) MetadataKeys {
	return MetadataKeys{
		ScalarCanonicalName:        prefix + "scalar.canonical_name",
		ScalarFlatName:             prefix + "scalar.flat_name",
		ScalarPrimitive:            prefix + "scalar.primitive",
		ScalarSQLType:              prefix + "scalar.sql_type",
		ScalarJSONSchema:           prefix + "scalar.json_schema_type",
		ScalarFormat:               prefix + "scalar.format",
		ScalarMaxLength:            prefix + "scalar.max_length",
		ScalarMinLength:            prefix + "scalar.min_length",
		ScalarPattern:              prefix + "scalar.pattern",
		EnumName:                   prefix + "enum.name",
		EnumValues:                 prefix + "enum.values",
		ProjectionSource:           prefix + "projection.source",
		ProjectionPool:             prefix + "projection.pool",
		ProjectionName:             prefix + "projection.name",
		ProjectionRelation:         prefix + "projection.relation",
		ProjectionView:             prefix + "projection.view",
		ProjectionSchema:           prefix + "projection.schema",
		ProjectionType:             prefix + "projection.type",
		ProjectionMigration:        prefix + "projection.migration",
		ProjectionSetting:          prefix + "projection.settings",
		ProjectionOptionalSettings: prefix + "projection.optional_settings",
		ProjectionRows:             prefix + "projection.rows",
	}
}

func scalarFieldMetadata(keys MetadataKeys, scalar *ir.ScalarDef) map[string]string {
	metadata := map[string]string{
		keys.ScalarCanonicalName: scalar.Name,
		keys.ScalarFlatName:      strings.ReplaceAll(scalar.Name, ".", "_"),
		keys.ScalarPrimitive:     scalar.Primitive,
		keys.ScalarSQLType:       scalar.TypeMappings["sql"],
	}
	if v := scalar.TypeMappings["json_schema"]; v != "" {
		metadata[keys.ScalarJSONSchema] = v
	}
	if scalar.Format != "" {
		metadata[keys.ScalarFormat] = scalar.Format
	}
	if scalar.MaxLength > 0 {
		metadata[keys.ScalarMaxLength] = fmt.Sprint(scalar.MaxLength)
	}
	if scalar.MinLength > 0 {
		metadata[keys.ScalarMinLength] = fmt.Sprint(scalar.MinLength)
	}
	if scalar.Pattern != "" {
		metadata[keys.ScalarPattern] = scalar.Pattern
	}
	return metadata
}

// ArrowSchemaJSON renders the view's Arrow schema in arrow-rs serde form: a
// reader can register the view with exactly these fields and metadata.
// keyPrefix is Naming.MetadataKeyPrefix.
func ArrowSchemaJSON(schema *ir.Schema, schemaName string, view ProjectionView, keyPrefix string) ([]byte, error) {
	keys := NewMetadataKeys(keyPrefix)
	fields := make([]any, 0, len(view.Columns))
	for _, col := range view.Columns {
		metadata := map[string]string{keys.ProjectionSource: col.Source}
		if col.Scalar != "" {
			if scalar := schema.Scalars[col.Scalar]; scalar != nil {
				for k, v := range scalarFieldMetadata(keys, scalar) {
					metadata[k] = v
				}
			}
		}
		if col.Enum != "" {
			metadata[keys.EnumName] = col.Enum
			enum := col.enumDef
			if enum == nil {
				enum = schema.Enums[col.Enum]
			}
			if enum != nil {
				metadata[keys.EnumValues] = strings.Join(enumSerializedValues(enum), ",")
			}
		}
		fields = append(fields, arrowField(col.Name, col.arrow, col.Nullable, metadata))
	}
	doc := map[string]any{
		"fields": fields,
		"metadata": map[string]string{
			keys.ProjectionPool:      view.Pool,
			keys.ProjectionName:      view.Name,
			keys.ProjectionRelation:  view.Relation,
			keys.ProjectionView:      view.Pool + "." + view.Name,
			keys.ProjectionSchema:    schemaName,
			keys.ProjectionType:      view.TypeName,
			keys.ProjectionMigration: view.Migration,
			keys.ProjectionSetting:   strings.Join(view.Settings, ","),
			// The subset a scan may leave empty; every other setting is
			// required and an unset one makes the view raise.
			keys.ProjectionOptionalSettings: strings.Join(view.OptionalSettings, ","),
			// "one" when the view keeps one row per key (DISTINCT ON), "all"
			// when it serves every admitted row.
			keys.ProjectionRows: projectionRowsMetadata(view),
		},
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// projectionRowsMetadata names the collapse for the Arrow schema metadata.
func projectionRowsMetadata(view ProjectionView) string {
	if len(view.DistinctOn) > 0 {
		return "one"
	}
	return "all"
}

// projectionDocsColumn is one column of the docs file.
type projectionDocsColumn struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Scalar      string `json:"scalar,omitempty"`
	Enum        string `json:"enum,omitempty"`
	SQLType     string `json:"sqlType"`
	ArrowType   string `json:"arrowType"`
	Nullable    bool   `json:"nullable"`
	Description string `json:"description,omitempty"`
}

// projectionDocs is the docs file one view contributes: enough for a
// catalog page row and a per-view column table, without reading SQL.
type projectionDocs struct {
	Pool        string   `json:"pool"`
	Name        string   `json:"name"`
	Relation    string   `json:"relation"`
	Type        string   `json:"type"`
	Schema      string   `json:"schema"`
	Owner       string   `json:"owner"`
	Description string   `json:"description"`
	View        string   `json:"view"`
	Migration   string   `json:"migration"`
	Settings    []string `json:"settings"`
	// OptionalSettings is the subset of Settings a scan may leave unset.
	OptionalSettings []string `json:"optionalSettings"`
	// CollapseBy lists the alias.field columns the view keeps one row per;
	// empty when every row is served.
	CollapseBy []string               `json:"collapseBy"`
	Columns    []projectionDocsColumn `json:"columns"`
}

// DocsJSON renders the view's docs file.
func DocsJSON(schemaName string, view ProjectionView) ([]byte, error) {
	doc := projectionDocs{
		Pool:             view.Pool,
		Name:             view.Name,
		Relation:         view.Relation,
		Type:             view.TypeName,
		Schema:           schemaName,
		Owner:            view.Owner,
		Description:      oneLine(view.Doc),
		View:             view.Pool + "." + view.Name,
		Migration:        view.Migration,
		Settings:         append([]string{}, view.Settings...),
		OptionalSettings: append([]string{}, view.OptionalSettings...),
		CollapseBy:       append([]string{}, view.collapseBy...),
	}
	for _, col := range view.Columns {
		arrow, err := json.Marshal(col.arrow)
		if err != nil {
			return nil, err
		}
		doc.Columns = append(doc.Columns, projectionDocsColumn{
			Name:        col.Name,
			Source:      col.Source,
			Scalar:      col.Scalar,
			Enum:        col.Enum,
			SQLType:     col.Type,
			ArrowType:   strings.Trim(string(arrow), `"`),
			Nullable:    col.Nullable,
			Description: oneLine(col.Doc),
		})
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
