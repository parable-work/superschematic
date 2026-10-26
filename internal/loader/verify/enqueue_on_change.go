package verify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkEnqueueOnChange checks a DB service's @enqueueOnChange declarations
// (D80) in every form: each is on a table of the schema and names a @queue
// class of it; its fields and conditions name fields of the table that
// have a column, which an update compares and a condition reads; and its
// message lines up with the queue's: each mapped field of the message
// takes a field of the table of its type, a required one a value the row
// always has, and every required field the trigger leaves out has its
// column's default.
func checkEnqueueOnChange(schema *ir.Schema, r *Result) {
	scalarTypes := sqlutil.ScalarSQLTypes(schema)
	bases := codegen.BaseTypeNames(schema)
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil || len(td.EnqueueOnChange) == 0 {
			continue
		}
		where := "@enqueueOnChange on " + td.Name
		switch {
		case schema.Kind != ir.SchemaKindDB:
			r.errorf(td.Owner, "%s: only a DB service's table enqueues on a change, and %s is a %s service", where, schema.Name, schema.Kind)
			continue
		case td.Role != ir.RoleDBTable || td.JsonField || td.Queue != nil || bases[td.Name]:
			r.errorf(td.Owner, "%s: %s is not a table of the database; put it on a table, whose rows' writes enqueue", where, td.Name)
			continue
		case codegen.IsEngineType(td):
			r.errorf(td.Owner, "%s: %s composes a behavior, so the engine keeps its instances in tables of its own, which no trigger watches", where, td.Name)
			continue
		}
		columns := enqueueColumns(schema, td, scalarTypes)
		for _, def := range td.EnqueueOnChange {
			checkOneEnqueueOnChange(schema, td, def, columns, scalarTypes, r)
		}
	}
}

// tableColumn is a field of a table and the column it stores.
type tableColumn struct {
	field  *ir.FieldDef // nil for the id sqlgen adds
	column sqlutil.FieldColumn
}

// enqueueColumns are the table's fields that store a column, by field
// name, with the id sqlgen adds to a table with no key.
func enqueueColumns(schema *ir.Schema, td *ir.TypeDef, scalarTypes map[string]string) map[string]tableColumn {
	out := map[string]tableColumn{}
	for _, f := range td.Fields {
		if f == nil {
			continue
		}
		if column, ok := sqlutil.ColumnOf(schema, f, scalarTypes); ok {
			out[f.Name] = tableColumn{field: f, column: column}
		}
	}
	if sqlutil.HasIDColumn(td) {
		out["id"] = tableColumn{column: sqlutil.FieldColumn{Name: "id", Type: "UUID"}}
	}
	return out
}

func checkOneEnqueueOnChange(schema *ir.Schema, td *ir.TypeDef, def *ir.EnqueueOnChange, columns map[string]tableColumn, scalarTypes map[string]string, r *Result) {
	where := fmt.Sprintf("@enqueueOnChange(%s) on %s", def.Queue, td.Name)
	if err := registry.CheckEnqueueOnChange(def); err != nil {
		r.errorf(td.Owner, "%s: %v", where, err)
		return
	}
	queue := schema.Types[def.Queue]
	if queue == nil || queue.Queue == nil {
		r.errorf(td.Owner, "%s: %s is no @queue class of the schema; name the queue whose message the trigger enqueues", where, def.Queue)
		return
	}
	column := func(name, role string) (tableColumn, bool) {
		c, ok := columns[name]
		if !ok {
			r.errorf(td.Owner, "%s: %s names %q, which is no field of %s with a column", where, role, name, td.Name)
		}
		return c, ok
	}
	for _, name := range def.Fields {
		if c, ok := column(name, "fields"); ok && !comparable(c.column.Type) {
			r.errorf(td.Owner, "%s: fields names %s, a %s column, which IS DISTINCT FROM cannot compare; leave it out", where, name, c.column.Type)
		}
	}
	notNull := map[string]bool{}
	for _, cond := range def.When {
		c, ok := column(cond.Field, "when")
		if !ok {
			continue
		}
		switch {
		case (cond.IsNull || cond.NotNull) && !c.column.Nullable:
			r.errorf(td.Owner, "%s: when tests whether %s is null, and it is required, so it never is; leave the condition out", where, cond.Field)
		case cond.Equals != nil:
			if err := equalsFits(schema, c, cond.Equals); err != nil {
				r.errorf(td.Owner, "%s: when %s equals %v: %v", where, cond.Field, cond.Equals, err)
			}
		}
		if cond.NotNull {
			notNull[cond.Field] = true
		}
	}

	fields := map[string]*ir.FieldDef{}
	for _, f := range queue.Fields {
		if f != nil {
			fields[f.Name] = f
		}
	}
	names := make([]string, 0, len(def.Message))
	for name := range def.Message {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		from := def.Message[name]
		mf := fields[name]
		if mf == nil {
			r.errorf(td.Owner, "%s: message maps %s, which is no field of %s", where, name, queue.Name)
			continue
		}
		c, ok := column(from, "message field "+name)
		if !ok {
			continue
		}
		if err := linesUp(schema, mf, c, scalarTypes); err != nil {
			r.errorf(td.Owner, "%s: message field %s takes %s: %v", where, name, from, err)
			continue
		}
		if mf.Required && c.column.Nullable && !notNull[from] {
			r.errorf(td.Owner, "%s: message field %s is required, and %s may be null; add { field: %q, notNull: true } to when, or make %s optional", where, name, from, from, name)
		}
	}
	for _, mf := range queue.Fields {
		if mf == nil || !mf.Required {
			continue
		}
		if _, mapped := def.Message[mf.Name]; mapped || hasColumnDefault(schema, mf, scalarTypes) {
			continue
		}
		r.errorf(td.Owner, "%s: %s's field %s is required and its column has no default, so the message maps it to a field of %s", where, queue.Name, mf.Name, td.Name)
	}
}

