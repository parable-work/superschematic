package shop_test

import (
	"fmt"

	common "example.com/acme/types/go/shop-common"
	db "example.com/acme/types/go/shop-db"
)

const feedItem = `{
  "sku": "darjeeling-first-flush",
  "name": "Darjeeling first flush",
  "price": {"amountCents": 1450, "currency": "GBP"},
  "tags": ["black", "india"],
  "attributes": {"origin": "Darjeeling", "harvest": "2026 spring"}
}`

const feedItemWithColour = `{
  "sku": "darjeeling-first-flush",
  "name": "Darjeeling first flush",
  "price": {"amountCents": 1450, "currency": "GBP"},
  "tags": ["black", "india"],
  "attributes": {},
  "colour": "amber"
}`

func ExampleFeedItemFromJSON() {
	item, err := common.FeedItemFromJSON([]byte(feedItem))
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(item.Name, item.Price.AmountCents, item.Price.Currency, item.Attributes["origin"])

	// FeedItem is @strictJSON: even the non-strict decoder refuses a key it
	// does not declare. Price is not, so its non-strict decoder skips one.
	_, err = common.FeedItemFromJSONNonStrict([]byte(feedItemWithColour))
	fmt.Println("feed item with an extra key:", err != nil)
	_, err = common.PriceFromJSONNonStrict([]byte(`{"amountCents": 1450, "currency": "GBP", "note": "sale"}`))
	fmt.Println("price with an extra key:", err != nil)
	// Output:
	// Darjeeling first flush 1450 GBP Darjeeling
	// feed item with an extra key: true
	// price with an extra key: false
}

func ExampleShippingAddress_Validate() {
	address := db.ShippingAddress{
		Recipient: "Ada Lovelace",
		Line1:     "12 St James's Square",
		City:      "London",
		Postcode:  "SW1Y 4JH",
		Country:   "gb",
	}
	for _, e := range address.Validate().GetFieldErrors("country") {
		fmt.Println(e.Validator, e.Message)
	}
	// Output: pattern invalid format
}
