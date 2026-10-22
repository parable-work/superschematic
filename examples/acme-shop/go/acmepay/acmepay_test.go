package acmepay_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/acme/shop/acmepay"
)

// A signature verifies under the secret that made it, and under either of
// two during a rotation; another secret, a changed body, a missing v1 and
// a timestamp past the tolerance do not.
func TestVerify(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"id":"evt_1"}`)
	header := acmepay.Sign("whsec_now", now, body)
	if err := acmepay.Verify(header, body, now, acmepay.DefaultTolerance, "whsec_now"); err != nil {
		t.Fatalf("its own signature: %v", err)
	}
	if err := acmepay.Verify(header, body, now, acmepay.DefaultTolerance, "whsec_next", "whsec_now"); err != nil {
		t.Fatalf("during a rotation: %v", err)
	}
	for name, check := range map[string]func() error{
		"another secret": func() error { return acmepay.Verify(header, body, now, acmepay.DefaultTolerance, "whsec_other") },
		"a changed body": func() error {
			return acmepay.Verify(header, []byte(`{"id":"evt_2"}`), now, acmepay.DefaultTolerance, "whsec_now")
		},
		"no v1": func() error {
			return acmepay.Verify(strings.Split(header, ",")[0], body, now, acmepay.DefaultTolerance, "whsec_now")
		},
		"an old delivery": func() error {
			return acmepay.Verify(header, body, now.Add(6*time.Minute), acmepay.DefaultTolerance, "whsec_now")
		},
	} {
		if err := check(); !errors.Is(err, acmepay.ErrSignature) {
			t.Errorf("%s: %v, want ErrSignature", name, err)
		}
	}
}

// acmepay takes a test token and never a card number; an intent takes an
// idempotency key, which returns the first intent again and refuses
// another request; a call without the account's key is refused.
func TestIntentsTakeTokensAndKeys(t *testing.T) {
	ctx := context.Background()
	provider := acmepay.New(acmepay.Config{APIKey: "ak", SigningSecret: "whsec"})
	server := httptest.NewServer(provider)
	defer server.Close()
	client := &acmepay.Client{BaseURL: server.URL, APIKey: "ak"}

	in := acmepay.IntentRequest{Amount: 900, Currency: "GBP", Reference: "c1", Attempt: "a1"}
	intent, err := client.CreateIntent(ctx, "a1", in)
	if err != nil || intent.Status != acmepay.StatusRequiresPaymentMethod || intent.Event == "" {
		t.Fatalf("CreateIntent = %+v, %v", intent, err)
	}
	again, err := client.CreateIntent(ctx, "a1", in)
	if err != nil || again.ID != intent.ID {
		t.Fatalf("the same key made %+v, %v; want %s", again, err, intent.ID)
	}
	var refused *acmepay.APIError
	in.Amount = 1000
	if _, err := client.CreateIntent(ctx, "a1", in); !errors.As(err, &refused) || refused.Status != http.StatusConflict {
		t.Fatalf("the key with another request: %v, want 409", err)
	}
	if _, err := (&acmepay.Client{BaseURL: server.URL, APIKey: "wrong"}).GetIntent(ctx, intent.ID); !errors.As(err, &refused) || refused.Status != http.StatusUnauthorized {
		t.Fatalf("a call with another key: %v, want 401", err)
	}

	for _, card := range []string{"4242424242424242", "tok_4242"} {
		if _, err := client.ConfirmIntent(ctx, intent.ID, intent.ClientSecret, card); !errors.As(err, &refused) || refused.Status != http.StatusBadRequest {
			t.Errorf("confirming with %q: %v, want 400", card, err)
		}
	}
	paid, err := client.ConfirmIntent(ctx, intent.ID, intent.ClientSecret, acmepay.TokenSucceeds)
	if err != nil || paid.Status != acmepay.StatusSucceeded || paid.Captured != 900 {
		t.Fatalf("ConfirmIntent = %+v, %v", paid, err)
	}
	refund, err := client.CreateRefund(ctx, "r1", acmepay.RefundRequest{PaymentIntent: intent.ID, Amount: 1000, Reference: "c1", Refund: "rf1"})
	if err != nil || refund.Status != "failed" {
		t.Fatalf("a refund past what was captured = %+v, %v; want failed", refund, err)
	}
	if events := provider.Events(); len(events) != 3 || events[2].Type != acmepay.EventRefundFailed {
		t.Fatalf("the events are %+v", events)
	}
}
