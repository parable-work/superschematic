package loader

import (
	"strings"
	"testing"
)

// TestDisplayCurrencyIsCheckedInTheDataForms: a field's @display currency
// (D48, amended) loads from YAML on Finance.Money and Generic.Int64, each
// an integer by the catalog's row, and the loader refuses it on a field that holds no amount,
// with a code no list carries, or naming a field that holds no text.
func TestDisplayCurrencyIsCheckedInTheDataForms(t *testing.T) {
	service := func(fields string) string {
		return writeService(t, map[string]string{
			"schema.config.yaml": "name: prices\nkind: General\noutputs: {}\n",
			"src/price.schema.yaml": `name: prices
kind: General
scalars:
  Finance.Money: { name: Finance.Money, languagePrimitive: number }
  Finance.Currency: { name: Finance.Currency, languagePrimitive: string }
  Generic.Int64: { name: Generic.Int64, languagePrimitive: number }
types:
  Price:
    name: Price
    role: EmbeddedStruct
    fields:
      - name: currency
        typeRef: { name: Finance.Currency }
` + fields,
		})
	}
	schema, err := LoadService(service(`      - name: amount
        typeRef: { name: Finance.Money }
        display: { currency: GBP }
      - name: tiers
        typeRef: { name: Generic.Int64, isArray: true }
        display: { currency: { field: currency } }
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range schema.Types["Price"].Fields {
		if f.Name != "currency" && (f.Display == nil || f.Display.Currency == nil) {
			t.Errorf("field %s display = %+v", f.Name, f.Display)
		}
	}

	for _, test := range []struct{ name, fields, want string }{
		{"text", "      - name: note\n        typeRef: { name: string }\n        display: { currency: GBP }\n",
			"type Price: field note takes @display currency, which is for an amount in minor units, but it is string"},
		{"a number", "      - name: rate\n        typeRef: { name: number }\n        display: { currency: GBP }\n",
			"type Price: field rate takes @display currency, which is for an amount in minor units, but it is number"},
		{"a code no list carries", "      - name: fee\n        typeRef: { name: Finance.Money }\n        display: { currency: XYZ }\n",
			`type Price: field fee takes @display currency "XYZ", which is not an active ISO 4217 currency code`},
		{"a field that holds no text", "      - name: fee\n        typeRef: { name: Finance.Money }\n        display: { currency: { field: count } }\n      - name: count\n        typeRef: { name: Generic.Int64 }\n",
			`type Price: field fee takes @display currency { field: "count" }, which is Generic.Int64; a currency field holds a single Finance.Currency or text value`},
		{"a lower-case code", "      - name: fee\n        typeRef: { name: Finance.Money }\n        display: { currency: gbp }\n",
			"display"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadService(service(test.fields))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("load = %v, want an error containing %s", err, test.want)
			}
		})
	}
}
