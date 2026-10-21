package validate

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/runtime/schema/go/internal/scalarcore"
)

// Finance.Currency, the code the commerce behaviors' amounts name (D70),
// is three uppercase letters by its pattern, but only the active ISO 4217
// codes are currencies: the core checks the list, so a value the pattern
// admits (ABC) is refused through the default registry, as a lowercase or
// historic code is. It skips while the linked superscalar has no
// Finance.Currency, until superscalar.pin moves past the scalar's commit.
func TestFinanceCurrencyThroughTheCore(t *testing.T) {
	if !slices.Contains(scalarcore.Names(), "Finance.Currency") {
		t.Skip("the linked superscalar has no Finance.Currency; it arrives with the pin bump")
	}
	schema := ir.NewSchema("prices", ir.SchemaKindGeneral)
	schema.Scalars["Finance.Currency"] = &ir.ScalarDef{Name: "Finance.Currency", Primitive: "String", Pattern: "^[A-Z]{3}$", HasCustomValidate: true}
	schema.Types["Price"] = &ir.TypeDef{
		Name: "Price",
		Kind: ir.TypeKindObject,
		Fields: []*ir.FieldDef{
			{Name: "currency", TypeRef: ir.TypeRef{Name: "Finance.Currency"}, Required: true},
		},
	}
	validator := New(schema, WithRegistry(DefaultRegistry()))
	for _, code := range []string{"GBP", "EUR", "USD", "JPY", "XAU", "CHE"} {
		assert.False(t, validator.ValidateType("Price", map[string]any{"currency": code}).HasErrors(), code)
	}
	for _, code := range []string{"ABC", "gbp", "Gbp", "DEM", "GB", "GBPP", ""} {
		result := validator.ValidateType("Price", map[string]any{"currency": code})
		assert.True(t, result.HasErrors(), code)
	}
	result := validator.ValidateType("Price", map[string]any{"currency": "ABC"})
	if assert.NotEmpty(t, result.GetFieldErrors("currency")) {
		assert.Equal(t, "pattern", result.GetFieldErrors("currency")[0].Validator)
	}
}
