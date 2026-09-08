package parse

import (
	"testing"

	scalarlib "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/ir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultNormalizeRegistry(t *testing.T) {
	r := DefaultNormalizeRegistry()
	require.True(t, r.Has("Contact.Email"))
	require.True(t, r.Has("Design.Color"))
	require.True(t, r.Has("Contact.PhoneNumber"))
	require.True(t, r.Has("Temporal.Duration"))

	fn, _ := r.Get("Contact.Email")
	assert.Equal(t, "user@example.com", fn("  USER@example.COM  "))
}

func TestDefaultParseRegistry_DateTime(t *testing.T) {
	r := DefaultParseRegistry()
	fn, ok := r.Get("Temporal.DateTime")
	require.True(t, ok)

	got, errs := fn("2024-01-15T10:30:00Z")
	assert.Empty(t, errs)
	assert.Equal(t, "2024-01-15T10:30:00Z", got)

	_, errs = fn("garbage")
	require.NotEmpty(t, errs)
	assert.Equal(t, "parse", errs[0].Validator)
}

func TestDefaultParseRegistry_Duration(t *testing.T) {
	r := DefaultParseRegistry()
	fn, ok := r.Get("Temporal.Duration")
	require.True(t, ok)

	got, errs := fn("60s")
	assert.Empty(t, errs)
	assert.Equal(t, "1m0s", got, "Go duration canonicalizes 60s to 1m0s")
}

func TestDefaultParseRegistry_TemporalIntegerUnits(t *testing.T) {
	r := DefaultParseRegistry()

	for _, name := range []string{
		"Temporal.Milliseconds",
		"Temporal.Seconds",
		"Temporal.Minutes",
		"Temporal.Hours",
		"Temporal.Days",
	} {
		t.Run(name, func(t *testing.T) {
			fn, ok := r.Get(name)
			require.True(t, ok)

			got, errs := fn("-1234")
			assert.Empty(t, errs)
			assert.Equal(t, "-1234", got)

			_, errs = fn("1.5")
			require.NotEmpty(t, errs)
			assert.Equal(t, "parse", errs[0].Validator)
		})
	}
}

// DefaultParseRegistry holds exactly the names the Parser consults it for:
// those whose core metadata marks a custom parse step. Names with a no-op
// parse (Contact.Email) are not registered; the extension's registry in
// utils/parable-schematic/ext/scalars keeps its passthrough entries.
func TestDefaultParseRegistry_NamesFollowCoreMetadata(t *testing.T) {
	r := DefaultParseRegistry()
	for name, meta := range scalarlib.ScalarMetadataByCanonical {
		assert.Equal(t, meta.HasCustomParse, r.Has(name), "parse entry for %q", name)
	}
	assert.False(t, r.Has("Contact.Email"))
}

func TestDefaultNormalizeRegistry_NamesFollowCoreMetadata(t *testing.T) {
	r := DefaultNormalizeRegistry()
	for name, meta := range scalarlib.ScalarMetadataByCanonical {
		assert.Equal(t, meta.HasCustomNormalize, r.Has(name), "normalize entry for %q", name)
	}
}

func TestNewDispatchNormalizeRegistry_CoreErrorLeavesInputUnchanged(t *testing.T) {
	r := NewDispatchNormalizeRegistry([]string{"Temporal.Duration"}, scalarlib.Normalize)
	fn, ok := r.Get("Temporal.Duration")
	require.True(t, ok)
	assert.Equal(t, "1m0s", fn("60s"))
	assert.Equal(t, "not a duration", fn("not a duration"))
}

func TestMissingParsers(t *testing.T) {
	r := NewParseRegistry()
	s := ir.NewSchema("t", ir.SchemaKindGeneral)
	s.Scalars["A"] = &ir.ScalarDef{Name: "A", HasCustomParse: true}
	s.Scalars["B"] = &ir.ScalarDef{Name: "B", HasCustomParse: false}

	missing := r.MissingParsers(s)
	assert.Equal(t, []string{"A"}, missing)

	r.Register("A", func(s string) (string, []ValidationError) { return s, nil })
	assert.Empty(t, r.MissingParsers(s))
}

func TestMissingNormalizers(t *testing.T) {
	r := NewNormalizeRegistry()
	s := ir.NewSchema("t", ir.SchemaKindGeneral)
	s.Scalars["A"] = &ir.ScalarDef{Name: "A", HasCustomNormalize: true}
	s.Scalars["B"] = &ir.ScalarDef{Name: "B", HasCustomNormalize: false}

	missing := r.MissingNormalizers(s)
	assert.Equal(t, []string{"A"}, missing)

	r.Register("A", func(s string) string { return s })
	assert.Empty(t, r.MissingNormalizers(s))
}

func TestParseRegistry_UnregisterAndNames(t *testing.T) {
	r := NewParseRegistry()
	identity := func(s string) (string, []ValidationError) { return s, nil }
	r.Register("X", identity)
	r.Register("A", identity)
	assert.Equal(t, []string{"A", "X"}, r.Names())
	assert.Equal(t, 2, r.Len())
	assert.True(t, r.Unregister("X"))
	assert.False(t, r.Unregister("X"))
	assert.Equal(t, []string{"A"}, r.Names())
}

func TestNormalizeRegistry_UnregisterAndNames(t *testing.T) {
	r := NewNormalizeRegistry()
	r.Register("Y", func(s string) string { return s })
	r.Register("B", func(s string) string { return s })
	assert.Equal(t, []string{"B", "Y"}, r.Names())
	assert.Equal(t, 2, r.Len())
	assert.True(t, r.Unregister("Y"))
	assert.False(t, r.Unregister("Y"))
	assert.Equal(t, []string{"B"}, r.Names())
}
