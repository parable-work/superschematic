package sqlutil

// How each dialect asks whether a row's effective audience holds one of a
// caller's ids (D156, D182), the containment a read of an @audience table
// keeps to: each a format of the effective audience column %s and the list
// of the ids' parameters %s.
//
// On Postgres the effective audience is a UUID[], which overlaps the ids'
// array (&&), a GIN index's to serve. On SQLite it is a JSON array of the
// ids' text in a TEXT column (sqlmigrate's sqliteType), which json_each
// reads; a parameter there is ?n (SQLiteClauses). A runtime writes the
// whole condition as (effective IS NULL OR <contains>), NULL being
// everyone. The TypeScript runtime's audienceConditions takes them from a
// table's definition, and the Go and Rust repositories on SQLite take the
// same (D182's shapes).
const (
	PostgresAudienceContains = "%s && ARRAY[%s]::uuid[]"
	SQLiteAudienceContains   = "EXISTS (SELECT 1 FROM json_each(%s) WHERE json_each.value IN (%s))"
	// SQLiteAudiencePlaceholder is a parameter numbered %d on SQLite.
	SQLiteAudiencePlaceholder = "?%d"
)
