package pgtest

import (
	"bytes"
	"strings"
	"testing"
)

// TestCreateSQL checks the preamble: the lock first, then each extension
// create.sql names, created in public, then create.sql unchanged.
func TestCreateSQL(t *testing.T) {
	createSQL := []byte(`-- Generated PostgreSQL DDL for schema: fixture-db

-- Enable required PostgreSQL extensions
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE "tenant" ("id" UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY);
`)
	got := CreateSQL(createSQL)
	if !bytes.HasSuffix(got, createSQL) {
		t.Fatalf("create.sql does not follow the preamble unchanged:\n%s", got)
	}
	preamble := got[:len(got)-len(createSQL)]
	want := strings.Join([]string{
		"SELECT pg_advisory_xact_lock(hashtextextended('superschematic test create.sql', 0));",
		"CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public;",
		"CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public;",
		"",
	}, "\n")
	if string(preamble) != want {
		t.Fatalf("preamble =\n%s\nwant\n%s", preamble, want)
	}
}
