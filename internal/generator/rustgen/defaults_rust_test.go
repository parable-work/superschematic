package rustgen

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

func TestFormatRustDefaultEmptyArray(t *testing.T) {
	got, ok := formatRustDefaultLiteral(
		"[]",
		codegen.DefaultLiteralEmptyArray,
		FieldInfo{RustType: "Vec<Widget>", Required: true},
		nil,
	)
	if !ok || got != "Vec::new()" {
		t.Fatalf("formatRustDefaultLiteral(empty array) = (%q, %t)", got, ok)
	}
}
