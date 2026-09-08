package pygen

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

func TestPythonFieldDefaultExprEmptyArray(t *testing.T) {
	value := "[]"
	got, ok := pythonFieldDefaultExpr(codegen.FieldInfo{
		Type:     "Widget",
		IsArray:  true,
		Required: true,
		Default:  &value,
	}, nil)
	if !ok || got != "[]" {
		t.Fatalf("pythonFieldDefaultExpr(empty array) = (%q, %t)", got, ok)
	}
}
