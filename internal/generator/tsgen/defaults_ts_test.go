package tsgen

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

func TestFormatTSDefaultEmptyArray(t *testing.T) {
	got, ok := formatTSDefaultLiteral(
		"[]",
		codegen.DefaultLiteralEmptyArray,
		FieldInfo{Type: "Widget[]"},
	)
	if !ok || got != "[]" {
		t.Fatalf("formatTSDefaultLiteral(empty array) = (%q, %t)", got, ok)
	}
}
