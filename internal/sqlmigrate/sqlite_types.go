package sqlmigrate

import (
	"fmt"
	"regexp"
	"strings"
)

// SQLite stores a value as one of five storage classes, and a column's
// declared type gives it an affinity (https://sqlite.org/datatype3.html).
// The SQLite model stores each catalog type as the type its values need
// (D27): text forms as TEXT, numbers as INTEGER, REAL or NUMERIC, bytes as
// BLOB, and lists and JSON as TEXT that holds JSON.

const (
	sqliteText    = "TEXT"
	sqliteNoCase  = "TEXT COLLATE NOCASE"
	sqliteInteger = "INTEGER"
	sqliteReal    = "REAL"
	sqliteNumeric = "NUMERIC"
	sqliteBlob    = "BLOB"
)

// sqliteTypes maps the base of a Postgres type to its SQLite type.
var sqliteTypes = map[string]string{
	"UUID":                sqliteText,
	"TEXT":                sqliteText,
	"VARCHAR":             sqliteText,
	"CHAR":                sqliteText,
	"CITEXT":              sqliteNoCase,
	"DATE":                sqliteText,
	"TIME":                sqliteText,
	"TIME WITH TIME ZONE": sqliteText,
	"TIMETZ":              sqliteText,
	"TIMESTAMP":           sqliteText,
	"TIMESTAMPTZ":         sqliteText,
	"INTERVAL":            sqliteText,
	"INET":                sqliteText,
	"SMALLINT":            sqliteInteger,
	"INTEGER":             sqliteInteger,
	"BIGINT":              sqliteInteger,
	"BOOLEAN":             sqliteInteger,
	"REAL":                sqliteReal,
	"DOUBLE PRECISION":    sqliteReal,
	"NUMERIC":             sqliteNumeric,
	"JSONB":               sqliteText,
	"JSON":                sqliteText,
	"BYTEA":               sqliteBlob,
}

// sqliteNoStorage are the Postgres types SQLite has no storage for: LTREE,
// POINT and the PostGIS types.
var sqliteNoStorage = map[string]bool{"LTREE": true, "POINT": true, "GEOGRAPHY": true, "GEOMETRY": true}

// sqliteType is the SQLite type of a column of Postgres type pg. A list is
// TEXT that holds a JSON array.
func sqliteType(pg string) (string, error) {
	t := parsePGType(pg)
	if sqliteNoStorage[t.base] {
		return "", fmt.Errorf("SQLite has no storage for %s", pg)
	}
	sqlite, ok := sqliteTypes[t.base]
	if !ok {
		return "", fmt.Errorf("the sqlite dialect has no type for %s", pg)
	}
	if t.array {
		return sqliteText, nil
	}
	return sqlite, nil
}

// sqliteHolds is what a column of Postgres type pg holds as JSON TEXT in
// SQLite: a list's JSON array, or a JSON value. A list of lists is JSONB
// in Postgres, so a JSON value here. Any other column holds no JSON.
func sqliteHolds(pg string) string {
	t := parsePGType(pg)
	switch {
	case t.array:
		return holdsList
	case t.base == "JSONB", t.base == "JSON":
		return holdsJSON
	}
	return ""
}

// The elements of a list's JSON array that no SQLite type names
// (Column.Element).
const (
	elementBoolean = "BOOLEAN"
	elementJSON    = "JSON"
)

// sqliteElement is what each element of a column of Postgres type pg holds
// in the column's JSON array, or "" when the column is not a list. JSON
// keeps text as a string and a number as a number, which SQLite reads as
// the type it stores the element's type as; no collation applies inside
// JSON. It keeps a boolean as true or false, which SQLite reads as 1 and 0
// but keeps apart from them, and a JSON value as itself.
func sqliteElement(pg string) string {
	t := parsePGType(pg)
	if !t.array {
		return ""
	}
	switch t.base {
	case "BOOLEAN":
		return elementBoolean
	case "JSONB", "JSON":
		return elementJSON
	}
	return sqliteAffinity(sqliteTypes[t.base])
}

