package validate

import (
	"testing"

	scalarlib "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/ir"
	"github.com/stretchr/testify/assert"
)

// par12Schema declares scalars by their CANONICAL names (the keys in
// superscalar's ScalarIDByCanonical), each carrying only the generic IR
// constraints superschematic would emit. The point of these tests is that the generic
// constraints alone DO NOT catch the deep semantic checks (Embedding.Vector /
// Generic.StringMap serde, Asset.FilePath / Text.Markdown min_length 1) -- those
// live in the Rust core and must be reached by delegation, not by the IR.
func par12Schema() *ir.Schema {
	s := ir.NewSchema("par12", ir.SchemaKindGeneral)

	// Embedding.Vector: no IR pattern/length, so the generic validator accepts
	// any non-empty string. The core rejects non-JSON-float-array input.
	s.Scalars["Embedding.Vector"] = &ir.ScalarDef{
		Name:      "Embedding.Vector",
		Primitive: "String",
	}
	// Generic.StringMap: same shape, core requires valid JSON.
	s.Scalars["Generic.StringMap"] = &ir.ScalarDef{
		Name:      "Generic.StringMap",
		Primitive: "String",
	}
	// Asset.FilePath: core enforces min_length 1.
	s.Scalars["Asset.FilePath"] = &ir.ScalarDef{
		Name:      "Asset.FilePath",
		Primitive: "String",
	}
	// Text.Markdown: core enforces min_length 1.
	s.Scalars["Text.Markdown"] = &ir.ScalarDef{
		Name:      "Text.Markdown",
		Primitive: "String",
	}

	s.Types["Doc"] = &ir.TypeDef{
		Name: "Doc",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "vector", TypeRef: ir.TypeRef{Name: "Embedding.Vector"}, Required: true},
			{Name: "meta", TypeRef: ir.TypeRef{Name: "Generic.StringMap"}, Required: true},
			{Name: "path", TypeRef: ir.TypeRef{Name: "Asset.FilePath"}, Required: true},
			{Name: "body", TypeRef: ir.TypeRef{Name: "Text.Markdown"}, Required: true},
			// Optional variants exercise the validateScalarValue (no-required) path.
			{Name: "altPath", TypeRef: ir.TypeRef{Name: "Asset.FilePath"}, Required: false},
			{Name: "altVector", TypeRef: ir.TypeRef{Name: "Embedding.Vector"}, Required: false},
		},
	}

	return s
}

// TestScalarDelegation_RegistryGovernsEveryName is the W8 contract: the
// validator reaches the scalar core only through the injected registry. With
// an empty registry the same deep-check values fall back to the generic IR
// constraints (which the fixture leaves empty) and are accepted; the old
// KnownScalar short-circuit would have rejected them behind the registry's
// back.
func TestScalarDelegation_RegistryGovernsEveryName(t *testing.T) {
	input := map[string]any{
		"vector": "notavector",
		"meta":   "not json",
		"path":   "ok/path",
		"body":   "# heading",
	}

	viaDefault := New(par12Schema(), WithRegistry(DefaultRegistry())).ValidateType("Doc", input)
	assert.True(t, viaDefault.HasErrors(), "DefaultRegistry dispatches Embedding.Vector to the core")
	assert.Equal(t, "scalar", viaDefault.GetFieldErrors("vector")[0].Validator)

	viaEmpty := New(par12Schema(), WithRegistry(NewRegistry())).ValidateType("Doc", input)
	assert.False(t, viaEmpty.HasErrors(), "an empty registry must not reach the core, got: %v", viaEmpty)

	// A registry that names Embedding.Vector but not Generic.StringMap
	// governs exactly what it names.
	partial := NewDispatchRegistry([]string{"Embedding.Vector"}, scalarlib.Validate)
	viaPartial := New(par12Schema(), WithRegistry(partial)).ValidateType("Doc", input)
	assert.NotEmpty(t, viaPartial.GetFieldErrors("vector"))
	assert.Empty(t, viaPartial.GetFieldErrors("meta"))
}

