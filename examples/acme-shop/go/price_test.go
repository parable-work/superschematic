package shop_test

import (
	"fmt"

	common "example.com/acme/types/go/shop-common"
)

func ExamplePriceFromJSON() {
	price, err := common.PriceFromJSON([]byte(`{"amountCents": 1999, "currency": "EUR"}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(price.AmountCents, price.Currency)
	// Output: 1999 EUR
}

func ExamplePrice_Validate() {
	price := common.Price{AmountCents: 1999, Currency: "YEN"}
	errs := price.Validate()
	for _, e := range errs.GetFieldErrors("currency") {
		fmt.Println(e.Validator)
	}
	// Output: enum
}

func ExamplePriceFromJSON_wrongType() {
	_, err := common.PriceFromJSON([]byte(`{"amountCents": "19.99", "currency": "EUR"}`))
	fmt.Println(err != nil)
	// Output: true
}