// SQLite defaults write the forms the schema runtime reads.
const (
	// sqliteUUIDDefault is a version 4 UUID in its lowercase string form:
	// 122 random bits, the version nibble 4, and the variant bits 10.
	sqliteUUIDDefault = "(lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || " +
		"substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || " +
		"substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6))))"
	// sqliteTimestampDefault is the current instant in RFC 3339, UTC, to
	// the millisecond.
	sqliteTimestampDefault = "(strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))"
	// sqliteDateDefault is the current UTC date.
	sqliteDateDefault = "(strftime('%Y-%m-%d', 'now'))"
	// sqliteTimeDefault is the current UTC time of day, to the millisecond.
	sqliteTimeDefault = "(strftime('%H:%M:%f', 'now'))"
)

var (
	// pgJSONLiteral is a string literal cast to JSON: a platform default.
	pgJSONLiteral = regexp.MustCompile(`^('(?:[^']|'')*')::jsonb?$`)
	// sqlLiteral is a constant: a number, a string, NULL or a boolean.
	sqlLiteral = regexp.MustCompile(`^(?:[-+]?[0-9]+(?:\.[0-9]+)?(?:[eE][-+]?[0-9]+)?|'(?:[^']|'')*'|NULL|TRUE|FALSE)$`)
)

// sqliteDefault is the SQLite form of a Postgres DEFAULT expression on a
// column of Postgres type pg, or an error for one it has none of.
func sqliteDefault(def, pg string) (string, error) {
	switch strings.ToLower(def) {
	case "":
		return "", nil
	case "gen_random_uuid()":
		return sqliteUUIDDefault, nil
	case "current_timestamp", "now()":
		return sqliteTimestampDefault, nil
	case "current_date":
		return sqliteDateDefault, nil
	case "current_time":
		return sqliteTimeDefault, nil
	}
	if parsePGType(pg).array && def == "'{}'" {
		return "'[]'", nil
	}
	if m := pgJSONLiteral.FindStringSubmatch(def); m != nil {
		return m[1], nil
	}
	if sqlLiteral.MatchString(strings.ToUpper(def)) {
		return def, nil
	}
	return "", fmt.Errorf("the sqlite dialect has no form of the default %s", def)
}

// sqliteConstant reports whether a default is a constant, which ALTER TABLE
// ADD COLUMN takes. CURRENT_TIMESTAMP and an expression in parentheses are
// not.
func sqliteConstant(def string) bool {
	return sqlLiteral.MatchString(strings.ToUpper(def))
}

// sqliteAffinity is the affinity a SQLite type in the model's spelling
// gives its column.
func sqliteAffinity(t string) string {
	if t == sqliteNoCase {
		return sqliteText
	}
	return t
}

// convert classifies a column's change between a scalar, a list and a
// JSON value as well as its type. A list and a JSON value are TEXT like
// text, so their types do not tell the change. A JSON value's text stays
// as it is when the column becomes text, as Postgres's cast to text keeps
// it, and casts to any other scalar as text does. No other change between
// them keeps every value as the new kind reads it in the meaning Postgres
// gives it: text is not a JSON array, Postgres parses text as JSON where
// SQLite's json_quote would wrap it, and Postgres converts no list to or
// from anything else. So each of them is impossible: the operator changes
// the column by hand and adopts the new model (D27). A list is TEXT
// whatever its element, so a list's change is its element's
// (sqliteListConvert).
func (sqliteDialect) convert(from, to *Column) conversion {
	switch {
	case from.Holds == holdsList && to.Holds == holdsList:
		return sqliteListConvert(from.Element, to.Element)
	case from.Holds == to.Holds:
		return sqliteConvert(from.Type, to.Type)
	case from.Holds == holdsJSON && to.Holds == "":
		c := sqliteConvert(from.Type, to.Type)
		if c.kind == convertSame {
			// The values stay; the change still needs a step for its
			// hazards, and SQLite rebuilds a table for a type change.
			c.kind = convertRewrite
		}
		return c
	}
	return conversion{kind: convertImpossible}
}

