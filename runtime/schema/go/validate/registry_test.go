package validate

import (
	"testing"

	scalarlib "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/ir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRegistry_Empty(t *testing.T) {
	r := NewRegistry()

	assert.Equal(t, 0, r.Len())
	assert.Empty(t, r.Names())
	assert.False(t, r.Has("Contact.Email"))
}

func TestRegistry_Register_And_Get(t *testing.T) {
	r := NewRegistry()
	called := false
	r.Register("TestScalar", func(value string) []ValidationError {
		called = true
		return nil
	})

	fn, ok := r.Get("TestScalar")
	assert.True(t, ok)
	require.NotNil(t, fn)

	errs := fn("hello")
	assert.True(t, called)
	assert.Nil(t, errs)
}

func TestRegistry_Get_NotFound(t *testing.T) {
	r := NewRegistry()

	fn, ok := r.Get("NoSuchScalar")
	assert.False(t, ok)
	assert.Nil(t, fn)
}

func TestRegistry_Register_Replaces(t *testing.T) {
	r := NewRegistry()
	r.Register("Foo", func(value string) []ValidationError {
		return []ValidationError{{Validator: "first", Message: "first"}}
	})
	r.Register("Foo", func(value string) []ValidationError {
		return []ValidationError{{Validator: "second", Message: "second"}}
	})

	fn, ok := r.Get("Foo")
	require.True(t, ok)

	errs := fn("x")
	require.Len(t, errs, 1)
	assert.Equal(t, "second", errs[0].Validator)
	assert.Equal(t, 1, r.Len())
}

func TestRegistry_Has(t *testing.T) {
	r := NewRegistry()
	r.Register("Contact.Email", func(string) []ValidationError { return nil })

	assert.True(t, r.Has("Contact.Email"))
	assert.False(t, r.Has("Contact.PhoneNumber"))
}

func TestRegistry_Unregister(t *testing.T) {
	r := NewRegistry()
	r.Register("Contact.Email", func(string) []ValidationError { return nil })
	r.Register("Contact.PhoneNumber", func(string) []ValidationError { return nil })
	assert.Equal(t, 2, r.Len())

	removed := r.Unregister("Contact.Email")
	assert.True(t, removed)
	assert.False(t, r.Has("Contact.Email"))
	assert.Equal(t, 1, r.Len())

	removed = r.Unregister("Contact.Email")
	assert.False(t, removed, "second unregister should return false")
}

func TestRegistry_Len(t *testing.T) {
	r := NewRegistry()
	assert.Equal(t, 0, r.Len())

	r.Register("A", func(string) []ValidationError { return nil })
	assert.Equal(t, 1, r.Len())

	r.Register("B", func(string) []ValidationError { return nil })
	assert.Equal(t, 2, r.Len())

	r.Unregister("A")
	assert.Equal(t, 1, r.Len())
}

func TestRegistry_Names_Sorted(t *testing.T) {
	r := NewRegistry()
	r.Register("Zulu", func(string) []ValidationError { return nil })
	r.Register("Alpha", func(string) []ValidationError { return nil })
	r.Register("Mike", func(string) []ValidationError { return nil })

	assert.Equal(t, []string{"Alpha", "Mike", "Zulu"}, r.Names())
}

func TestRegistry_MissingValidators_AllCovered(t *testing.T) {
	s := ir.NewSchema("test", ir.SchemaKindGeneral)
	s.Scalars["Contact.Email"] = &ir.ScalarDef{Name: "Contact.Email", HasCustomValidate: true}
	s.Scalars["Contact.PhoneNumber"] = &ir.ScalarDef{Name: "Contact.PhoneNumber", HasCustomValidate: true}
	s.Scalars["Parable.Slug"] = &ir.ScalarDef{Name: "Parable.Slug", HasCustomValidate: false}

	r := NewRegistry()
	r.Register("Contact.Email", func(string) []ValidationError { return nil })
	r.Register("Contact.PhoneNumber", func(string) []ValidationError { return nil })

	missing := r.MissingValidators(s)
	assert.Empty(t, missing)
}

