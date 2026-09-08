package serialize

import (
	"encoding/json"
	"testing"

	"github.com/parable-work/superschematic/ir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSchema() *ir.Schema {
	s := ir.NewSchema("ser-test", ir.SchemaKindGeneral)
	s.Scalars["Email"] = &ir.ScalarDef{Name: "Email", Primitive: "String"}
	s.Scalars["Name"] = &ir.ScalarDef{Name: "Name", Primitive: "String"}

	s.Types["Address"] = &ir.TypeDef{
		Name: "Address",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "street", TypeRef: ir.TypeRef{Name: "String"}},
			{Name: "city", TypeRef: ir.TypeRef{Name: "String"}},
		},
	}

	s.Types["User"] = &ir.TypeDef{
		Name: "User",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "String"}, JSONTag: "id"},
			{Name: "email", TypeRef: ir.TypeRef{Name: "Email"}},
			{Name: "password", TypeRef: ir.TypeRef{Name: "String"}, Secret: true},
			{Name: "tags", TypeRef: ir.TypeRef{Name: "String", IsArray: true}},
			{Name: "address", TypeRef: ir.TypeRef{Name: "Address"}},
		},
	}
	return s
}

func TestTypeToMap_DropsUnknown(t *testing.T) {
	s := New(testSchema())
	in := map[string]any{
		"id":    "abc",
		"email": "x@y.com",
		"extra": "dropped",
	}
	out, errs := s.TypeToMap("User", in)
	require.False(t, errs.HasErrors())
	assert.Equal(t, "abc", out["id"])
	assert.NotContains(t, out, "extra")
}

func TestTypeToMap_NormalizesNilSlice(t *testing.T) {
	s := New(testSchema())
	in := map[string]any{
		"id":    "abc",
		"email": "x@y.com",
		"tags":  nil,
	}
	out, errs := s.TypeToMap("User", in)
	require.False(t, errs.HasErrors())
	tags, ok := out["tags"].([]any)
	require.True(t, ok)
	assert.Equal(t, []any{}, tags)
}

func TestTypeToMap_MaskSecrets(t *testing.T) {
	s := New(testSchema(), WithMaskSecrets(true))
	in := map[string]any{
		"id":       "abc",
		"password": "supersecret",
	}
	out, errs := s.TypeToMap("User", in)
	require.False(t, errs.HasErrors())
	// password is optional in the schema; mask.MaskType zeroes optional
	// secret fields to nil (the mask package's contract). The result map
	// still has the key (set explicitly to nil).
	assert.Contains(t, out, "password")
	assert.Nil(t, out["password"])
}

func TestTypeToMap_NoMaskByDefault(t *testing.T) {
	s := New(testSchema())
	in := map[string]any{
		"id":       "abc",
		"password": "supersecret",
	}
	out, errs := s.TypeToMap("User", in)
	require.False(t, errs.HasErrors())
	assert.Equal(t, "supersecret", out["password"])
}

func TestTypeToMap_NestedType(t *testing.T) {
	s := New(testSchema())
	in := map[string]any{
		"id":    "abc",
		"email": "x@y.com",
		"address": map[string]any{
			"street": "1 Main",
			"city":   "NYC",
			"extra":  "dropped",
		},
	}
	out, errs := s.TypeToMap("User", in)
	require.False(t, errs.HasErrors())
	addr := out["address"].(map[string]any)
	assert.Equal(t, "1 Main", addr["street"])
	assert.NotContains(t, addr, "extra")
}

func TestTypeToMap_StrictRejectsUnknown(t *testing.T) {
	s := New(testSchema(), WithStrict(true))
	in := map[string]any{
		"id":    "abc",
		"extra": "rejected",
	}
	_, errs := s.TypeToMap("User", in)
	require.True(t, errs.HasErrors())
	_, ok := errs["extra"]
	assert.True(t, ok)
}

func TestTypeToJSON(t *testing.T) {
	s := New(testSchema())
	in := map[string]any{
		"id":    "abc",
		"email": "x@y.com",
		"tags":  nil,
	}
	b, errs := s.TypeToJSON("User", in)
	require.False(t, errs.HasErrors())

	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, []any{}, got["tags"], "nil slices serialize as []")
}

func TestUnknownType(t *testing.T) {
	s := New(testSchema())
	_, errs := s.TypeToMap("Nope", nil)
	assert.True(t, errs.HasErrors())
}

func TestTypeToJSON_SchemaFieldOrder(t *testing.T) {
	s := New(testSchema())
	// Insert into the map in reverse declaration order; output must still
	// follow the schema's declared field order (id, email, password, tags,
	// address).
	in := map[string]any{
		"address": map[string]any{"street": "1 Main", "city": "NYC"},
		"tags":    []any{"a", "b"},
		"email":   "x@y.com",
		"id":      "abc",
	}
	b, errs := s.TypeToJSON("User", in)
	require.False(t, errs.HasErrors())
	got := string(b)
	expected := `{"id":"abc","email":"x@y.com","tags":["a","b"],"address":{"street":"1 Main","city":"NYC"}}`
	assert.Equal(t, expected, got)
}
