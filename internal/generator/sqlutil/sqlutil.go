// Package sqlutil provides PostgreSQL identifier helpers shared by the
// SQL-targeting generators (sqlgen and ormgen).
package sqlutil

import (
	"fmt"
	"strings"
)

// PostgresReservedKeywords lists PostgreSQL reserved keywords that need to be
// quoted when used as identifiers.
// Source: https://www.postgresql.org/docs/current/sql-keywords-appendix.html
var PostgresReservedKeywords = map[string]bool{
	"all": true, "analyse": true, "analyze": true, "and": true, "any": true,
	"array": true, "as": true, "asc": true, "asymmetric": true, "authorization": true,
	"between": true, "binary": true, "both": true, "case": true, "cast": true,
	"check": true, "collate": true, "collation": true, "column": true, "concurrently": true,
	"constraint": true, "create": true, "cross": true, "current_catalog": true, "current_date": true,
	"current_role": true, "current_schema": true, "current_time": true, "current_timestamp": true, "current_user": true,
	"default": true, "deferrable": true, "desc": true, "distinct": true, "do": true,
	"else": true, "end": true, "except": true, "false": true, "fetch": true,
	"for": true, "foreign": true, "freeze": true, "from": true, "full": true,
	"grant": true, "group": true, "having": true, "ilike": true, "in": true,
	"initially": true, "inner": true, "intersect": true, "into": true, "is": true,
	"isnull": true, "join": true, "lateral": true, "leading": true, "left": true,
	"like": true, "limit": true, "localtime": true, "localtimestamp": true, "natural": true,
	"not": true, "notnull": true, "null": true, "offset": true, "on": true,
	"only": true, "or": true, "order": true, "outer": true, "overlaps": true,
	"placing": true, "primary": true, "references": true, "returning": true, "right": true,
	"select": true, "session_user": true, "similar": true, "some": true, "symmetric": true,
	"table": true, "tablesample": true, "then": true, "to": true, "trailing": true,
	"true": true, "union": true, "unique": true, "user": true, "using": true,
	"variadic": true, "verbose": true, "when": true, "where": true, "window": true,
	"with": true,
	// Common type-related reserved words
	"bigint": true, "bit": true, "boolean": true, "char": true, "character": true,
	"date": true, "decimal": true, "double": true, "float": true, "int": true,
	"integer": true, "interval": true, "numeric": true, "precision": true, "real": true,
	"smallint": true, "time": true, "timestamp": true, "varchar": true, "zone": true,
	// Other commonly problematic words
	"abort": true, "action": true, "add": true, "after": true, "aggregate": true,
	"cascade": true, "comment": true, "commit": true, "data": true, "database": true,
	"index": true, "key": true, "language": true, "lock": true, "match": true,
	"name": true, "new": true, "next": true, "nothing": true, "off": true,
	"old": true, "owner": true, "partition": true, "password": true, "procedure": true,
	"function": true, "rename": true, "replace": true, "reset": true, "restart": true,
	"restrict": true, "return": true, "rollback": true, "row": true, "rows": true,
	"schema": true, "sequence": true, "session": true, "set": true, "show": true,
	"sql": true, "start": true, "statistics": true, "transaction": true, "trigger": true,
	"truncate": true, "type": true, "update": true, "vacuum": true, "values": true,
	"view": true, "work": true, "year": true,
}

// QuoteIdentifier quotes a SQL identifier if it is a reserved keyword or
// contains characters that would require quoting. PostgreSQL folds unquoted
// identifiers to lowercase, so identifiers are only quoted when necessary.
func QuoteIdentifier(name string) string {
	lowerName := strings.ToLower(name)

	if PostgresReservedKeywords[lowerName] {
		return fmt.Sprintf(`"%s"`, name)
	}

	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
			return fmt.Sprintf(`"%s"`, name)
		}
	}

	return name
}