// sqliteConvert classifies a type change. SQLite's CAST never fails: a
// value the new type cannot hold becomes another one, such as 0 for text
// that is not a number, so a cast that cannot keep every value is lossy
// rather than one that may fail.
func sqliteConvert(from, to string) conversion {
	if from == to {
		return conversion{kind: convertSame}
	}
	f, t := sqliteAffinity(from), sqliteAffinity(to)
	switch {
	case f == t:
		// The collation changes; the values do not.
		return conversion{kind: convertRewrite}
	case t == sqliteText && f == sqliteBlob:
		// Bytes that are not UTF-8 do not survive as text.
		return conversion{kind: convertRewrite, lossy: true}
	case t == sqliteText:
		return conversion{kind: convertRewrite}
	case f == sqliteText && t == sqliteBlob:
		return conversion{kind: convertRewrite}
	case f == sqliteInteger && t == sqliteNumeric, f == sqliteReal && t == sqliteNumeric:
		return conversion{kind: convertRewrite}
	case f == sqliteBlob || t == sqliteBlob:
		return conversion{kind: convertImpossible}
	}
	// Text that is not a number becomes 0, a fraction is cut toward zero
	// as an INTEGER, and an integer past 2^53 is rounded as a REAL.
	return conversion{kind: convertRewrite, lossy: true}
}

// sqliteListConvert classifies a change of a list's element from one type
// to another (Column.Element). A rebuild converts each element in order
// (sqliteListValue). A cast between TEXT, INTEGER, REAL and NUMERIC keeps
// or loses values as the same cast of a column does (sqliteConvert). A
// boolean becomes 1 or 0 as a number and true or false as text, which
// keeps every value; any other element becomes a boolean by SQLite's truth
// test, which keeps only 0 and 1. SQLite's CAST never fails, so a
// conversion that cannot keep every element is lossy, never one that may
// fail, and says what it loses (sqliteElementLoss).
//
// A JSON value or bytes converts to and from nothing. SQLite's CAST keeps
// a nested JSON value as JSON, where Postgres's cast to text gives its
// text, and SQLite's JSON holds no BLOB, so bytes in a list are text in an
// encoding no SQLite function reads. An element a model does not record,
// as in a SQLite model built before models recorded one, is taken as
// unchanged.
func sqliteListConvert(from, to string) conversion {
	switch {
	case from == to, from == "", to == "":
		return conversion{kind: convertSame}
	case from == elementJSON, to == elementJSON, from == sqliteBlob, to == sqliteBlob:
		return conversion{kind: convertImpossible}
	case from == elementBoolean:
		return conversion{kind: convertRewrite}
	}
	c := conversion{kind: convertRewrite, lossy: true}
	if to != elementBoolean {
		c = sqliteConvert(from, to)
	}
	if c.lossy {
		c.loss = sqliteElementLoss(from, to)
	}
	return c
}

// sqliteElementLoss says what a lossy conversion of a list's elements
// loses, ending the destructive hazard's sentence.
func sqliteElementLoss(from, to string) string {
	var lost []string
	switch {
	case to == elementBoolean && from == sqliteText:
		lost = append(lost, "text is true only where it reads as a number other than 0, so 'true' becomes false")
	case to == elementBoolean:
		lost = append(lost, "every number other than 0 becomes true")
	case from == sqliteText:
		lost = append(lost, "text that is not a number becomes 0")
		if to == sqliteInteger {
			lost = append(lost, "a fraction is cut toward zero")
		}
	case to == sqliteInteger:
		lost = append(lost, "a fraction is cut toward zero")
	case to == sqliteReal:
		lost = append(lost, "an integer past 2^53 is rounded")
	}
	return "SQLite converts each element with a cast that never fails, so the conversion does not keep every element: " +
		joinAnd(lost) + "."
}
