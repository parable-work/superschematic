package parse

import (
	"testing"

	"github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/runtime/schema/go/ptr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testParserSchema() *ir.Schema {
	s := ir.NewSchema("parse-test", ir.SchemaKindGeneral)

	s.Scalars["Email"] = &ir.ScalarDef{
		Name:               "Contact.Email",
		Primitive:          "String",
		HasCustomNormalize: true,
		HasCustomValidate:  true,
		HasCustomParse:     true,
	}
	s.Scalars["DateTime"] = &ir.ScalarDef{
		Name:           "Temporal.DateTime",
		Primitive:      "String",
		HasCustomParse: true,
	}
	s.Scalars["Money"] = &ir.ScalarDef{
		Name:      "Money",
		Primitive: "Int",
		Minimum:   ptr.To(int64(0)),
	}
	s.Scalars["Seconds"] = &ir.ScalarDef{
		Name:           "Temporal.Seconds",
		Primitive:      "Int",
		HasCustomParse: true,
	}
	s.Scalars["Ratio"] = &ir.ScalarDef{
		Name:      "Ratio",
		Primitive: "Float",
	}

	s.Enums["Status"] = &ir.EnumDef{
		Name: "Status",
		Values: []ir.EnumValueDef{
			{Name: "ACTIVE", SerializedAs: "active"},
		},
	}

	timeoutDefault := "30"
	enabledDefault := "true"
	statusDefault := "active"

	s.Types["User"] = &ir.TypeDef{
		Name: "User",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "String"}},
			{Name: "email", TypeRef: ir.TypeRef{Name: "Email"}, Required: true},
			{Name: "joinedAt", TypeRef: ir.TypeRef{Name: "DateTime"}},
			{Name: "amount", TypeRef: ir.TypeRef{Name: "Money"}},
			{Name: "durationSeconds", TypeRef: ir.TypeRef{Name: "Seconds"}},
			{Name: "ratio", TypeRef: ir.TypeRef{Name: "Ratio"}},
			{Name: "enabled", TypeRef: ir.TypeRef{Name: "Boolean"}, Default: &enabledDefault},
			{Name: "timeoutSeconds", TypeRef: ir.TypeRef{Name: "Int"}, Default: &timeoutDefault},
			{Name: "status", TypeRef: ir.TypeRef{Name: "Status"}, Default: &statusDefault},
			{Name: "tags", TypeRef: ir.TypeRef{Name: "String", IsArray: true}},
		},
	}

	s.Types["Address"] = &ir.TypeDef{
		Name: "Address",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "street", TypeRef: ir.TypeRef{Name: "String"}, Required: true},
			{Name: "city", TypeRef: ir.TypeRef{Name: "String"}, Required: true},
		},
	}

	s.Types["Company"] = &ir.TypeDef{
		Name: "Company",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "name", TypeRef: ir.TypeRef{Name: "String"}, Required: true},
			{Name: "owner", TypeRef: ir.TypeRef{Name: "User"}},
			{Name: "addresses", TypeRef: ir.TypeRef{Name: "Address", IsArray: true, ElemNonNull: true}},
		},
	}

	s.Inputs["CreateUserInput"] = &ir.TypeDef{
		Name: "CreateUserInput",
		Kind: ir.TypeKindInput,
		Fields: []*ir.FieldDef{
			{Name: "email", TypeRef: ir.TypeRef{Name: "Email"}, Required: true},
			{Name: "timeoutSeconds", TypeRef: ir.TypeRef{Name: "Int"}, Default: &timeoutDefault},
		},
	}

	return s
}

func TestParseType_CustomIntegerParserPreservesIntegerTypeAndBounds(t *testing.T) {
	p := newTestParser(t)

	out, errs := p.ParseType("User", map[string]any{"durationSeconds": float64(-12)})
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, int64(-12), out["durationSeconds"])

	out, errs = p.ParseType("User", map[string]any{"durationSeconds": float64(9007199254740992)})
	require.True(t, errs.HasErrors())
	assert.Equal(t, int64(9007199254740992), out["durationSeconds"])
	assert.Equal(t, "parse", errs.GetFieldErrors("durationSeconds")[0].Validator)
}

func newTestParser(t *testing.T, opts ...Option) *Parser {
	t.Helper()
	defaults := []Option{
		WithParseRegistry(DefaultParseRegistry()),
		WithNormalizeRegistry(DefaultNormalizeRegistry()),
	}
	defaults = append(defaults, opts...)
	return New(testParserSchema(), defaults...)
}

func TestParseType_RoundTrip(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"id":      "abc",
		"email":   "Foo@Example.COM",
		"amount":  float64(42),
		"ratio":   float64(0.5),
		"enabled": true,
	}
	out, errs := p.ParseType("User", raw)
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, "abc", out["id"])
	assert.Equal(t, "foo@example.com", out["email"], "email normalized to lowercase")
	assert.Equal(t, int64(42), out["amount"], "float64 coerced to int64")
	assert.InDelta(t, 0.5, out["ratio"].(float64), 1e-9)
	assert.Equal(t, true, out["enabled"])
}

