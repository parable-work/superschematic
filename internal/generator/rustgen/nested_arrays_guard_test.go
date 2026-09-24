package rustgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestNestedArraysGuard: rustgen refuses arrays of arrays (T[][]) until it
// renders them, rather than emitting T[]. The change that teaches rustgen
// T[][] deletes its nested-arrays guard and replaces this test with output
// for the fixture-nested-arrays services.
func TestNestedArraysGuard(t *testing.T) {
	for _, svc := range []string{"fixture-nested-arrays", "fixture-nested-arrays-db", "fixture-nested-arrays-api"} {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, svc))
		if err != nil {
			t.Fatalf("load %s: %v", svc, err)
		}
		_, err = Generate(schema, Options{SchemaName: svc})
		if err == nil || !strings.HasPrefix(err.Error(), "rustgen does not support arrays of arrays yet (") {
			t.Fatalf("%s: err = %v", svc, err)
		}
	}
}
