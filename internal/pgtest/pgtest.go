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

// VectorDatabaseEnv names the Postgres a test whose create.sql has a
// vector(n) column runs on (D84): one with the pgvector extension
// available, which the other test databases need not have. CI runs a
// pgvector image beside the postgres:<version>-alpine one the rest share,
// whose tzdata the version-graph vectors hold to.
const VectorDatabaseEnv = "SUPERSCHEMATIC_VECTOR_TEST_DATABASE_URL"

// RequireVectorEnv, set to 1, makes a vector test that finds no
// VectorDatabaseEnv fail rather than skip, as CI sets it.
const RequireVectorEnv = "SUPERSCHEMATIC_REQUIRE_VECTOR_TESTS"

// VectorDatabaseURL returns the URL VectorDatabaseEnv names. Without one it
// skips the test, or fails it under RequireVectorEnv.
func VectorDatabaseURL(t testing.TB) string {
	t.Helper()
	url := os.Getenv(VectorDatabaseEnv)
	if url == "" {
		if os.Getenv(RequireVectorEnv) == "1" {
			t.Fatalf("%s is set, so %s must name a Postgres with the pgvector extension", RequireVectorEnv, VectorDatabaseEnv)
		}
		t.Skipf("set %s to a Postgres with the pgvector extension to run the vector columns against it", VectorDatabaseEnv)
	}
	return url
}