func TestParseType_AppliesDefaults(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"email": "x@y.com",
	}
	out, errs := p.ParseType("User", raw)
	require.False(t, errs.HasErrors())
	assert.Equal(t, int64(30), out["timeoutSeconds"])
	assert.Equal(t, true, out["enabled"])
	assert.Equal(t, "active", out["status"])
}

func TestParseType_DefaultsSkippedWhenPresent(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"email":          "x@y.com",
		"timeoutSeconds": float64(60),
		"enabled":        false,
	}
	out, errs := p.ParseType("User", raw)
	require.False(t, errs.HasErrors())
	assert.Equal(t, int64(60), out["timeoutSeconds"])
	assert.Equal(t, false, out["enabled"])
}

func TestParseType_NormalizeEmail(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"email": "  USER@EXAMPLE.COM  ",
	}
	out, errs := p.ParseType("User", raw)
	require.False(t, errs.HasErrors())
	assert.Equal(t, "user@example.com", out["email"])
}

func TestParseType_DateTimeCanonicalization(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"email":    "a@b.com",
		"joinedAt": "2024-01-15T10:30:00Z",
	}
	out, errs := p.ParseType("User", raw)
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, "2024-01-15T10:30:00Z", out["joinedAt"])
}

func TestParseType_DateTimeInvalid(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"email":    "a@b.com",
		"joinedAt": "not-a-date",
	}
	_, errs := p.ParseType("User", raw)
	require.True(t, errs.HasErrors())
	v, ok := errs["joinedAt"]
	require.True(t, ok, "expected error at joinedAt key, got: %v", errs)
	fieldErrs, ok := v.([]ValidationError)
	require.True(t, ok, "expected []ValidationError, got %T", v)
	require.NotEmpty(t, fieldErrs)
	assert.Equal(t, "parse", fieldErrs[0].Validator)
}

func TestParseType_NestedType(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"name": "Acme",
		"owner": map[string]any{
			"email": "OWNER@ACME.COM",
		},
		"addresses": []any{
			map[string]any{"street": "1 Main", "city": "NYC"},
			map[string]any{"street": "2 Oak", "city": "LA"},
		},
	}
	out, errs := p.ParseType("Company", raw)
	require.False(t, errs.HasErrors(), "errs=%v", errs)

	owner := out["owner"].(map[string]any)
	assert.Equal(t, "owner@acme.com", owner["email"])
	assert.Equal(t, int64(30), owner["timeoutSeconds"], "nested defaults applied")

	addresses := out["addresses"].([]any)
	require.Len(t, addresses, 2)
	assert.Equal(t, "1 Main", addresses[0].(map[string]any)["street"])
}

func TestParseType_LenientKeepsUnknown(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"email": "a@b.com",
		"extra": "kept",
	}
	_, errs := p.ParseType("User", raw)
	assert.False(t, errs.HasErrors(), "lenient mode tolerates unknown fields")
}

func TestParseType_StrictRejectsUnknown(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"email": "a@b.com",
		"extra": "rejected",
	}
	_, errs := p.ParseTypeStrict("User", raw)
	require.True(t, errs.HasErrors())
	v, ok := errs["extra"]
	require.True(t, ok)
	fieldErrs := v.([]ValidationError)
	require.NotEmpty(t, fieldErrs)
	assert.Equal(t, "unknown_field", fieldErrs[0].Validator)
}

func TestParseType_StrictRejectsCoercion(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"email":  "a@b.com",
		"amount": "42",
	}
	_, errs := p.ParseTypeStrict("User", raw)
	require.True(t, errs.HasErrors())
	v, ok := errs["amount"]
	require.True(t, ok)
	fieldErrs := v.([]ValidationError)
	require.NotEmpty(t, fieldErrs)
	assert.Equal(t, "type", fieldErrs[0].Validator)
}

func TestParseType_LenientCoercesStringInt(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"email":  "a@b.com",
		"amount": "42",
	}
	out, errs := p.ParseType("User", raw)
	require.False(t, errs.HasErrors())
	assert.Equal(t, int64(42), out["amount"])
}

func TestParseInput(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"email": "USER@example.com",
	}
	out, errs := p.ParseInput("CreateUserInput", raw)
	require.False(t, errs.HasErrors())
	assert.Equal(t, "user@example.com", out["email"])
	assert.Equal(t, int64(30), out["timeoutSeconds"], "input default applied")
}