func TestRegistry_MissingValidators_Partial(t *testing.T) {
	s := ir.NewSchema("test", ir.SchemaKindGeneral)
	s.Scalars["Contact.Email"] = &ir.ScalarDef{Name: "Contact.Email", HasCustomValidate: true}
	s.Scalars["Design.Color"] = &ir.ScalarDef{Name: "Design.Color", HasCustomValidate: true}
	s.Scalars["Contact.PhoneNumber"] = &ir.ScalarDef{Name: "Contact.PhoneNumber", HasCustomValidate: true}
	s.Scalars["Parable.Slug"] = &ir.ScalarDef{Name: "Parable.Slug", HasCustomValidate: false}

	r := NewRegistry()
	r.Register("Contact.Email", func(string) []ValidationError { return nil })

	missing := r.MissingValidators(s)
	assert.Equal(t, []string{"Contact.PhoneNumber", "Design.Color"}, missing)
}

func TestRegistry_MissingValidators_NoneCustom(t *testing.T) {
	s := ir.NewSchema("test", ir.SchemaKindGeneral)
	s.Scalars["Identity.Name"] = &ir.ScalarDef{Name: "Identity.Name", HasCustomValidate: false}
	s.Scalars["Parable.Slug"] = &ir.ScalarDef{Name: "Parable.Slug", HasCustomValidate: false}

	r := NewRegistry()

	missing := r.MissingValidators(s)
	assert.Empty(t, missing)
}

func TestRegistry_MissingValidators_NamespacedCollisionIsolation(t *testing.T) {
	s := ir.NewSchema("test", ir.SchemaKindGeneral)
	s.Scalars["Parable.Slug"] = &ir.ScalarDef{Name: "Parable.Slug", HasCustomValidate: true}
	s.Scalars["Identity.Slug"] = &ir.ScalarDef{Name: "Identity.Slug", HasCustomValidate: true}

	r := NewRegistry()
	r.Register("Parable.Slug", func(string) []ValidationError { return nil })

	missing := r.MissingValidators(s)
	assert.Equal(t, []string{"Identity.Slug"}, missing)
}

func TestRegistry_MissingValidators_EmptySchema(t *testing.T) {
	s := ir.NewSchema("test", ir.SchemaKindGeneral)

	r := DefaultRegistry()

	missing := r.MissingValidators(s)
	assert.Empty(t, missing)
}

func TestDefaultRegistry_Contents(t *testing.T) {
	r := DefaultRegistry()

	// Every name the linked scalar core knows is dispatched, not just the
	// ones whose metadata marks a custom validator: the core's Validate is
	// the single source of truth for all of them.
	assert.Equal(t, len(scalarlib.ScalarIDByCanonical), r.Len())
	for name := range scalarlib.ScalarIDByCanonical {
		assert.True(t, r.Has(name), "expected default registry to contain %q", name)
	}
	assert.False(t, r.Has("Custom.Missing"))
}

func TestNewDispatchRegistry_MapsCoreErrorToScalarTag(t *testing.T) {
	r := NewDispatchRegistry([]string{"Contact.Email"}, scalarlib.Validate)
	require.Equal(t, []string{"Contact.Email"}, r.Names())

	fn, ok := r.Get("Contact.Email")
	require.True(t, ok)
	assert.Nil(t, fn("test@example.com"))

	errs := fn("not-an-email")
	require.Len(t, errs, 1)
	assert.Equal(t, "scalar", errs[0].Validator)
	assert.NotEmpty(t, errs[0].Message)
}

func TestDefaultRegistry_Email_Valid(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Contact.Email")
	require.True(t, ok)

	errs := fn("test@example.com")
	assert.Nil(t, errs)
}

func TestDefaultRegistry_PhoneNumber_Invalid(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Contact.PhoneNumber")
	require.True(t, ok)

	errs := fn("not-a-phone")
	assert.NotEmpty(t, errs)
}

func TestDefaultRegistry_Color_Valid(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Design.Color")
	require.True(t, ok)

	errs := fn("#FF0000FF")
	assert.Nil(t, errs)
}

func TestDefaultRegistry_Color_Invalid(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Design.Color")
	require.True(t, ok)

	errs := fn("not-a-color")
	assert.NotEmpty(t, errs)
}

