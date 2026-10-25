package writer

import (
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestDDLRoundTrips: fixture-ddl-db's @index options, its @rawSql pieces,
// multi-line SQL among them, and its SET NULL relations are written to
// TypeScript, JSON and YAML and read back to the same IR (D81).
func TestDDLRoundTrips(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-ddl-db")
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
