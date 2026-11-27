package orchestration

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/notifications"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

// The statements over the orchestration's own tables. Their fields are
// the loader's, the same in every database, so the inbound path and the
// dispatcher read them by the columns the generated ORM's definitions
// name, as the notifier reads its own (D190): a row reads as its fields,
// a UUID in base62, a date-time a time.Time, a JSON value decoded.

// Statements is what a statement runs on: a Host, or a host in a
// transaction of it.
type Statements interface {
	Query(ctx context.Context, sql string, args ...any) ([]notifications.Row, error)
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
}

// Table is one of the orchestration's tables, a part.
type Table struct {
	notifications.Part
}

// C is the column of field, quoted.
func (t Table) C(field string) string {
	if column, ok := t.Columns[field]; ok {
		return column.Name
	}
	return `"` + field + `"`
}

// Has reports whether the table has field.
func (t Table) Has(field string) bool {
	_, ok := t.Columns[field]
	return ok
}

// selectList reads every column as its field.
func (t Table) selectList() string {
	fields := make([]string, 0, len(t.Columns))
	for field := range t.Columns {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	out := make([]string, len(fields))
	for i, field := range fields {
		out[i] = fmt.Sprintf(`%s AS "%s"`, t.Columns[field].Name, field)
	}
	return strings.Join(out, ", ")
}

// Value is v as the column of field takes it: a UUID in the database's
// form, "" as null for a UUID.
func (t Table) Value(field string, v any) any {
	if column, ok := t.Columns[field]; ok && column.UUID {
		if s, ok := v.(string); ok {
			if s == "" {
				return nil
			}
			if id := rowrefs.UUIDToDB(s); id != "" {
				return id
			}
		}
	}
	return v
}

// Holds reports whether the column of field can hold id: any text, or a
// UUID in either form for a UUID column.
func (t Table) Holds(field, id string) bool {
	if column, ok := t.Columns[field]; ok && column.UUID {
		return rowrefs.UUIDToDB(id) != ""
	}
	return true
}

// Where is the conditions of a statement and their values.
type Where struct {
	t     Table
	conds []string
	args  []any
}

// Where starts the conditions of a statement over t.
func (t Table) Where() *Where { return &Where{t: t} }

func (w *Where) arg(v any) string {
	w.args = append(w.args, v)
	return "$" + strconv.Itoa(len(w.args))
}

// Eq keeps the rows whose field is v.
func (w *Where) Eq(field string, v any) *Where {
	w.conds = append(w.conds, fmt.Sprintf("%s = %s", w.t.C(field), w.arg(w.t.Value(field, v))))
	return w
}

// In keeps the rows whose field is one of values.
func (w *Where) In(field string, values ...string) *Where {
	w.conds = append(w.conds, fmt.Sprintf("%s = ANY(%s)", w.t.C(field), w.arg(values)))
	return w
}

// Null keeps the rows whose field is null, NotNull those whose field is
// not.
func (w *Where) Null(field string) *Where {
	w.conds = append(w.conds, w.t.C(field)+" IS NULL")
	return w
}

func (w *Where) NotNull(field string) *Where {
	w.conds = append(w.conds, w.t.C(field)+" IS NOT NULL")
	return w
}

// Cmp keeps the rows whose field is op v.
func (w *Where) Cmp(field, op string, v any) *Where {
	w.conds = append(w.conds, fmt.Sprintf("%s %s %s", w.t.C(field), op, w.arg(v)))
	return w
}

func (w *Where) sql() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

// Find reads up to limit rows w keeps (none for no limit), ordered by
// order, fields and DESC as SQL names them ("" for none).
func Find(ctx context.Context, h Statements, w *Where, order string, limit int) ([]Row, error) {
	sql := fmt.Sprintf("SELECT %s FROM %s%s", w.t.selectList(), w.t.Table, w.sql())
	if order != "" {
		sql += " ORDER BY " + order
	}
	if limit > 0 {
		sql += " LIMIT " + strconv.Itoa(limit)
	}
	rows, err := h.Query(ctx, sql, w.args...)
	if err != nil {
		return nil, fmt.Errorf("orchestration: read %s: %w", w.t.Type, err)
	}
	out := make([]Row, len(rows))
	for i, r := range rows {
		out[i] = Clean(r)
	}
	return out, nil
}

// FindOne is the first row w keeps, nil for none.
func FindOne(ctx context.Context, h Statements, w *Where, order string) (Row, error) {
	rows, err := Find(ctx, h, w, order, 1)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// ByID is the row of t whose id is id, nil for none.
func ByID(ctx context.Context, h Statements, t Table, id string) (Row, error) {
	if id == "" || !t.Holds("id", id) {
		return nil, nil
	}
	return FindOne(ctx, h, t.Where().Eq("id", id), "")
}

// Insert writes a row of values into t and reads it back.
func Insert(ctx context.Context, h Statements, t Table, values map[string]any) (Row, error) {
	fields := make([]string, 0, len(values))
	for field := range values {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	columns := make([]string, len(fields))
	placeholders := make([]string, len(fields))
	args := make([]any, len(fields))
	for i, field := range fields {
		columns[i] = t.C(field)
		placeholders[i] = "$" + strconv.Itoa(i+1)
		args[i] = t.Value(field, values[field])
	}
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s", t.Table, strings.Join(columns, ", "), strings.Join(placeholders, ", "), t.selectList())
	rows, err := h.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("orchestration: write %s: %w", t.Type, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("orchestration: write %s: no row came back", t.Type)
	}
	return Clean(rows[0]), nil
}

// Update sets values on the rows w keeps and returns how many it set.
func Update(ctx context.Context, h Statements, w *Where, values map[string]any) (int64, error) {
	t := w.t
	fields := make([]string, 0, len(values))
	for field := range values {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	sets := make([]string, len(fields))
	args := make([]any, 0, len(fields)+len(w.args))
	// The conditions' placeholders come first.
	args = append(args, w.args...)
	for i, field := range fields {
		args = append(args, t.Value(field, values[field]))
		sets[i] = fmt.Sprintf("%s = $%d", t.C(field), len(args))
	}
	sql := fmt.Sprintf("UPDATE %s SET %s%s", t.Table, strings.Join(sets, ", "), w.sql())
	n, err := h.Exec(ctx, sql, args...)
	if err != nil {
		return 0, fmt.Errorf("orchestration: write %s: %w", t.Type, err)
	}
	return n, nil
}

// UpdateID sets values on the row of t whose id is id and reads it back,
// nil when there is none.
func UpdateID(ctx context.Context, h Statements, t Table, id string, values map[string]any) (Row, error) {
	if _, err := Update(ctx, h, t.Where().Eq("id", id), values); err != nil {
		return nil, err
	}
	return ByID(ctx, h, t, id)
}

// Row is a row of one of the orchestration's tables, or of a repository's
// JSON, by field.
type Row map[string]any

// Clean is a row as the orchestration reads it: a UUID in base62, a JSON
// value decoded.
func Clean(r map[string]any) Row {
	out := make(Row, len(r))
	for k, v := range r {
		out[k] = cleanValue(v)
	}
	return out
}

func cleanValue(v any) any {
	switch value := v.(type) {
	case [16]byte:
		b := value[:]
		return rowrefs.UUIDFromDB(fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16])))
	case []byte:
		var decoded any
		if json.Unmarshal(value, &decoded) == nil {
			return decoded
		}
		return string(value)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = cleanValue(item)
		}
		return out
	}
	return v
}

