package sqlutil

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// What a column says beyond its type (D83): the DEFAULT a field's
// Default<T, V> gives it, and the CHECK its enum or its Validate rules
// give it. sqlgen writes both into create.sql, the migration planner
// diffs them, and verification refuses a required user column whose
// default the DDL cannot carry.

// sqlTypeBase is a column type's name without its modifiers or array
// brackets, upper-cased: "VARCHAR(80)" is VARCHAR.
func sqlTypeBase(columnType string) string {
	t := strings.TrimSuffix(strings.TrimSpace(columnType), "[]")
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i]
	}
	return strings.ToUpper(strings.Join(strings.Fields(t), " "))
}

// IsTextType reports whether a column type holds text: TEXT, VARCHAR,
// CHAR or CITEXT.
func IsTextType(columnType string) bool {
	switch sqlTypeBase(columnType) {
	case "TEXT", "VARCHAR", "CHAR", "CHARACTER", "CHARACTER VARYING", "CITEXT":
		return true
	}
	return false
}

// isIntegerType reports whether a column type holds integers.
func isIntegerType(columnType string) bool {
	switch sqlTypeBase(columnType) {
	case "SMALLINT", "INTEGER", "INT", "BIGINT":
		return true
	}
	return false
}

// IsNumberType reports whether a column type holds numbers.
func IsNumberType(columnType string) bool {
	switch sqlTypeBase(columnType) {
	case "REAL", "DOUBLE PRECISION", "NUMERIC", "DECIMAL":
		return true
	}
	return isIntegerType(columnType)
}

// scalarColumn reports whether field's column holds one value of its own,
// as the database sees it: not a list, a map or a JSON value, and not an
// Encrypted<T> field's ciphertext (D85), whose Validate rules bound the
// plaintext, which the decoders check, and never the text stored.
func scalarColumn(field *ir.FieldDef, columnType string) bool {
	return !field.EncryptedAtRest && !field.JsonField && !field.TypeRef.IsArray && !field.TypeRef.IsMap &&
		!strings.HasSuffix(columnType, "[]") && sqlTypeBase(columnType) != "JSONB"
}

// ColumnDefault returns the DEFAULT the column of field, of type
// columnType, takes from the field's Default<T, V>: V as a SQL literal.
// ok is false for a field with no default and for one whose default stays
// the decoders' alone:
//
//   - an optional field: its column defaults to NULL, which the ORMs write
//     for a value left unset, and the Go ORM leaves an optional zero out of
//     an insert;
//   - a key, a list, a map, a JSON value or an Encrypted<T> field;
//   - a column whose type is not text, a boolean or a number, such as a
//     UUID, a time or a duration, whose stored form is not the form V is
//     written in.
//
// An enum is text, so its member's value is a quoted literal.
func ColumnDefault(field *ir.FieldDef, columnType string) (string, bool) {
	if field.Default == nil || !field.Required || field.Key || !scalarColumn(field, columnType) {
		return "", false
	}
	v := *field.Default
	switch {
	case IsTextType(columnType):
		return QuoteLiteral(v), true
	case sqlTypeBase(columnType) == "BOOLEAN":
		if v == "true" || v == "false" {
			return v, true
		}
	case isIntegerType(columnType):
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return strconv.FormatInt(n, 10), true
		}
	case IsNumberType(columnType):
		// As written, so a NUMERIC keeps every digit.
		if numberLiteral.MatchString(v) {
			return v, true
		}
	}
	return "", false
}

// numberLiteral is a number as SQL and JSON both write it.
var numberLiteral = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)

// QuoteLiteral renders s as a SQL string literal.
func QuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// Check is the CHECK constraint a field gives its column (D83): the values
// its enum allows, or the bounds of its Validate rules. A field has at most
// one; its rules are ANDed. NULL passes every check, so a nullable column
// stays nullable.
type Check struct {
	// Column is the column's name.
	Column string
	// In lists the serialized values of the enum's members, in declaration
	// order.
	In []string
	// Min and Max bound a number column (Validate min and max).
	Min, Max *float64
	// MinLength and MaxLength bound a text column's length in characters,
	// code points as every validator counts them (Validate minLength and
	// maxLength).
	MinLength, MaxLength *int
}

