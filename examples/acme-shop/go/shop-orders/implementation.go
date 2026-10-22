// Package shoporders implements the shop-orders API over the generated
// shop-db ORM and shop-db's engine (D58, D70). It sits where the naming
// file's [implementation_paths] go template puts it, go/{service}, so the
// entrypoint the stack's build writes for shop-orders' server imports it
// and calls New with the server's Deps. The identity runtime
// authenticates its callers, with the sessions shop-api's sign-in starts
// (D50). A shopper's checkout, which shop-cart filled, holds its units and
// pickup slot through the engine's Holds, is paid for at acmepay, the
// fake provider in go/acmepay, through Payments and this package's
// adapter, and once placed writes its Order row and tells shop-stock, a
// Rust server, what it sold, through the client of shop-stock's Go SDK its
// Deps holds (D57). Its lists of orders, reviews and products answer a
// page at a time, in the sort a request asks for, through the ORM's
// FindPage (D60, amended), and the reviews' and the products' searches
// read each row's search_text (D62).
package shoporders

import (
	"net/http"
	"time"

	api "example.com/acme/api/shop-orders"
	"example.com/acme/shop/acmepay"
)

// New builds the implementation of shop-orders from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	pay := payClient(deps)
	return api.Implementations{
		Order:          &Orders{DB: deps.DB},
		ProductCatalog: &Products{DB: deps.DB},
		ProductReviews: &Reviews{DB: deps.DB},
		Checkouts:      &Checkouts{Engine: deps.Engine, DB: deps.DB, Stock: deps.ShopStock, Pay: pay, Logger: deps.Logger},
		PaymentWebhooks: &PaymentWebhooks{
			Engine: deps.Engine, DB: deps.DB, Stock: deps.ShopStock, Logger: deps.Logger,
		},
		WebhookVerifiers: map[string]api.WebhookVerifier{
			"acmepay": Verifier(deps.Config.AcmepayWebhookSecret, time.Now),
		},
	}, nil
}

var _ api.Constructor = New

// payClient is acmepay's client, at the URL and with the key shop-orders'
// config holds.
func payClient(deps api.Deps) *acmepay.Client {
	return &acmepay.Client{
		BaseURL: deps.Config.AcmepayUrl,
		APIKey:  deps.Config.AcmepayApiKey,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}
