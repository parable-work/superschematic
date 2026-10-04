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

// sqliteNoStorage are the Postgres types SQLite has no storage for: LTREE
// and the PostGIS types.
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

// convert classifies a type change. SQLite's CAST never fails: a value the
// new type cannot hold becomes another one, such as 0 for text that is not
// a number, so a cast that cannot keep every value is lossy rather than
// one that may fail.
func (sqliteDialect) convert(from, to string) conversion {
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