// comparable reports whether IS DISTINCT FROM compares a column of type
// sqlType: a POINT and the PostGIS types have no equality operator.
func comparable(sqlType string) bool {
	switch strings.ToUpper(strings.TrimSuffix(sqlType, "[]")) {
	case "POINT", "GEOGRAPHY", "GEOMETRY":
		return false
	}
	return true
}

// equalsFits checks a condition's literal against its field: an enum's
// member, a string for a text column, a number for a numeric one, a whole
// one for an integer column, and a boolean for a BOOLEAN one. Any other
// column, a list, a UUID or a time among them, takes no equals.
func equalsFits(schema *ir.Schema, c tableColumn, literal any) error {
	if c.field == nil || c.column.Relation || c.field.TypeRef.IsArray || c.field.TypeRef.IsMap || c.field.JsonField {
		return fmt.Errorf("equals compares an enum, text, number or boolean field")
	}
	if enum := schema.Enums[c.field.TypeRef.Name]; enum != nil {
		s, ok := literal.(string)
		if !ok {
			return fmt.Errorf("%s is an enum, whose members are strings", c.field.Name)
		}
		for _, v := range enum.Values {
			if v.SerializedAs == s || (v.SerializedAs == "" && v.Name == s) {
				return nil
			}
		}
		return fmt.Errorf("%q is no member of %s", s, enum.Name)
	}
	switch base := strings.ToUpper(c.column.Type); {
	case base == "TEXT" || base == "CITEXT" || strings.HasPrefix(base, "VARCHAR") || strings.HasPrefix(base, "CHAR"):
		if _, ok := literal.(string); !ok {
			return fmt.Errorf("%s is a %s column, which equals compares with a string", c.field.Name, c.column.Type)
		}
	case base == "BOOLEAN" || base == "BOOL":
		if _, ok := literal.(bool); !ok {
			return fmt.Errorf("%s is a BOOLEAN column, which equals compares with true or false", c.field.Name)
		}
	case base == "INTEGER" || base == "BIGINT" || base == "SMALLINT":
		n, ok := literal.(float64)
		if !ok || n != float64(int64(n)) {
			return fmt.Errorf("%s is an %s column, which equals compares with a whole number", c.field.Name, c.column.Type)
		}
	case base == "REAL" || base == "DOUBLE PRECISION" || strings.HasPrefix(base, "NUMERIC"):
		if _, ok := literal.(float64); !ok {
			return fmt.Errorf("%s is a %s column, which equals compares with a number", c.field.Name, c.column.Type)
		}
	default:
		return fmt.Errorf("%s is a %s column; equals compares an enum, text, number or boolean field", c.field.Name, c.column.Type)
	}
	return nil
}

// linesUp checks that a message's field takes the column of a table's
// field: the same type, a list for a list, or, from a to-one relation, the
// type of the related table's key; and the same SQL column type, so the
// value copies across as it is.
func linesUp(schema *ir.Schema, mf *ir.FieldDef, c tableColumn, scalarTypes map[string]string) error {
	msgType := sqlutil.ColumnType(schema, mf, scalarTypes)
	if !strings.EqualFold(msgType, c.column.Type) {
		return fmt.Errorf("the message's column is %s and the table's %s", msgType, c.column.Type)
	}
	want := c.field
	if c.column.Relation {
		want = keyField(schema.Types[c.field.TypeRef.Name])
	}
	if want == nil {
		// The id sqlgen adds is a UUID, which the column types compared.
		return nil
	}
	if mf.TypeRef.Name != want.TypeRef.Name || mf.TypeRef.IsArray != want.TypeRef.IsArray || mf.TypeRef.IsMap != want.TypeRef.IsMap {
		return fmt.Errorf("the message's field is %s and the table's %s", typeName(mf.TypeRef), typeName(want.TypeRef))
	}
	return nil
}

// keyField is a table's @key field, else its id field, or nil for one that
// takes the id sqlgen adds.
func keyField(td *ir.TypeDef) *ir.FieldDef {
	if td == nil {
		return nil
	}
	for _, f := range td.Fields {
		if f != nil && f.Key {
			return f
		}
	}
	for _, f := range td.Fields {
		if f != nil && f.Name == "id" {
			return f
		}
	}
	return nil
}

// typeName names a field's type in an error.
func typeName(ref ir.TypeRef) string {
	switch {
	case ref.IsMap:
		return "a map of " + ref.Name
	case ref.IsArray:
		return "a list of " + ref.Name
	}
	return ref.Name
}

// hasColumnDefault reports whether a required field of a queue's message
// has a value when an insert leaves its column out, as sqlgen writes the
// column and the trigger the insert: a list takes the empty list, a
// timestamp, a date or a time the database's clock, and an enum with a
// schema default the default, which the trigger writes.
func hasColumnDefault(schema *ir.Schema, f *ir.FieldDef, scalarTypes map[string]string) bool {
	if f.AutoGenerated || (f.Default != nil && schema.Enums[f.TypeRef.Name] != nil && !f.TypeRef.IsArray && !f.TypeRef.IsMap) {
		return true
	}
	if f.JsonField || f.TypeRef.IsMap || f.TypeRef.IsArrayOfArrays {
		return false
	}
	if f.TypeRef.IsArray {
		return true
	}
	switch strings.ToUpper(sqlutil.ColumnType(schema, f, scalarTypes)) {
	case "TIMESTAMPTZ", "DATE", "TIME":
		return true
	}
	traits := codegen.GuessScalarTraits(f.TypeRef.Name)
	return traits.IsDateTimeLike || traits.IsDateLike || traits.IsTimeLike
}
