package migrate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
)

// TestCanonicalJSONVectors: the runner's copy of ir.CanonicalJSON agrees
// with the compiler's on every vector in testdata/canonical.json, which the
// compiler's function wrote: key order at every depth, whitespace, <, > and
// & escaped, non-ASCII as UTF-8, U+2028 and U+2029 escaped, number literals
// kept, the last of two equal keys, and the inputs it refuses.
func TestCanonicalJSONVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "canonical.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name      string `json:"name"`
		Input     string `json:"input"`
		Canonical string `json:"canonical"`
		SHA256    string `json:"sha256"`
		Error     bool   `json:"error"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) < 20 {
		t.Fatalf("read %d vectors", len(vectors))
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			canonical, err := migrate.CanonicalJSON([]byte(v.Input))
			if v.Error {
				if err == nil {
					t.Fatalf("CanonicalJSON(%q) = %s, want an error", v.Input, canonical)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(canonical) != v.Canonical {
				t.Fatalf("CanonicalJSON(%q)\n got %s\nwant %s", v.Input, canonical, v.Canonical)
			}
			if hash := migrate.Hash(canonical); hash != v.SHA256 {
				t.Fatalf("Hash = %s, want %s", hash, v.SHA256)
			}
			again, err := migrate.CanonicalJSON(canonical)
			if err != nil || string(again) != v.Canonical {
				t.Fatalf("canonical JSON is not a fixed point: %s, %v", again, err)
			}
		})
	}
}

// TestCanonicalJSONInvalidUTF8: a byte that is not UTF-8 becomes U+FFFD,
// written as UTF-8, as encoding/json writes it.
func TestCanonicalJSONInvalidUTF8(t *testing.T) {
	canonical, err := migrate.CanonicalJSON([]byte{'"', 'a', 0xff, 'b', '"'})
	if err != nil {
		t.Fatal(err)
	}
	if want := "\"a\xef\xbf\xbdb\""; string(canonical) != want {
		t.Fatalf("got %s, want %s", canonical, want)
	}
}

// TestPlanHash: a plan's hash leaves out its hash member and nothing else,
// and does not depend on the plan's encoding.
func TestPlanHash(t *testing.T) {
	compact := `{"version":1,"hash":"anything","steps":[],"to":"b","from":"a"}`
	indented := "{\n  \"from\": \"a\",\n  \"steps\": [ ],\n  \"to\": \"b\",\n  \"version\": 1\n}\n"
	want := migrate.Hash([]byte(`{"from":"a","steps":[],"to":"b","version":1}`))
	for _, doc := range []string{compact, indented} {
		hash, err := migrate.PlanHash([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		if hash != want {
			t.Fatalf("PlanHash(%q) = %s, want %s", doc, hash, want)
		}
	}
	if _, err := migrate.PlanHash([]byte(`[1]`)); err == nil {
		t.Fatal("PlanHash of an array: no error")
	}
}

// TestLockKey: the advisory lock key is the first eight bytes, big-endian,
// of the SHA-256 of superschematic_migrate:<service>, read as a signed
// 64-bit integer (computed with shasum).
func TestLockKey(t *testing.T) {
	for service, want := range map[string]int64{
		"shop":    -793857064436278117, // f4fba734b7d75c9b...
		"billing": 2903723579480470513, // 284c193600dbf3f1...
	} {
		if got := migrate.LockKey(service); got != want {
			t.Errorf("LockKey(%q) = %d, want %d", service, got, want)
		}
	}
}

// TestURLDialect: postgres:// and postgresql:// select Postgres; anything
// else is SQLite.
func TestURLDialect(t *testing.T) {
	for url, want := range map[string]migrate.Dialect{
		"postgres://u:p@h:5432/db?sslmode=disable": migrate.Postgres,
		"postgresql://h/db":                        migrate.Postgres,
		"POSTGRES://h/db":                          migrate.Postgres,
		"sqlite:///tmp/app.db":                     migrate.SQLite,
		"sqlite:app.db":                            migrate.SQLite,
		"file:app.db?mode=rwc":                     migrate.SQLite,
		"/var/lib/app.db":                          migrate.SQLite,
		"app.db":                                   migrate.SQLite,
	} {
		if got := migrate.URLDialect(url); got != want {
			t.Errorf("URLDialect(%q) = %s, want %s", url, got, want)
		}
	}
}

func TestStepErrorNamesTheStep(t *testing.T) {
	err := &migrate.StepError{Index: 3, Subject: "table/order/column/total", Statement: "ALTER TABLE x", Err: os.ErrClosed}
	for _, want := range []string{"step 3", "table/order/column/total", "ALTER TABLE x", os.ErrClosed.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not name %q", err.Error(), want)
		}
	}
}
