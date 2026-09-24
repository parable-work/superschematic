package rustrestgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestNestedArraysGuard: rustrestgen refuses arrays of arrays (T[][]) until it
// renders them, rather than emitting T[]. The change that teaches rustrestgen
// T[][] deletes its nested-arrays guard and replaces this test with output
// for the fixture-nested-arrays services.
func TestNestedArraysGuard(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-nested-arrays-api"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Generate(schema, Options{AuthProvider: sessionauth.Provider{}, SchemaName: "fixture-nested-arrays-api"})
	if err == nil || !strings.HasPrefix(err.Error(), "rustrestgen does not support arrays of arrays yet (") {
		t.Fatalf("err = %v", err)
	}
}
