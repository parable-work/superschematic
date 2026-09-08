package runtime

import (
	"encoding/json"
	"testing"

	"github.com/parable-work/superschematic/runtime/schema/go/ptr"
	"github.com/parable-work/superschematic/ir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSchema() *ir.Schema {
	s := ir.NewSchema("rt-test", ir.SchemaKindGeneral)

	s.Scalars["Email"] = &ir.ScalarDef{
		Name:               "Contact.Email",
		Primitive:          "String",
		Pattern:            `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`,
		HasCustomNormalize: true,
		HasCustomValidate:  true,
	}
	s.Scalars["Name"] = &ir.ScalarDef{
		Name:      "Name",
		Primitive: "String",
		MinLength: 2,
		MaxLength: 80,
	}
	s.Scalars["Money"] = &ir.ScalarDef{
		Name:      "Money",
		Primitive: "Int",
		Minimum:   ptr.To(int64(0)),
	}

	timeoutDefault := "30"
	s.Types["User"] = &ir.TypeDef{
		Name: "User",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "String"}},
			{Name: "name", TypeRef: ir.TypeRef{Name: "Name"}, Required: true},
			{Name: "email", TypeRef: ir.TypeRef{Name: "Email"}, Required: true},
			{Name: "amount", TypeRef: ir.TypeRef{Name: "Money"}},
			{Name: "timeoutSeconds", TypeRef: ir.TypeRef{Name: "Int"}, Default: &timeoutDefault},
			{Name: "password", TypeRef: ir.TypeRef{Name: "String"}, Secret: true},
			{Name: "tags", TypeRef: ir.TypeRef{Name: "String", IsArray: true}},
		},
	}

	s.Inputs["CreateUserInput"] = &ir.TypeDef{
		Name: "CreateUserInput",
		Kind: ir.TypeKindInput,
		Fields: []*ir.FieldDef{
			{Name: "name", TypeRef: ir.TypeRef{Name: "Name"}, Required: true},
			{Name: "email", TypeRef: ir.TypeRef{Name: "Email"}, Required: true},
			{Name: "password", TypeRef: ir.TypeRef{Name: "String"}, Secret: true},
		},
	}

	return s
}

func TestLoadType_HappyPath(t *testing.T) {
	rt := New(testSchema())
	data, errs := rt.LoadType("User", []byte(`{"name":"Alice","email":"ALICE@example.com","amount":42}`))
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, "Alice", data["name"])
	assert.Equal(t, "alice@example.com", data["email"], "email normalized")
	assert.Equal(t, int64(42), data["amount"], "Int coerced")
	assert.Equal(t, int64(30), data["timeoutSeconds"], "default applied")
}

func TestLoadType_ParseError(t *testing.T) {
	rt := New(testSchema())
	_, errs := rt.LoadType("User", []byte("not json"))
	require.True(t, errs.HasErrors())
}

func TestLoadType_ValidateError(t *testing.T) {
	rt := New(testSchema())
	_, errs := rt.LoadType("User", []byte(`{"name":"A","email":"alice@example.com"}`))
	require.True(t, errs.HasErrors(), "Name MinLength=2 should fail")
	v, ok := errs["name"]
	require.True(t, ok, "errs=%v", errs)
	fieldErrs := v.([]ValidationError)
	require.NotEmpty(t, fieldErrs)
}

func TestLoadType_MergesParseAndValidateErrors(t *testing.T) {
	rt := New(testSchema())
	_, errs := rt.LoadType("User", []byte(`{"email":"alice@example.com","amount":"not-a-number"}`))
	// "amount" coerces leniently from "not-a-number" -> fails in coerce, gets a parse-level type error.
	// "name" is missing required -> validate error.
	require.True(t, errs.HasErrors())
}

func TestLoadTypeStrict_RejectsUnknown(t *testing.T) {
	rt := New(testSchema())
	_, errs := rt.LoadTypeStrict("User", []byte(`{"name":"Alice","email":"alice@example.com","extra":"x"}`))
	require.True(t, errs.HasErrors())
	v, ok := errs["extra"]
	require.True(t, ok)
	fieldErrs := v.([]ValidationError)
	assert.Equal(t, "unknown_field", fieldErrs[0].Validator)
}

func TestLoadType_StrictOption(t *testing.T) {
	rt := New(testSchema(), WithStrict(true))
	_, errs := rt.LoadType("User", []byte(`{"name":"Alice","email":"alice@example.com","extra":"x"}`))
	require.True(t, errs.HasErrors())
}

func TestLoadTypeYAML(t *testing.T) {
	rt := New(testSchema())
	data, errs := rt.LoadTypeYAML("User", []byte("name: Bob\nemail: BOB@example.com\namount: 7\n"))
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, "bob@example.com", data["email"])
	assert.Equal(t, int64(7), data["amount"])
}

