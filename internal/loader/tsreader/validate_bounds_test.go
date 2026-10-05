package tsreader

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateFractionalBoundsFail: the JSON and YAML forms type the length
// and list bounds as integers, and the TypeScript form refuses each
// fractional bound by key and value instead of truncating maxLength: 2.5 to 2.
func TestValidateFractionalBoundsFail(t *testing.T) {
	_, _, err := LoadService(filepath.Join("testdata", "services", "broken-validate-fractional-bounds"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	msg := err.Error()
	for _, want := range []string{
		"Validate minLength must be a finite JavaScript-safe integer literal, got 0.5",
		"Validate maxLength must be a finite JavaScript-safe integer literal, got 2.5",
		"Validate listMin must be a finite JavaScript-safe integer literal, got 1.5",
		"Validate listMax must be a finite JavaScript-safe integer literal, got 3.25",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("want %q, got:\n%s", want, msg)
		}
	}
}
