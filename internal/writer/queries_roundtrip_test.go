package writer

import (
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestQueriesRoundTrip: fixture-queries-db's @query declarations, with
// multi-line SQL, an order, a page, notNull and dialects, and their params
// and row types, classes and an object type alias alike, are written to
// TypeScript, JSON and YAML and read back to the same IR (D86). A class
// @query names reads back as the embedded struct it became, an object type
// alias in TypeScript.
func TestQueriesRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-queries-db")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	want := normalizeIR(t, schema)
	for _, target := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(target), func(t *testing.T) {
			written := schema
			if target == FormatTS {
				written = writableAsTS(t, schema)
			}
			if got := normalizeIR(t, writeAndReload(t, written, target)); got != want {
				t.Errorf("IR mismatch after TS -> %s round trip\nwant:\n%s\ngot:\n%s", target, want, got)
			}
		})
	}
}