// ColumnCheck returns the check of field's column, named column and of
// type columnType: a scalar enum column's members (enum is the field's
// enum, nil when its type is none), or a scalar number or text column's
// Validate bounds. ok is false when the column has none: a list, a map, a
// JSON value or an Encrypted<T> field, a pattern (which the dialects'
// regular expressions do not read alike), and any other rule.
func ColumnCheck(field *ir.FieldDef, column, columnType string, enum *ir.EnumDef) (Check, bool) {
	if !scalarColumn(field, columnType) {
		return Check{}, false
	}
	c := Check{Column: column}
	switch {
	case enum != nil && IsTextType(columnType):
		c.In = EnumValues(enum)
		return c, len(c.In) > 0
	case IsNumberType(columnType):
		c.Min, c.Max = field.ValidateMin, field.ValidateMax
		return c, c.Min != nil || c.Max != nil
	case IsTextType(columnType):
		c.MinLength, c.MaxLength = field.ValidateMinLength, field.ValidateMaxLength
		return c, c.MinLength != nil || c.MaxLength != nil
	}
	return Check{}, false
}

// EnumValues lists the serialized values of an enum's members, in
// declaration order: the values a column of the enum holds.
func EnumValues(enum *ir.EnumDef) []string {
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

// Expression renders the check's predicate, without CHECK and its
// parentheses, with the column quoted by quote: `status IN ('a', 'b')`,
// `rating >= 1 AND rating <= 5`, `length(title) <= 100`. length() counts
// characters in Postgres and SQLite alike.
func (c Check) Expression(quote func(string) string) string {
	col := quote(c.Column)
	if len(c.In) > 0 {
		values := make([]string, len(c.In))
		for i, v := range c.In {
			values[i] = QuoteLiteral(v)
		}
		return col + " IN (" + strings.Join(values, ", ") + ")"
	}
	var terms []string
	number := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
	if c.Min != nil {
		terms = append(terms, col+" >= "+number(*c.Min))
	}
	if c.Max != nil {
		terms = append(terms, col+" <= "+number(*c.Max))
	}
	if c.MinLength != nil {
		terms = append(terms, "length("+col+") >= "+strconv.Itoa(*c.MinLength))
	}
	if c.MaxLength != nil {
		terms = append(terms, "length("+col+") <= "+strconv.Itoa(*c.MaxLength))
	}
	return strings.Join(terms, " AND ")
}

// CheckName is the name of the check on table's column: ck_<table>_<column>,
// as a foreign key's is fk_<table>_<column>. sqlgen cuts it to the 63 bytes
// Postgres keeps, as it does a foreign key's.
func CheckName(table, column string) string {
	return "ck_" + table + "_" + column
}

var opclassName = regexp.MustCompile(`^[a-z_][a-z0-9_]*(\.[a-z_][a-z0-9_]*)?$`)

// ValidOpclass reports whether s can name an operator class in @index's
// ops: a lowercase identifier, optionally schema-qualified.
func ValidOpclass(s string) bool {
	return opclassName.MatchString(s)
}

// IndexWith is the WITH list of a vector index's storage parameters
// (@index's m, efConstruction and lists, D84), "m = 16, ef_construction =
// 64", in that order; empty when it sets none.
func IndexWith(idx ir.IndexDef) string {
	var params []string
	if idx.M != nil {
		params = append(params, fmt.Sprintf("m = %d", *idx.M))
	}
	if idx.EFConstruction != nil {
		params = append(params, fmt.Sprintf("ef_construction = %d", *idx.EFConstruction))
	}
	if idx.Lists != nil {
		params = append(params, fmt.Sprintf("lists = %d", *idx.Lists))
	}
	return strings.Join(params, ", ")
}
