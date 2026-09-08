package parse

import (
	"testing"

	"github.com/parable-work/superschematic/ir"
	"github.com/stretchr/testify/assert"
)

func TestApplyDefault_Int(t *testing.T) {
	def := "30"
	field := &ir.FieldDef{
		Name:    "timeoutSeconds",
		TypeRef: ir.TypeRef{Name: "Int"},
		Default: &def,
	}
	v, ok := applyDefault(field, nil)
	assert.True(t, ok)
	assert.Equal(t, int64(30), v)
}

func TestApplyDefault_Float(t *testing.T) {
	def := "1.5"
	field := &ir.FieldDef{
		Name:    "ratio",
		TypeRef: ir.TypeRef{Name: "Float"},
		Default: &def,
	}
	v, ok := applyDefault(field, nil)
	assert.True(t, ok)
	assert.InDelta(t, 1.5, v.(float64), 1e-9)
}

func TestApplyDefault_Bool(t *testing.T) {
	def := "true"
	field := &ir.FieldDef{
		Name:    "enabled",
		TypeRef: ir.TypeRef{Name: "Boolean"},
		Default: &def,
	}
	v, ok := applyDefault(field, nil)
	assert.True(t, ok)
	assert.Equal(t, true, v)
}

func TestApplyDefault_String(t *testing.T) {
	def := "hello"
	field := &ir.FieldDef{
		Name:    "greeting",
		TypeRef: ir.TypeRef{Name: "String"},
		Default: &def,
	}
	v, ok := applyDefault(field, nil)
	assert.True(t, ok)
	assert.Equal(t, "hello", v)
}

func TestApplyDefault_ScalarPrimitive(t *testing.T) {
	def := "42"
	field := &ir.FieldDef{
		Name:    "amount",
		TypeRef: ir.TypeRef{Name: "Money"},
		Default: &def,
	}
	scalar := &ir.ScalarDef{Name: "Money", Primitive: "Int"}
	v, ok := applyDefault(field, scalar)
	assert.True(t, ok)
	assert.Equal(t, int64(42), v)
}

func TestApplyDefault_EnumLikeString(t *testing.T) {
	def := "flatten_json"
	field := &ir.FieldDef{
		Name:    "type",
		TypeRef: ir.TypeRef{Name: "TransformStepTypeEnum"},
		Default: &def,
	}
	v, ok := applyDefault(field, nil)
	assert.True(t, ok)
	assert.Equal(t, "flatten_json", v)
}

func TestApplyDefault_MalformedInt(t *testing.T) {
	def := "not-a-number"
	field := &ir.FieldDef{
		Name:    "timeoutSeconds",
		TypeRef: ir.TypeRef{Name: "Int"},
		Default: &def,
	}
	_, ok := applyDefault(field, nil)
	assert.False(t, ok)
}

func TestApplyDefault_NilDefault(t *testing.T) {
	field := &ir.FieldDef{
		Name:    "foo",
		TypeRef: ir.TypeRef{Name: "String"},
		Default: nil,
	}
	_, ok := applyDefault(field, nil)
	assert.False(t, ok)
}