func TestLoadInput(t *testing.T) {
	rt := New(testSchema())
	data, errs := rt.LoadInput("CreateUserInput", []byte(`{"name":"Alice","email":"ALICE@example.com"}`))
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, "alice@example.com", data["email"])
}

func TestValidateType_Standalone(t *testing.T) {
	rt := New(testSchema())
	errs := rt.ValidateType("User", map[string]any{"email": "alice@example.com"})
	require.True(t, errs.HasErrors(), "missing required name")
}

func TestParseType_NoValidation(t *testing.T) {
	rt := New(testSchema())
	data, errs := rt.ParseType("User", []byte(`{"email":"ALICE@example.com"}`))
	// Parse does not enforce required, so missing name does NOT produce an error.
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, "alice@example.com", data["email"])
}

func TestMarshalType_NilSliceNormalization(t *testing.T) {
	rt := New(testSchema())
	b, errs := rt.MarshalType("User", map[string]any{
		"name":  "Alice",
		"email": "alice@example.com",
		"tags":  nil,
	})
	require.False(t, errs.HasErrors())
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, []any{}, out["tags"])
}

func TestMarshalType_NoMaskByDefault(t *testing.T) {
	rt := New(testSchema())
	out, errs := rt.TypeToMap("User", map[string]any{
		"name":     "Alice",
		"email":    "alice@example.com",
		"password": "secret",
	})
	require.False(t, errs.HasErrors())
	assert.Equal(t, "secret", out["password"])
}

func TestMarshalType_MaskSecrets(t *testing.T) {
	rt := New(testSchema(), WithMaskSecrets(true))
	out, errs := rt.TypeToMap("User", map[string]any{
		"name":     "Alice",
		"email":    "alice@example.com",
		"password": "secret",
	})
	require.False(t, errs.HasErrors())
	// password is optional in the schema; mask zeroes optional secrets to nil.
	assert.Contains(t, out, "password")
	assert.Nil(t, out["password"])
}

func TestRoundTrip(t *testing.T) {
	rt := New(testSchema())
	data, errs := rt.LoadType("User", []byte(`{"name":"Alice","email":"alice@example.com","amount":42,"tags":["a","b"]}`))
	require.False(t, errs.HasErrors())

	b, errs := rt.MarshalType("User", data)
	require.False(t, errs.HasErrors())

	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, "Alice", got["name"])
	assert.Equal(t, "alice@example.com", got["email"])
	assert.Equal(t, []any{"a", "b"}, got["tags"])
}

func TestMaskAndMerge(t *testing.T) {
	rt := New(testSchema())
	masked := rt.MaskType("User", map[string]any{
		"name":     "Alice",
		"password": "secret",
	})
	// password is optional in the schema; mask.MaskType zeroes optional
	// secret fields to nil (required string secrets zero to "").
	assert.Nil(t, masked["password"], "optional secret zeroed to nil")

	merged := rt.MergeType("User",
		map[string]any{"name": "Alice", "password": ""},
		map[string]any{"name": "Bob", "password": "existing"},
	)
	assert.Equal(t, "existing", merged["password"], "merge preserves existing secret when new is empty")
}

func TestPackageLevelLoadType(t *testing.T) {
	schema := testSchema()
	data, errs := LoadType(schema, "User", []byte(`{"name":"Alice","email":"alice@example.com"}`))
	require.False(t, errs.HasErrors())
	assert.Equal(t, "Alice", data["name"])
}

func TestPackageLevelMarshalType(t *testing.T) {
	schema := testSchema()
	b, errs := MarshalType(schema, "User", map[string]any{
		"name":  "Alice",
		"email": "alice@example.com",
	})
	require.False(t, errs.HasErrors())
	assert.Contains(t, string(b), `"Alice"`)
}

func TestMergeErrors(t *testing.T) {
	dst := NewValidationErrors()
	dst.AddFieldError("a", "v1", "msg1")

	src := NewValidationErrors()
	src.AddFieldError("a", "v2", "msg2")
	src.AddFieldError("b", "v3", "msg3")

	mergeErrors(dst, src)
	a := dst["a"].([]ValidationError)
	require.Len(t, a, 2)
	assert.Equal(t, "v1", a[0].Validator)
	assert.Equal(t, "v2", a[1].Validator)
	_, ok := dst["b"]
	assert.True(t, ok)
}

func TestStrictRegistryOptionPropagated(t *testing.T) {
	// Add a HasCustomValidate scalar with no registered validator. With
	// strict registry on, New should panic.
	s := testSchema()
	s.Scalars["Unregistered"] = &ir.ScalarDef{
		Name:              "Unregistered.Scalar",
		Primitive:         "String",
		HasCustomValidate: true,
	}
	assert.Panics(t, func() {
		New(s, WithStrictRegistry(true))
	})
}
