package notify

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/parable-work/superschematic/runtime/schema/go/notifications"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

// The statements over the notifier's own tables. Their fields are the
// loader's, the same in every database, so the notifier reads and writes
// them by the columns the generated ORM's definitions name, as the
// TypeScript notifier does through its untyped repositories: a row reads
// as its fields, a UUID in base62, a date-time a time.Time, a time of day
// its HH:MM:SS text, a JSON value decoded.

// table is one of the notifier's tables.
type table struct {
	notifications.Part
	part string
}

// c is the column of field, quoted.
func (t table) c(field string) string {
	if column, ok := t.Columns[field]; ok {
		return column.Name
	}
	return `"` + field + `"`
}

// selectList reads every column as its field.
func (t table) selectList() string {
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

// value is v as the column of field takes it: a UUID in the database's
// form.
func (t table) value(field string, v any) any {
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

// holds reports whether the column of field can hold id: any text, or a
// UUID in either form for a UUID column. A principal whose id is no UUID
// has no row of a table that keys principals by one.
func (t table) holds(field, id string) bool {
	if column, ok := t.Columns[field]; ok && column.UUID {
		return rowrefs.UUIDToDB(id) != ""
	}
	return true
}

// where is the conditions of a statement and their values.
type where struct {
	t     table
	conds []string
	args  []any
}

func (t table) where() *where { return &where{t: t} }

func (w *where) arg(v any) string {
	w.args = append(w.args, v)
	return "$" + strconv.Itoa(len(w.args))
}

// eq keeps the rows whose field is v.
func (w *where) eq(field string, v any) *where {
	w.conds = append(w.conds, fmt.Sprintf("%s = %s", w.t.c(field), w.arg(w.t.value(field, v))))
	return w
}

// ne keeps the rows whose field is not v, null among them.
func (w *where) ne(field string, v any) *where {
	w.conds = append(w.conds, fmt.Sprintf("%s IS DISTINCT FROM %s", w.t.c(field), w.arg(w.t.value(field, v))))
	return w
}

// in keeps the rows whose field is one of values.
func (w *where) in(field string, values ...string) *where {
	w.conds = append(w.conds, fmt.Sprintf("%s = ANY(%s)", w.t.c(field), w.arg(values)))
	return w
}

// null keeps the rows whose field is null.
func (w *where) null(field string) *where {
	w.conds = append(w.conds, w.t.c(field)+" IS NULL")
	return w
}

// cmp keeps the rows whose field is op v.
func (w *where) cmp(field, op string, v any) *where {
	w.conds = append(w.conds, fmt.Sprintf("%s %s %s", w.t.c(field), op, w.arg(v)))
	return w
}

func (w *where) sql() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

// find reads up to limit rows w keeps, ordered by order ("" for none).
func (n *Notifier) find(ctx context.Context, h notifications.Host, w *where, order string, limit int) ([]row, error) {
	sql := fmt.Sprintf("SELECT %s FROM %s%s", w.t.selectList(), w.t.Table, w.sql())
	if order != "" {
		sql += " ORDER BY " + order
	}
	if limit > 0 {
		sql += " LIMIT " + strconv.Itoa(limit)
	}
	rows, err := h.Query(ctx, sql, w.args...)
	if err != nil {
		return nil, fmt.Errorf("notify: read %s: %w", w.t.Type, err)
	}
	out := make([]row, len(rows))
	for i, r := range rows {
		out[i] = clean(r)
	}
	return out, nil
}

// findOne is the first row w keeps, nil for none.
func (n *Notifier) findOne(ctx context.Context, h notifications.Host, w *where, order string) (row, error) {
	rows, err := n.find(ctx, h, w, order, 1)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// byID is the row of t whose id is id, nil for none.
func (n *Notifier) byID(ctx context.Context, h notifications.Host, t table, id string) (row, error) {
	if id == "" || rowrefs.UUIDToDB(id) == "" {
		return nil, nil
	}
	return n.findOne(ctx, h, t.where().eq("id", id), "")
}

// insert writes a row of values into t and reads it back.
func (n *Notifier) insert(ctx context.Context, h notifications.Host, t table, values map[string]any) (row, error) {
	fields := make([]string, 0, len(values))
	for field := range values {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	columns := make([]string, len(fields))
	placeholders := make([]string, len(fields))
	args := make([]any, len(fields))
	for i, field := range fields {
		columns[i] = t.c(field)
		placeholders[i] = "$" + strconv.Itoa(i+1)
		args[i] = t.value(field, values[field])
	}
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s", t.Table, strings.Join(columns, ", "), strings.Join(placeholders, ", "), t.selectList())
	rows, err := h.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("notify: write %s: %w", t.Type, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("notify: write %s: no row came back", t.Type)
	}
	return clean(rows[0]), nil
}

// update sets values on the row of t whose id is id.
func (n *Notifier) update(ctx context.Context, h notifications.Host, t table, id string, values map[string]any) error {
	fields := make([]string, 0, len(values))
	for field := range values {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	sets := make([]string, len(fields))
	args := make([]any, 0, len(fields)+1)
	for i, field := range fields {
		args = append(args, t.value(field, values[field]))
		sets[i] = fmt.Sprintf("%s = $%d", t.c(field), i+1)
	}
	args = append(args, t.value("id", id))
	sql := fmt.Sprintf("UPDATE %s SET %s WHERE %s = $%d", t.Table, strings.Join(sets, ", "), t.c("id"), len(args))
	if _, err := h.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("notify: write %s: %w", t.Type, err)
	}
	return nil
}

// row is a row of one of the notifier's tables, by field.
type row map[string]any

// clean is a row as the notifier reads it: a UUID in base62, a JSON
// value decoded.
func clean(r notifications.Row) row {
	out := make(row, len(r))
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
	case pgtype.Time:
		// A time of day (a Temporal.Time column), which the driver reads
		// as microseconds of the day.
		if !value.Valid {
			return nil
		}
		seconds := value.Microseconds / 1_000_000
		text := fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
		if micros := value.Microseconds % 1_000_000; micros != 0 {
			text += strings.TrimRight(fmt.Sprintf(".%06d", micros), "0")
		}
		return text
	}
	return v
}

// str is a field of r as text, "" for null.
func (r row) str(field string) string {
	return textOf(r[field])
}

// textOf is a value as text, "" for null.
func textOf(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case time.Time:
		return value.UTC().Format("2006-01-02T15:04:05.000Z")
	case fmt.Stringer:
		return value.String()
	}
	return fmt.Sprint(v)
}

// int is a field of r as a number, 0 for null.
func (r row) int(field string) int64 {
	switch value := r[field].(type) {
	case int64:
		return value
	case int32:
		return int64(value)
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		n, _ := value.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(value, 10, 64)
		return n
	}
	return 0
}

// time is a field of r as an instant, nil for null.
func (r row) time(field string) *time.Time {
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

// strings is a field of r as a list of text; nil for null.
func (r row) strings(field string) []string {
	return stringsOf(r[field])
}

func stringsOf(v any) []string {
	switch value := v.(type) {
	case nil:
		return nil
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			out = append(out, textOf(item))
		}
		return out
	}
	return nil
}

// isNull reports whether field of r is null.
func (r row) isNull(field string) bool { return r[field] == nil }
