package typegen

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

func TestFormatGoDefaultEmptyArray(t *testing.T) {
	got, ok := formatGoDefaultLiteral(
		"[]",
		codegen.DefaultLiteralEmptyArray,
		FieldInfo{GoType: "[]Widget"},
	)
	if !ok || got != "[]Widget{}" {
		t.Fatalf("formatGoDefaultLiteral(empty array) = (%q, %t)", got, ok)
	}
}
