package sqlgen

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// A table's @seedRows (#625, #667): the rows it holds from its first
// migration on. create.sql inserts each, on Postgres and SQLite (the
// SQLite create.sql is the plan from an empty database); the migration
// planner's model (internal/sqlmigrate) holds each by its table and key,
// and a plan inserts one that is new. Each insert leaves a row the table
// already holds of the same key alone, and skips one another unique
// constraint refuses, so it may run again and never overwrites an app's
// edit.

// SeedRow is one row a table's @seedRows declares.
type SeedRow struct {
	// Origin is the table's type.
	Origin string

	Table, QuotedTable string

	// Key is the row's key, its key fields' values by field as a JSON
	// object, which identifies the row among the table's seeds.
	Key string

	// Values are the columns the row sets, in the table's column order;
	// KeyValues are those of its key, in the key's order.
	Values, KeyValues []SeedValue
}

// SeedValue is a column a seed row sets and its value.
type SeedValue struct {
	// Column is the column's name, QuotedColumn as Postgres writes it.
	Column, QuotedColumn string
	// Type is the column's Postgres type, which the value is written as.
	Type string
	// Value is the value as the IR holds it: a string, a number, a
	// boolean, nil, a list or a JSON value.
	Value any
}

// buildSeedRows builds each @seedRows row of the schema's tables, in table
// order, then declaration order.
func buildSeedRows(tableTypes []*ir.TypeDef, tables []Table) ([]SeedRow, error) {
	var out []SeedRow
	for _, td := range tableTypes {
		if td.SeedRows == nil {
			continue
		}
		var table *Table
		for i := range tables {
			if tables[i].OriginalName == td.Name && tables[i].Name == sqlutil.TableName(td) {
				table = &tables[i]
			}
		}
		if table == nil {
			return nil, fmt.Errorf("@seedRows on %s: %s has no table", td.Name, td.Name)
		}
		column := func(field string) *Column {
			for i := range table.Columns {
				if table.Columns[i].Origin == td.Name+"."+field {
					return &table.Columns[i]
				}
			}
			return nil
		}
		key := td.SeedRows.KeyOf(td)
		if len(key) == 0 {
			return nil, fmt.Errorf("@seedRows on %s: no fields identify a seeded row", td.Name)
		}
		for i, row := range td.SeedRows.Rows {
			for field := range row {
				if column(field) == nil {
					return nil, fmt.Errorf("@seedRows on %s: row %d names %s, which has no column of its own", td.Name, i, field)
				}
			}
			seed := SeedRow{Origin: td.Name, Table: table.Name, QuotedTable: table.QuotedName}
			for _, c := range table.Columns {
				field := strings.TrimPrefix(c.Origin, td.Name+".")
				if value, ok := row[field]; ok && c.Origin == td.Name+"."+field {
					seed.Values = append(seed.Values, SeedValue{Column: c.Name, QuotedColumn: c.QuotedName, Type: c.Type, Value: value})
				}
			}
			keyObject := map[string]any{}
			for _, field := range key {
				c := column(field)
				value, ok := row[field]
				if c == nil || !ok {
					return nil, fmt.Errorf("@seedRows on %s: row %d gives no %s, which its key names", td.Name, i, field)
				}
				seed.KeyValues = append(seed.KeyValues, SeedValue{Column: c.Name, QuotedColumn: c.QuotedName, Type: c.Type, Value: value})
				keyObject[field] = value
			}
			data, err := json.Marshal(keyObject)
			if err != nil {
				return nil, fmt.Errorf("@seedRows on %s: row %d's key: %w", td.Name, i, err)
			}
			seed.Key = string(data)
			out = append(out, seed)
		}
	}
	return out, nil
}

// InsertSQL inserts the row on Postgres unless the table holds a row of
// its key, or another unique constraint refuses it.
func (s SeedRow) InsertSQL() string {
	return s.insertSQL(s.QuotedTable, func(v SeedValue) string { return v.QuotedColumn }, postgresSeedLiteral)
}

// SQLiteInsertSQL is InsertSQL on SQLite, its identifiers quoted by quote.
func (s SeedRow) SQLiteInsertSQL(quote func(string) string) string {
	return s.insertSQL(quote(s.Table), func(v SeedValue) string { return quote(v.Column) }, sqliteSeedLiteral)
}

// insertSQL renders the insert: table is quoted, column quotes a value's
// column, and literal writes a value in the dialect.
func (s SeedRow) insertSQL(table string, column func(SeedValue) string, literal func(SeedValue) string) string {
	columns := make([]string, len(s.Values))
	values := make([]string, len(s.Values))
	for i, v := range s.Values {
		columns[i], values[i] = column(v), literal(v)
	}
	match := make([]string, len(s.KeyValues))
	for i, v := range s.KeyValues {
		if v.Value == nil {
			match[i] = column(v) + " IS NULL"
		} else {
			match[i] = column(v) + " = " + literal(v)
		}
	}
	return "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ")\nSELECT " + strings.Join(values, ", ") +
		"\nWHERE NOT EXISTS (SELECT 1 FROM " + table + " WHERE " + strings.Join(match, " AND ") + ")\nON CONFLICT DO NOTHING"
}

// pgTypeOf splits a Postgres column type into its base and whether it is
// a list.
func pgTypeOf(t string) (base string, list bool) {
	if strings.HasSuffix(t, "[]") {
		return strings.ToUpper(strings.TrimSuffix(t, "[]")), true
	}
	return strings.ToUpper(t), false
}

// isJSONType reports whether a Postgres base type holds JSON.
func isJSONType(base string) bool { return base == "JSONB" || base == "JSON" }

// postgresSeedLiteral writes a seeded value as a Postgres literal of its
// column's type, cast, so the SELECT the insert reads it from types it as
// the column: a list as an ARRAY, JSON as its text.
func postgresSeedLiteral(v SeedValue) string {
	base, list := pgTypeOf(v.Type)
	if v.Value == nil {
		return "NULL::" + v.Type
	}
	if isJSONType(base) {
		return stringLiteral(seedJSON(v.Value)) + "::" + v.Type
	}
	if list {
		items, _ := v.Value.([]any)
		if len(items) == 0 {
			return "'{}'::" + v.Type
		}
		elements := make([]string, len(items))
		for i, item := range items {
			elements[i] = seedScalar(item, base == "BOOLEAN", "TRUE", "FALSE")
		}
		return "ARRAY[" + strings.Join(elements, ", ") + "]::" + v.Type
	}
	return seedScalar(v.Value, base == "BOOLEAN", "TRUE", "FALSE") + "::" + v.Type
}

// sqliteSeedLiteral writes a seeded value as SQLite stores it (D83): a
// list and JSON as JSON text, a boolean as 1 or 0.
func sqliteSeedLiteral(v SeedValue) string {
	base, list := pgTypeOf(v.Type)
	switch {
	case v.Value == nil:
		return "NULL"
	case list || isJSONType(base):
		return stringLiteral(seedJSON(v.Value))
	}
	return seedScalar(v.Value, base == "BOOLEAN", "1", "0")
}

// seedScalar writes one value: a boolean as yes or no, a number as it is,
// and anything else as a string literal of its text.
func seedScalar(value any, boolean bool, yes, no string) string {
	switch v := value.(type) {
	case bool:
		if v {
			return yes
		}
		return no
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case json.Number:
		return v.String()
	case string:
		return stringLiteral(v)
	}
	return stringLiteral(seedJSON(value))
}

// seedJSON is a value's JSON text, its object keys sorted.
func seedJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}