func TestRegistry_IntegrationWithValidator(t *testing.T) {
	s := ir.NewSchema("test", ir.SchemaKindGeneral)
	s.Scalars["Design.Color"] = &ir.ScalarDef{
		Name:              "Design.Color",
		Primitive:         "String",
		MaxLength:         9,
		Pattern:           `^#[0-9A-Fa-f]{8}$`,
		HasCustomValidate: true,
	}
	s.Types["Theme"] = &ir.TypeDef{
		Name: "Theme",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "primary", TypeRef: ir.TypeRef{Name: "Design.Color"}, Required: true},
		},
	}

	reg := DefaultRegistry()
	v := New(s, WithRegistry(reg))

	errs := v.ValidateType("Theme", map[string]any{
		"primary": "#FF0000FF",
	})
	assert.False(t, errs.HasErrors())

	errs = v.ValidateType("Theme", map[string]any{
		"primary": "bad-color",
	})
	assert.True(t, errs.HasErrors())
}

func TestDefaultRegistry_Duration_Valid(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Temporal.Duration")
	require.True(t, ok)

	errs := fn("5s")
	assert.Empty(t, errs)
}

func TestDefaultRegistry_Duration_Invalid(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Temporal.Duration")
	require.True(t, ok)

	errs := fn("not-a-duration")
	assert.NotEmpty(t, errs)
}

func TestDefaultRegistry_Permission_Valid(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Parable.Permission")
	require.True(t, ok)

	errs := fn("admin")
	assert.Empty(t, errs)
}

func TestDefaultRegistry_Permission_Invalid(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Parable.Permission")
	require.True(t, ok)

	errs := fn("not.a.real.permission")
	assert.NotEmpty(t, errs)
}

func TestDefaultRegistry_File_ValidJSON(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Asset.File")
	require.True(t, ok)

	errs := fn(`{"url":"https://cdn.example.com/a.txt","mimeType":"text/plain","size":42,"filename":"a.txt"}`)
	assert.Empty(t, errs)
}

func TestDefaultRegistry_File_FailsStructValidation(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Asset.File")
	require.True(t, ok)

	errs := fn(`{"url":"https://cdn.example.com/a.txt","mimeType":"","size":0,"filename":""}`)
	require.Len(t, errs, 1)
	assert.Equal(t, "scalar", errs[0].Validator)
}

func TestDefaultRegistry_File_InvalidJSON(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Asset.File")
	require.True(t, ok)

	errs := fn(`{not json}`)
	require.Len(t, errs, 1)
	assert.Equal(t, "scalar", errs[0].Validator)
}

func TestDefaultRegistry_Image_Valid(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Asset.Image")
	require.True(t, ok)

	errs := fn(`{"url":"https://cdn.example.com/a.png","mimeType":"image/png","size":1024,"width":100,"height":100,"filename":"a.png"}`)
	assert.Empty(t, errs)
}

func TestDefaultRegistry_Image_InvalidMimeType(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Asset.Image")
	require.True(t, ok)

	errs := fn(`{"url":"https://cdn.example.com/a.bmp","mimeType":"image/bmp","size":1024,"width":100,"height":100,"filename":"a.bmp"}`)
	require.NotEmpty(t, errs)
	assert.Equal(t, "scalar", errs[0].Validator)
}

func TestDefaultRegistry_LogoImage_Valid(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Asset.LogoImage")
	require.True(t, ok)

	errs := fn(`{"url":"https://cdn.example.com/logo.png","mimeType":"image/png","size":2048,"width":256,"height":256,"filename":"logo.png","hasTransparency":true}`)
	assert.Empty(t, errs)
}

func TestDefaultRegistry_LogoImage_RejectsNonPNG(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Asset.LogoImage")
	require.True(t, ok)

	errs := fn(`{"url":"https://cdn.example.com/logo.jpg","mimeType":"image/jpeg","size":2048,"width":256,"height":256,"filename":"logo.jpg"}`)
	require.NotEmpty(t, errs)
	assert.Equal(t, "scalar", errs[0].Validator)
}

