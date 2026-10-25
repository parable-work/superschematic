package topcoat

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The scalars of money (D48, amended): an amount in a currency's minor
// units, and the currency's ISO 4217 code.
const (
	moneyScalar    = "Finance.Money"
	currencyScalar = "Finance.Currency"
)

// moneyOf reports whether the crate formats an amount by its currency
// (D44, amended): a display component shows one with views::money_text, or
// a form takes one as a decimal, so Cargo.toml depends on the scalar crate
// for each currency's minor units (WritesMoney). It writes to the build
// log each amount a component shows as a number because it names no
// currency the component can read.
func moneyOf(c registry.GenerateContext, schemas schemaSet, out *crate) (bool, error) {
	seen := map[string]bool{}
	for _, v := range out.Views {
		for _, note := range v.moneyNotes {
			if !seen[note] {
				seen[note] = true
				c.Logf("  - topcoat: %s\n", note)
			}
		}
	}
	return out.ViewsUse("money_text") || len(out.FormCurrencies()) > 0, nil
}

// ScalarIdent is the scalar crate as a Rust path names it.
func (c *crate) ScalarIdent() string { return strings.ReplaceAll(c.ScalarCrate, "-", "_") }

// fieldCurrency is the currency of an amount field: a literal code, or the
// field of the same type that holds it. Both are empty when it names none,
// and why is then the build log's reason, or empty for an amount nobody
// would read as money.
type fieldCurrency struct {
	code    string
	sibling *ir.FieldDef
	why     string
}

// currencyOf is the currency of field, an amount of owner, by its @display
// currency (D48, amended): a code, or a sibling field, which must be one
// of owner's own that holds a single text value. A Finance.Money field
// without one takes owner's one Finance.Currency field, when owner has
// exactly one. Without either, a Finance.Money field says why it shows as
// a number; any other integer is a number with no word.
func currencyOf(schemas schemaSet, owner *ir.TypeDef, field *ir.FieldDef) fieldCurrency {
	if field.TypeRef.IsMap || schemas.leafOf(field.TypeRef.Name) != integerLeaf {
		return fieldCurrency{}
	}
	if field.Display != nil && field.Display.Currency != nil {
		c := field.Display.Currency
		if c.Code != "" {
			return fieldCurrency{code: c.Code}
		}
		sibling := fieldNamed(owner, c.Field)
		if sibling == nil || sibling == field || sibling.TypeRef.IsArray || sibling.TypeRef.IsMap || schemas.leafOf(sibling.TypeRef.Name) != stringLeaf {
			return fieldCurrency{why: fmt.Sprintf("its @display currency field %s holds no currency", c.Field)}
		}
		return fieldCurrency{sibling: sibling}
	}
	if field.TypeRef.Name != moneyScalar {
		return fieldCurrency{}
	}
	var currencies []*ir.FieldDef
	for _, other := range owner.Fields {
		if other != nil && other.TypeRef.Name == currencyScalar && !other.TypeRef.IsArray && !other.TypeRef.IsMap {
			currencies = append(currencies, other)
		}
	}
	switch len(currencies) {
	case 1:
		return fieldCurrency{sibling: currencies[0]}
	case 0:
		return fieldCurrency{why: "it is Finance.Money, and names no currency: @display({ currency }) gives it one"}
	}
	names := make([]string, len(currencies))
	for i, other := range currencies {
		names[i] = other.Name
	}
	return fieldCurrency{why: fmt.Sprintf("it is Finance.Money, and its type has %d Finance.Currency fields (%s): @display({ currency: { field } }) names one", len(currencies), strings.Join(names, ", "))}
}