func TestParseTypeJSON(t *testing.T) {
	p := newTestParser(t)
	out, errs := p.ParseTypeJSON("User", []byte(`{"email":"Foo@Example.com","amount":42}`))
	require.False(t, errs.HasErrors())
	assert.Equal(t, "foo@example.com", out["email"])
	assert.Equal(t, int64(42), out["amount"])
	assert.Equal(t, int64(30), out["timeoutSeconds"], "default applied")
}

func TestParseTypeJSON_BadInput(t *testing.T) {
	p := newTestParser(t)
	_, errs := p.ParseTypeJSON("User", []byte("{ this is not json"))
	require.True(t, errs.HasErrors())
	v, ok := errs[""]
	require.True(t, ok)
	fieldErrs := v.([]ValidationError)
	require.NotEmpty(t, fieldErrs)
	assert.Equal(t, "json", fieldErrs[0].Validator)
}

func TestParseTypeYAML(t *testing.T) {
	p := newTestParser(t)
	out, errs := p.ParseTypeYAML("User", []byte("email: A@B.com\namount: 7\n"))
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, "a@b.com", out["email"])
	assert.Equal(t, int64(7), out["amount"])
}

func TestParseType_UnknownType(t *testing.T) {
	p := newTestParser(t)
	_, errs := p.ParseType("Nope", nil)
	require.True(t, errs.HasErrors())
}

func TestParseType_NilRaw(t *testing.T) {
	p := newTestParser(t)
	out, errs := p.ParseType("User", nil)
	// No errors expected; defaults still apply.
	require.False(t, errs.HasErrors())
	assert.Equal(t, int64(30), out["timeoutSeconds"])
}

func TestParseType_DefaultPrimitiveMismatchEmitsDefaultError(t *testing.T) {
	badDefault := "not-a-number"
	s := testParserSchema()
	s.Types["User"].Fields = append(s.Types["User"].Fields,
		&ir.FieldDef{Name: "broken", TypeRef: ir.TypeRef{Name: "Int"}, Default: &badDefault},
	)
	p := New(s, WithNormalizeRegistry(DefaultNormalizeRegistry()), WithParseRegistry(DefaultParseRegistry()))
	raw := map[string]any{"email": "x@y.com"}
	_, errs := p.ParseType("User", raw)
	require.True(t, errs.HasErrors())
	v, ok := errs["broken"]
	require.True(t, ok)
	fieldErrs := v.([]ValidationError)
	assert.Equal(t, "default", fieldErrs[0].Validator)
}

func TestParseType_StrictOptionMakesFromMapStrict(t *testing.T) {
	p := newTestParser(t, WithStrict(true))
	raw := map[string]any{
		"email": "a@b.com",
		"extra": "rejected",
	}
	_, errs := p.ParseType("User", raw)
	require.True(t, errs.HasErrors())
	_, ok := errs["extra"]
	assert.True(t, ok)
}

func TestParseType_DoesNotMutateInput(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"email": "X@Y.com",
	}
	rawBefore := map[string]any{"email": "X@Y.com"}
	_, _ = p.ParseType("User", raw)
	assert.Equal(t, rawBefore, raw, "raw input must not be mutated")
}

func TestParseType_RecursesIntoNestedDefaults(t *testing.T) {
	p := newTestParser(t)
	raw := map[string]any{
		"name":  "Acme",
		"owner": map[string]any{"email": "a@b.com"},
	}
	out, errs := p.ParseType("Company", raw)
	require.False(t, errs.HasErrors())
	owner := out["owner"].(map[string]any)
	assert.Equal(t, int64(30), owner["timeoutSeconds"])
}

// TestParseType_AnyJSONScalar: a scalar whose json_schema type mapping is
// "any" keeps its value as it is, whatever its String primitive, in the
// lenient and the strict parse.
func TestParseType_AnyJSONScalar(t *testing.T) {
	s := ir.NewSchema("parse-test", ir.SchemaKindGeneral)
	s.Scalars["Blob"] = &ir.ScalarDef{
		Name:         "Blob",
		Primitive:    "String",
		TypeMappings: map[string]string{"json_schema": "any"},
	}
	s.Types["Doc"] = &ir.TypeDef{
		Name: "Doc",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "body", TypeRef: ir.TypeRef{Name: "Blob"}, Required: true},
			{Name: "parts", TypeRef: ir.TypeRef{Name: "Blob", IsArray: true}},
		},
	}
	raw := map[string]any{
		"body":  map[string]any{"k": []any{1.0, nil}},
		"parts": []any{[]any{1.0}, "s", 2.0, true},
	}
	for _, strict := range []bool{false, true} {
		p := New(s, WithStrict(strict))
		out, errs := p.ParseType("Doc", raw)
		require.False(t, errs.HasErrors(), "strict=%v errs=%v", strict, errs)
		assert.Equal(t, raw["body"], out["body"])
		assert.Equal(t, raw["parts"], out["parts"])
	}
}