// Str is a field of r as text, "" for null.
func (r Row) Str(field string) string {
	return TextOf(r[field])
}

// TextOf is a value as text, "" for null.
func TextOf(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	case time.Time:
		return value.UTC().Format("2006-01-02T15:04:05.000Z")
	case fmt.Stringer:
		return value.String()
	}
	return fmt.Sprint(v)
}

// Int is a field of r as a number, 0 for null.
func (r Row) Int(field string) int64 {
	return IntOf(r[field])
}

// IntOf is a value as a number, 0 for null or none.
func IntOf(v any) int64 {
	switch value := v.(type) {
	case int64:
		return value
	case int32:
		return int64(value)
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		if n, err := value.Int64(); err == nil {
			return n
		}
		f, _ := value.Float64()
		return int64(f)
	case string:
		n, _ := strconv.ParseInt(value, 10, 64)
		return n
	}
	return 0
}

// Float is a field of r as a number, 0 for null.
func (r Row) Float(field string) float64 {
	switch value := r[field].(type) {
	case float64:
		return value
	case json.Number:
		f, _ := value.Float64()
		return f
	}
	return float64(r.Int(field))
}

// Bool is a field of r as a boolean, false for null.
func (r Row) Bool(field string) bool {
	b, _ := r[field].(bool)
	return b
}

// Time is a field of r as an instant, nil for null.
func (r Row) Time(field string) *time.Time {
	switch value := r[field].(type) {
	case time.Time:
		return &value
	case string:
		if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return &t
		}
	}
	return nil
}

// Strings is a field of r as a list of text; nil for null.
func (r Row) Strings(field string) []string {
	return StringsOf(r[field])
}

// StringsOf is a value as a list of text; nil for null.
func StringsOf(v any) []string {
	switch value := v.(type) {
	case nil:
		return nil
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			out = append(out, TextOf(item))
		}
		return out
	}
	return nil
}

// IsNull reports whether field of r is null.
func (r Row) IsNull(field string) bool { return r[field] == nil }

// Object is a field of r as a JSON object; nil for null or another value.
func (r Row) Object(field string) map[string]any {
	m, _ := r[field].(map[string]any)
	return m
}
