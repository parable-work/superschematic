package runtime

import (
	"testing"

	"github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/runtime/schema/go/parse"
	"github.com/parable-work/superschematic/runtime/schema/go/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefault_IsTheScalarLibRegistrySet(t *testing.T) {
	reg := Default()
	require.NotNil(t, reg.Parse)
	require.NotNil(t, reg.Normalize)
	require.NotNil(t, reg.Validate)
	assert.Equal(t, parse.DefaultParseRegistry().Names(), reg.Parse.Names())
	assert.Equal(t, parse.DefaultNormalizeRegistry().Names(), reg.Normalize.Names())
	assert.Equal(t, validate.DefaultRegistry().Names(), reg.Validate.Names())
	assert.NotEmpty(t, reg.Validate.Names())
}

func TestNewRegistry_IsEmpty(t *testing.T) {
	reg := NewRegistry()
	assert.Equal(t, 0, reg.Parse.Len())
	assert.Equal(t, 0, reg.Normalize.Len())
	assert.Equal(t, 0, reg.Validate.Len())
}

// New without a registry option behaves as New with Default(): the email
// is normalized and custom-validated.
func TestNew_DefaultRegistryMatchesExplicitDefault(t *testing.T) {
	implicit := New(testSchema())
	explicit := New(testSchema(), WithRegistry(Default()))
	input := []byte(`{"name":"Alice","email":"ALICE@example.com"}`)

	got, errs := implicit.LoadType("User", input)
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	want, errs := explicit.LoadType("User", input)
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, want, got)
	assert.Equal(t, "alice@example.com", got["email"])
}

// An injected empty registry drops the superscalar behaviour: the email is
// no longer normalized and the custom validator is not consulted.
func TestNew_WithEmptyRegistry(t *testing.T) {
	rt := New(testSchema(), WithRegistry(NewRegistry()))
	data, errs := rt.LoadType("User", []byte(`{"name":"Alice","email":"ALICE@example.com"}`))
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, "ALICE@example.com", data["email"], "no normalizer registered")
}

// A registry carrying a validator for a scalar name superscalar does not
// know proves the injected functions are the ones consulted. (Names
// superscalar knows are delegated to it directly by the validate package.)
func TestNew_WithCustomRegistry(t *testing.T) {
	schema := testSchema()
	schema.Scalars["Shelf"] = &ir.ScalarDef{Name: "Acme.Shelf", Primitive: "String", HasCustomValidate: true}
	schema.Types["User"].Fields = append(schema.Types["User"].Fields,
		&ir.FieldDef{Name: "shelf", TypeRef: ir.TypeRef{Name: "Shelf"}})

	reg := NewRegistry()
	reg.Validate.Register("Acme.Shelf", func(string) []ValidationError {
		return []ValidationError{{Validator: "custom", Message: "rejected"}}
	})
	rt := New(schema, WithRegistry(reg))
	_, errs := rt.LoadType("User", []byte(`{"name":"Alice","email":"alice@example.com","shelf":"A3"}`))
	require.True(t, errs.HasErrors())
	fieldErrs, ok := errs["shelf"].([]ValidationError)
	require.True(t, ok, "errs=%v", errs)
	assert.Equal(t, "custom", fieldErrs[0].Validator)

	// The same schema against Default() has no Acme.Shelf validator and
	// accepts the value.
	_, errs = New(schema).LoadType("User", []byte(`{"name":"Alice","email":"alice@example.com","shelf":"A3"}`))
	require.False(t, errs.HasErrors(), "errs=%v", errs)
}

// The per-registry options still apply on top of WithRegistry, in option
// order, so existing callers keep their behaviour.
func TestNew_PerRegistryOptionOverridesBundle(t *testing.T) {
	rt := New(testSchema(), WithRegistry(NewRegistry()), WithNormalizeRegistry(parse.DefaultNormalizeRegistry()))
	data, errs := rt.LoadType("User", []byte(`{"name":"Alice","email":"ALICE@example.com"}`))
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, "alice@example.com", data["email"])
}

func TestNew_StrictRegistryPanicsOnEmptyRegistry(t *testing.T) {
	assert.Panics(t, func() {
		New(testSchema(), WithRegistry(NewRegistry()), WithStrictRegistry(true))
	})
	assert.NotPanics(t, func() {
		New(testSchema(), WithRegistry(Default()), WithStrictRegistry(true))
	})
}
