package sqlutil_test

import (
	"testing"

	. "github.com/parable-work/superschematic/internal/generator/sqlutil"
)

// TestUpdatedAtStampsTheDatabaseClock holds an update's updatedAt to the
// database's clock, the one an insert's default stamps createdAt and
// updatedAt with: on Postgres the statement's time, on SQLite its 'now' in
// the default's form. The assignment binds no host time, so no update
// stamps a row earlier than its insert when the host's clock runs behind
// the database's.
func TestUpdatedAtStampsTheDatabaseClock(t *testing.T) {
	delivery := fencedTables(t)["Delivery"]
	for _, tc := range []struct {
		dialect string
		s       TableStatements
		want    string
		filled  string
	}{
		{"postgres", TableStatementsOf(delivery), `updated_at = statement_timestamp()`, `updated_at = statement_timestamp()`},
		{"sqlite", SQLiteTableStatementsOf(delivery), `updated_at = strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', 'now')`, `updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`},
	} {
		if tc.s.UpdatedAt != tc.want {
			t.Errorf("%s's updatedAt = %q, want %q", tc.dialect, tc.s.UpdatedAt, tc.want)
		}
		if err := CheckFormat("updatedAt", tc.s.UpdatedAt); err != nil {
			t.Errorf("%s: %v", tc.dialect, err)
		}
		if got := Filled(tc.s.UpdatedAt); got != tc.filled {
			t.Errorf("%s's updatedAt filled = %q, want %q", tc.dialect, got, tc.filled)
		}
	}
}
