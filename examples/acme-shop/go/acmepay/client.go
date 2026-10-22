package acmepay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client calls a provider's API with the account's key: the shop's
// adapter makes intents, cancels them and refunds them with it, and the
// tests pay as a payer's page would.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// APIError is a provider's refusal: its status and its message.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("acmepay: %d %s", e.Status, e.Message)
}

// IntentRequest is what an intent is made for: an amount in a currency's
// minor units, the shop's reference (the checkout's id) and the engine's
// attempt, which come back on every event about it.
type IntentRequest struct {
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Reference string `json:"reference"`
	Attempt   string `json:"attempt"`
}

// CreateIntent makes an intent, once per key.
func (c *Client) CreateIntent(ctx context.Context, key string, in IntentRequest) (Intent, error) {
	var out Intent
	err := c.call(ctx, http.MethodPost, "/v1/payment_intents", key, in, &out)
	return out, err
}

// GetIntent reads an intent.
func (c *Client) GetIntent(ctx context.Context, id string) (Intent, error) {
	var out Intent
	err := c.call(ctx, http.MethodGet, "/v1/payment_intents/"+id, "", nil, &out)
	return out, err
}

// CancelIntent cancels an intent no one has paid, once per key.
func (c *Client) CancelIntent(ctx context.Context, key, id string) (Intent, error) {
	var out Intent
	err := c.call(ctx, http.MethodPost, "/v1/payment_intents/"+id+"/cancel", key, struct{}{}, &out)
	return out, err
}

// RefundRequest is a refund of an amount of an intent's captured money,
// with the shop's reference and the engine's refund it pays.
type RefundRequest struct {
	PaymentIntent string `json:"paymentIntent"`
	Amount        int64  `json:"amount"`
	Reference     string `json:"reference"`
	Refund        string `json:"refund"`
}

// CreateRefund refunds, once per key. A refund past what the intent has
// captured fails, as the provider's refund.failed event says.
func (c *Client) CreateRefund(ctx context.Context, key string, in RefundRequest) (Refund, error) {
	var out Refund
	err := c.call(ctx, http.MethodPost, "/v1/refunds", key, in, &out)
	return out, err
}

// ConfirmIntent pays an intent as a payer's page does on the provider's
// hosted fields, with the intent's client secret and a test token.
func (c *Client) ConfirmIntent(ctx context.Context, id, clientSecret, token string) (Intent, error) {
	var out Intent
	err := c.call(ctx, http.MethodPost, "/v1/payment_intents/"+id+"/confirm", "", map[string]string{"clientSecret": clientSecret, "token": token}, &out)
	return out, err
}

// SettleIntent finishes an intent a TokenProcessing payment left
// processing: it succeeds.
func (c *Client) SettleIntent(ctx context.Context, id string) (Intent, error) {
	var out Intent
	err := c.call(ctx, http.MethodPost, "/v1/payment_intents/"+id+"/settle", "", struct{}{}, &out)
	return out, err
}

// SetWebhook sets where the provider posts its events.
func (c *Client) SetWebhook(ctx context.Context, url string) error {
	return c.call(ctx, http.MethodPost, "/v1/webhook_endpoints", "", map[string]string{"url": url}, nil)
}

func (c *Client) call(ctx context.Context, method, path, key string, in, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("acmepay: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("acmepay: %s %s: %w", method, path, err)
	}
	// A declined payment answers 402 with the intent, which says so.
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusPaymentRequired {
		var refused problemMessage
		_ = json.Unmarshal(data, &refused)
		return &APIError{Status: resp.StatusCode, Message: refused.Error}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
