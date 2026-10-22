// Command acmepay runs the fake payment provider on this machine, beside
// `superschematic stack dev`, at the address shop-orders' ACMEPAY_URL
// defaults to:
//
//	ACMEPAY_API_KEY=... ACMEPAY_WEBHOOK_SECRET=... go run ./acmepay/cmd/acmepay \
//	  -webhook http://127.0.0.1:<shop-orders' port>/api/payments/acmepay/events
//
// The keys are the values `superschematic stack secrets set Dev` gave
// PaymentsSecrets in the stack's Dev environment.
package main

import (
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"example.com/acme/shop/acmepay"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:4250", "the address to listen on")
	webhook := flag.String("webhook", "", "where to post events; set it later with POST /v1/webhook_endpoints")
	flag.Parse()
	key, secret := os.Getenv("ACMEPAY_API_KEY"), os.Getenv("ACMEPAY_WEBHOOK_SECRET")
	if key == "" || secret == "" {
		log.Fatal("acmepay: set ACMEPAY_API_KEY and ACMEPAY_WEBHOOK_SECRET to the shop's PaymentsSecrets")
	}
	provider := acmepay.New(acmepay.Config{APIKey: key, SigningSecret: secret, WebhookURL: *webhook})
	server := &http.Server{Addr: *listen, Handler: provider, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("acmepay listening on http://%s", *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