// TestScalarDelegation_RejectsInvalidPAR12 asserts the runtime validator now
// surfaces the core's deep checks. Against the old hand-rolled validator
// (generic IR constraints only) every one of these would pass, which is the
// over-acceptance bug being closed.
func TestScalarDelegation_RejectsInvalidPAR12(t *testing.T) {
	v := New(par12Schema(), WithRegistry(DefaultRegistry()))

	errs := v.ValidateType("Doc", map[string]any{
		"vector": "notavector",
		"meta":   "not json",
		"path":   "ok/path",
		"body":   "# heading",
	})

	assert.True(t, errs.HasErrors(), "expected delegation to surface core errors")
	assert.NotEmpty(t, errs.GetFieldErrors("vector"), "Embedding.Vector notavector must error")
	assert.NotEmpty(t, errs.GetFieldErrors("meta"), "Generic.StringMap non-JSON must error")
}

// TestScalarDelegation_RequiredEmptyStrings asserts required empty strings still
// produce a "required" error (semantics preserved) for the min_length-1 scalars.
func TestScalarDelegation_RequiredEmptyStrings(t *testing.T) {
	v := New(par12Schema(), WithRegistry(DefaultRegistry()))

	errs := v.ValidateType("Doc", map[string]any{
		"vector": "[1.0, 2.0]",
		"meta":   `{"k":"v"}`,
		"path":   "",
		"body":   "",
	})

	assert.True(t, errs.HasErrors())
	pathErrs := errs.GetFieldErrors("path")
	bodyErrs := errs.GetFieldErrors("body")
	assert.NotEmpty(t, pathErrs, "empty required Asset.FilePath must error")
	assert.NotEmpty(t, bodyErrs, "empty required Text.Markdown must error")
	// Required-empty semantics are preserved by validateScalarRequired before
	// delegation, so the tag stays "required" rather than the core's "scalar".
	assert.Equal(t, "required", pathErrs[0].Validator)
	assert.Equal(t, "required", bodyErrs[0].Validator)
}

// TestScalarDelegation_OptionalEmptyStringSkipped asserts optional empty strings
// are still skipped (generated optional-field semantics), so an absent/empty
// optional Asset.FilePath does NOT trip the core's min_length 1.
func TestScalarDelegation_OptionalEmptyStringSkipped(t *testing.T) {
	v := New(par12Schema(), WithRegistry(DefaultRegistry()))

	errs := v.ValidateType("Doc", map[string]any{
		"vector":  "[1.0, 2.0]",
		"meta":    `{"k":"v"}`,
		"path":    "ok/path",
		"body":    "# heading",
		"altPath": "", // optional, empty -> skipped, no error
	})

	assert.False(t, errs.HasErrors(), "optional empty string should be skipped, got: %v", errs)
}

// TestScalarDelegation_OptionalInvalidStillRejected asserts a non-empty optional
// value is still delegated and rejected when invalid.
func TestScalarDelegation_OptionalInvalidStillRejected(t *testing.T) {
	v := New(par12Schema(), WithRegistry(DefaultRegistry()))

	errs := v.ValidateType("Doc", map[string]any{
		"vector":    "[1.0, 2.0]",
		"meta":      `{"k":"v"}`,
		"path":      "ok/path",
		"body":      "# heading",
		"altVector": "garbage", // optional, non-empty, invalid -> must error
	})

	assert.True(t, errs.HasErrors())
	assert.NotEmpty(t, errs.GetFieldErrors("altVector"))
}

// TestScalarDelegation_AcceptsValidDeepChecks asserts valid deep-check values pass cleanly.
func TestScalarDelegation_AcceptsValidDeepChecks(t *testing.T) {
	v := New(par12Schema(), WithRegistry(DefaultRegistry()))

	errs := v.ValidateType("Doc", map[string]any{
		"vector": "[1.0, 2.0, 3.0]",
		"meta":   `{"region":"us"}`,
		"path":   "path/to/file.txt",
		"body":   "# Title\n\nbody",
	})

	assert.False(t, errs.HasErrors(), "valid deep-check values should pass, got: %v", errs)
}