func TestDefaultRegistry_ArtifactFile_Valid(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Artifact.File")
	require.True(t, ok)

	errs := fn(`{"gcsPath":"gs://bucket/path/file.csv","mimeType":"text/csv","size":1024,"filename":"file.csv"}`)
	assert.Empty(t, errs)
}

func TestDefaultRegistry_ArtifactFile_FailsStructValidation(t *testing.T) {
	r := DefaultRegistry()

	fn, ok := r.Get("Artifact.File")
	require.True(t, ok)

	errs := fn(`{"gcsPath":"","mimeType":"","size":0,"filename":""}`)
	require.NotEmpty(t, errs)
}

func TestNew_WithStrictRegistry_PanicsOnMissing(t *testing.T) {
	s := ir.NewSchema("test", ir.SchemaKindGeneral)
	s.Scalars["Custom.Missing"] = &ir.ScalarDef{Name: "Custom.Missing", HasCustomValidate: true}
	s.Scalars["Contact.Email"] = &ir.ScalarDef{Name: "Contact.Email", HasCustomValidate: true}

	r := NewRegistry()
	r.Register("Contact.Email", func(string) []ValidationError { return nil })

	assert.PanicsWithValue(t,
		`validate: schema "test" has HasCustomValidate scalars without registered validators: [Custom.Missing]`,
		func() {
			New(s, WithRegistry(r), WithStrictRegistry(true))
		},
	)
}

func TestNew_WithStrictRegistry_OkWhenComplete(t *testing.T) {
	s := ir.NewSchema("test", ir.SchemaKindGeneral)
	s.Scalars["Contact.Email"] = &ir.ScalarDef{Name: "Contact.Email", HasCustomValidate: true}

	assert.NotPanics(t, func() {
		v := New(s, WithRegistry(DefaultRegistry()), WithStrictRegistry(true))
		assert.NotNil(t, v)
	})
}

func TestNew_DefaultNoStrict_NoPanic(t *testing.T) {
	s := ir.NewSchema("test", ir.SchemaKindGeneral)
	s.Scalars["Custom.Missing"] = &ir.ScalarDef{Name: "Custom.Missing", HasCustomValidate: true}

	assert.NotPanics(t, func() {
		v := New(s, WithRegistry(NewRegistry()))
		assert.NotNil(t, v)
	})
}

func TestRegistry_IntegrationWithValidator_FileScalar(t *testing.T) {
	s := ir.NewSchema("test", ir.SchemaKindGeneral)
	s.Scalars["Asset.File"] = &ir.ScalarDef{
		Name:              "Asset.File",
		Primitive:         "String",
		HasCustomValidate: true,
	}
	s.Types["Document"] = &ir.TypeDef{
		Name: "Document",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "attachment", TypeRef: ir.TypeRef{Name: "Asset.File"}, Required: true},
		},
	}

	v := New(s, WithRegistry(DefaultRegistry()))

	errs := v.ValidateType("Document", map[string]any{
		"attachment": `{"url":"https://cdn.example.com/a.txt","mimeType":"text/plain","size":42,"filename":"a.txt"}`,
	})
	assert.False(t, errs.HasErrors())

	errs = v.ValidateType("Document", map[string]any{
		"attachment": `{"url":"https://cdn.example.com/a.txt","mimeType":"","size":0,"filename":""}`,
	})
	assert.True(t, errs.HasErrors())
}

func TestRegistry_FallbackToIRConstraints(t *testing.T) {
	s := ir.NewSchema("test", ir.SchemaKindGeneral)
	s.Scalars["CustomScalar"] = &ir.ScalarDef{
		Name:              "CustomScalar",
		Primitive:         "String",
		MaxLength:         5,
		HasCustomValidate: true,
	}
	s.Types["Widget"] = &ir.TypeDef{
		Name: "Widget",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "value", TypeRef: ir.TypeRef{Name: "CustomScalar"}, Required: false},
		},
	}

	v := New(s)

	errs := v.ValidateType("Widget", map[string]any{
		"value": "toolong",
	})
	assert.True(t, errs.HasErrors(), "IR constraints should still apply without registry entry")
	valErrs := errs.GetFieldErrors("value")
	require.Len(t, valErrs, 1)
	assert.Equal(t, "maxLength", valErrs[0].Validator)
}
