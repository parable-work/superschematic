// Package pgtest prepares a generated create.sql for the Postgres-backed
// tests. They share one database: CI points every test database variable at
// the same Postgres and runs the test packages in parallel, each test in a
// schema of its own.
package pgtest

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"testing"
)

// createExtension matches the statements create.sql opens with (sqlgen's
// templates/create.tmpl).
var createExtension = regexp.MustCompile(`(?m)^CREATE EXTENSION IF NOT EXISTS (\w+);$`)

// CreateSQL returns createSQL as a test applies it to the shared database:
// behind a preamble that takes a transaction-scoped advisory lock and
// creates each extension createSQL names in public. What follows the
// preamble is createSQL byte for byte.
//
// An extension belongs to the database, not to a schema of one test. Two
// sessions creating one at once race on pg_extension_name_index, and one
// created without a schema lands in its creator's schema: another test's
// search path does not find its types, and the creator's DROP SCHEMA ...
// CASCADE takes it away. Created in public, every schema,public search
// path finds it and create.sql's own CREATE EXTENSION IF NOT EXISTS skips.
//
// Every test sends create.sql as one query string, which Postgres runs as
// one transaction, so the lock is held until create.sql commits and the
// next test's preamble finds the extensions already there.
func CreateSQL(createSQL []byte) []byte {
	var b bytes.Buffer
	b.WriteString("SELECT pg_advisory_xact_lock(hashtextextended('superschematic test create.sql', 0));\n")
	for _, m := range createExtension.FindAllSubmatch(createSQL, -1) {
		fmt.Fprintf(&b, "CREATE EXTENSION IF NOT EXISTS %s SCHEMA public;\n", m[1])
	}
	b.Write(createSQL)
	return b.Bytes()
}

// WriteCreateSQL writes CreateSQL of the create.sql at src to dst, which
// may be src.
func WriteCreateSQL(t testing.TB, src, dst string) {
	t.Helper()
	createSQL, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("pgtest: read create.sql: %v", err)
	}
	if err := os.WriteFile(dst, CreateSQL(createSQL), 0o644); err != nil {
		t.Fatalf("pgtest: write create.sql: %v", err)
	}
}
