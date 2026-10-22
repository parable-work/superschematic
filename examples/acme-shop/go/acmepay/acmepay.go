// Package acmepay is a fake payment provider for the shop (D70): a small
// HTTP server with payment intents, captures, cancellations, refunds and
// signed webhooks, shaped like a card provider's API, which `stack dev`
// beside the shop and the tests run. No real provider is called, and no
// test needs its keys.
//
// It never takes a card number. A payer pays on the provider's own page,
// which ConfirmIntent stands in for, with an opaque test token
// (TokenSucceeds, TokenDeclines, TokenProcessing), and a request that
// carries any other field, a card number among them, is refused. So the
// shop's pages and APIs never see card data, which keeps a merchant to
// SAQ A.
//
// Every change of an intent or a refund is an event with an id. The API's
// answer names it (Intent.Event, Refund.Event), and the webhook delivers
// it, signed as a card provider signs: the header Acmepay-Signature holds
// t=<unix seconds>,v1=<hex HMAC-SHA256 of "<t>.<body>">. So an adapter
// that records the answer and the webhook that brings the same event
// record one event, which the engine applies once per (provider, eventId).
//
// Each POST takes an Idempotency-Key: the same key returns what the first
// request did, so an adapter that retries a call, or a second adapter that
// makes it, gets the first one's result.
package acmepay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SignatureHeader is the header a webhook's signature travels in.
const SignatureHeader = "Acmepay-Signature"

// DefaultTolerance is how far a webhook's timestamp may be from now before
// Verify refuses it as a replay.
const DefaultTolerance = 5 * time.Minute

// The test tokens a payer confirms an intent with, standing in for what
// the provider's hosted fields hand back for a card: TokenSucceeds pays,
// TokenDeclines is declined, and TokenProcessing leaves the intent
// processing until Settle finishes it.
const (
	TokenSucceeds   = "tok_succeeds"
	TokenDeclines   = "tok_declines"
	TokenProcessing = "tok_processing"
)

// An intent's statuses.
const (
	StatusRequiresPaymentMethod = "requires_payment_method"
	StatusProcessing            = "processing"
	StatusSucceeded             = "succeeded"
	StatusFailed                = "payment_failed"
	StatusCanceled              = "canceled"
)

// The events acmepay sends.
const (
	EventIntentCreated    = "payment_intent.created"
	EventIntentProcessing = "payment_intent.processing"
	EventIntentSucceeded  = "payment_intent.succeeded"
	EventIntentFailed     = "payment_intent.payment_failed"
	EventIntentCanceled   = "payment_intent.canceled"
	EventRefundSucceeded  = "refund.succeeded"
	EventRefundFailed     = "refund.failed"
)

// Object is what an event is about, as the webhook carries it: an intent
// or a refund. Reference and Attempt are what the shop sent when it made
// it: its own reference (the checkout's id) and the engine's attempt;
// Refund is the engine's refund a refund pays.
type Object struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
	PaymentIntent string `json:"paymentIntent,omitempty"`
	Reference     string `json:"reference,omitempty"`
	Attempt       string `json:"attempt,omitempty"`
	Refund        string `json:"refund,omitempty"`
}

// Event is a webhook's body.
type Event struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Created int64  `json:"created"`
	Data    struct {
		Object Object `json:"object"`
	} `json:"data"`
}

// Intent is a payment intent: what a payer is asked to pay, and how it
// went. ClientSecret is what the shop hands the payer's page, which pays
// with it. Event is the id of the event its last change made.
type Intent struct {
	ID           string `json:"id"`
	Object       string `json:"object"`
	Amount       int64  `json:"amount"`
	Currency     string `json:"currency"`
	Status       string `json:"status"`
	ClientSecret string `json:"clientSecret"`
	Reference    string `json:"reference,omitempty"`
	Attempt      string `json:"attempt,omitempty"`
	Captured     int64  `json:"captured"`
	Refunded     int64  `json:"refunded"`
	// Event and EventCreated are the id and the time, in seconds, of the
	// event the intent's last change made.
	Event        string `json:"event"`
	EventCreated int64  `json:"eventCreated"`
}

// Refund is a refund of an intent's captured money.
type Refund struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	PaymentIntent string `json:"paymentIntent"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
	Reference     string `json:"reference,omitempty"`
	Refund        string `json:"refund,omitempty"`
	Event         string `json:"event"`
	EventCreated  int64  `json:"eventCreated"`
}

// Config is a provider's keys and where it sends its webhooks.
type Config struct {
	// APIKey is the bearer key every call but a payer's confirmation
	// carries.
	APIKey string
	// SigningSecret signs each webhook.
	SigningSecret string
	// WebhookURL is where events are posted; none, they are kept and not
	// sent until one is set (SetWebhookURL, or POST /v1/webhook_endpoints).
	WebhookURL string
	// Client posts the webhooks; http.DefaultClient when nil.
	Client *http.Client
	// Now is the provider's clock; time.Now when nil.
	Now func() time.Time
}

// Provider is the fake provider: an http.Handler that keeps everything in
// memory.
type Provider struct {
	cfg Config

	mu         sync.Mutex
	webhookURL string
	seq        int
	intents    map[string]*Intent
	refunds    map[string]*Refund
	// replies holds each idempotency key's answer.
	replies map[string]reply
	events  []Event
	// delivered holds the ids of the events a webhook answered 2xx to.
	delivered map[string]bool
}

type reply struct {
	request string
	status  int
	body    []byte
}

// New makes a provider.
func New(cfg Config) *Provider {
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Provider{
		cfg:        cfg,
		webhookURL: cfg.WebhookURL,
		intents:    map[string]*Intent{},
		refunds:    map[string]*Refund{},
		replies:    map[string]reply{},
		delivered:  map[string]bool{},
	}
}

// SetWebhookURL sets where events are posted.
func (p *Provider) SetWebhookURL(url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.webhookURL = url
}

// Events are the events the provider has made, oldest first.
func (p *Provider) Events() []Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.events)
}

// Delivered reports whether a webhook answered 2xx to the event.
func (p *Provider) Delivered(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.delivered[id]
}

// Intent reads an intent.
func (p *Provider) Intent(id string) (Intent, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	intent, ok := p.intents[id]
	if !ok {
		return Intent{}, false
	}
	return *intent, true
}

// Resend posts an event again, as a provider does when it retries a
// delivery, and returns the webhook's status.
func (p *Provider) Resend(ctx context.Context, id string) (int, error) {
	p.mu.Lock()
	var event *Event
	for i := range p.events {
		if p.events[i].ID == id {
			event = &p.events[i]
		}
	}
	p.mu.Unlock()
	if event == nil {
		return 0, fmt.Errorf("acmepay: no event %s", id)
	}
	return p.deliver(ctx, *event)
}

// ids of the provider's objects: a prefix and a count.
func (p *Provider) next(prefix string) string {
	p.seq++
	return fmt.Sprintf("%s_%06d", prefix, p.seq)
}

var (
	tokenPattern     = regexp.MustCompile(`^tok_[a-z_]{1,40}$`)
	currencyPattern  = regexp.MustCompile(`^[A-Z]{3}$`)
	referencePattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{0,255}$`)
)

// ServeHTTP answers the provider's API:
//
//	POST /v1/payment_intents               {amount, currency, reference, attempt}
//	GET  /v1/payment_intents/{id}
//	POST /v1/payment_intents/{id}/confirm  {clientSecret, token}, the payer's page
//	POST /v1/payment_intents/{id}/cancel
//	POST /v1/payment_intents/{id}/settle   finishes a processing intent
//	POST /v1/refunds                       {paymentIntent, amount, reference, refund}
//	POST /v1/webhook_endpoints             {url}
//	POST /v1/events/{id}/resend
func (p *Provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] != "v1" {
		problem(w, http.StatusNotFound, "no such route")
		return
	}
	confirm := len(parts) == 4 && parts[1] == "payment_intents" && parts[3] == "confirm"
	// A payer's page confirms with the intent's client secret; every other
	// call is the shop's, with its key.
	if !confirm && r.Header.Get("Authorization") != "Bearer "+p.cfg.APIKey {
		problem(w, http.StatusUnauthorized, "a call carries the account's API key")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		problem(w, http.StatusBadRequest, "unreadable body")
		return
	}
	if r.Method == http.MethodGet {
		if len(parts) == 3 && parts[1] == "payment_intents" {
			p.mu.Lock()
			intent, ok := p.intents[parts[2]]
			var copied Intent
			if ok {
				copied = *intent
			}
			p.mu.Unlock()
			if !ok {
				problem(w, http.StatusNotFound, "no such intent")
				return
			}
			respond(w, http.StatusOK, copied)
			return
		}
		problem(w, http.StatusNotFound, "no such route")
		return
	}
	if r.Method != http.MethodPost {
		problem(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	request := r.URL.Path + "\n" + string(body)
	if key != "" {
		p.mu.Lock()
		earlier, ok := p.replies[key]
		p.mu.Unlock()
		if ok {
			if earlier.request != request {
				problem(w, http.StatusConflict, "the idempotency key was used with another request")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(earlier.status)
			_, _ = w.Write(earlier.body)
			return
		}
	}
	status, answer, events := p.post(parts[1:], body, key != "")
	encoded, _ := json.Marshal(answer)
	if key != "" && status < 500 {
		p.mu.Lock()
		p.replies[key] = reply{request: request, status: status, body: encoded}
		p.mu.Unlock()
	}
	// Each event goes to the webhook before the answer, so a shop that
	// records the answer finds the event recorded already, or records it
	// first; either way it applies once.
	for _, event := range events {
		_, _ = p.deliver(r.Context(), event)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

// post runs a POST and returns its status, its answer and the events it
// made.
func (p *Provider) post(parts []string, body []byte, keyed bool) (int, any, []Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case len(parts) == 1 && parts[0] == "payment_intents":
		if !keyed {
			return http.StatusBadRequest, problemBody("a payment intent takes an Idempotency-Key"), nil
		}
		var in struct {
			Amount    int64  `json:"amount"`
			Currency  string `json:"currency"`
			Reference string `json:"reference"`
			Attempt   string `json:"attempt"`
		}
		if err := strict(body, &in); err != nil {
			return http.StatusBadRequest, problemBody(err.Error()), nil
		}
		if in.Amount < 1 || !currencyPattern.MatchString(in.Currency) || !referencePattern.MatchString(in.Reference) || !referencePattern.MatchString(in.Attempt) {
			return http.StatusBadRequest, problemBody("an intent takes an amount of 1 or more, a currency code, and a reference and an attempt of letters, digits and ._:-"), nil
		}
		id := p.next("pi")
		intent := &Intent{
			ID: id, Object: "payment_intent", Amount: in.Amount, Currency: in.Currency,
			Status: StatusRequiresPaymentMethod, ClientSecret: id + "_secret_" + strconv.Itoa(p.seq),
			Reference: in.Reference, Attempt: in.Attempt,
		}
		p.intents[id] = intent
		event := p.intentEvent(EventIntentCreated, intent)
		return http.StatusOK, *intent, []Event{event}
	case len(parts) == 3 && parts[0] == "payment_intents":
		intent, ok := p.intents[parts[1]]
		if !ok {
			return http.StatusNotFound, problemBody("no such intent"), nil
		}
		switch parts[2] {
		case "confirm":
			var in struct {
				ClientSecret string `json:"clientSecret"`
				Token        string `json:"token"`
			}
			if err := strict(body, &in); err != nil {
				return http.StatusBadRequest, problemBody("acmepay takes a test token, never a card number: " + err.Error()), nil
			}
			if !hmac.Equal([]byte(in.ClientSecret), []byte(intent.ClientSecret)) {
				return http.StatusUnauthorized, problemBody("the client secret is not the intent's"), nil
			}
			if !tokenPattern.MatchString(in.Token) {
				return http.StatusBadRequest, problemBody("acmepay takes a test token (tok_...), never a card number"), nil
			}
			if intent.Status != StatusRequiresPaymentMethod {
				return http.StatusConflict, problemBody("the intent is " + intent.Status), nil
			}
			switch in.Token {
			case TokenSucceeds:
				intent.Status, intent.Captured = StatusSucceeded, intent.Amount
				return http.StatusOK, *intent, []Event{p.intentEvent(EventIntentSucceeded, intent)}
			case TokenDeclines:
				intent.Status = StatusFailed
				return http.StatusPaymentRequired, *intent, []Event{p.intentEvent(EventIntentFailed, intent)}
			case TokenProcessing:
				intent.Status = StatusProcessing
				return http.StatusOK, *intent, []Event{p.intentEvent(EventIntentProcessing, intent)}
			}
			return http.StatusBadRequest, problemBody("an unknown test token"), nil
		case "settle":
			if intent.Status != StatusProcessing {
				return http.StatusConflict, problemBody("the intent is " + intent.Status), nil
			}
			intent.Status, intent.Captured = StatusSucceeded, intent.Amount
			return http.StatusOK, *intent, []Event{p.intentEvent(EventIntentSucceeded, intent)}
		case "cancel":
			if err := strict(body, &struct{}{}); err != nil {
				return http.StatusBadRequest, problemBody(err.Error()), nil
			}
			switch intent.Status {
			case StatusCanceled, StatusFailed:
				// Nothing to cancel: the answer says what it is.
				return http.StatusOK, *intent, nil
			case StatusSucceeded:
				return http.StatusConflict, problemBody("the intent is succeeded: refund it"), nil
			}
			intent.Status = StatusCanceled
			return http.StatusOK, *intent, []Event{p.intentEvent(EventIntentCanceled, intent)}
		}
	case len(parts) == 1 && parts[0] == "refunds":
		if !keyed {
			return http.StatusBadRequest, problemBody("a refund takes an Idempotency-Key"), nil
		}
		var in struct {
			PaymentIntent string `json:"paymentIntent"`
			Amount        int64  `json:"amount"`
			Reference     string `json:"reference"`
			Refund        string `json:"refund"`
		}
		if err := strict(body, &in); err != nil {
			return http.StatusBadRequest, problemBody(err.Error()), nil
		}
		intent, ok := p.intents[in.PaymentIntent]
		if !ok {
			return http.StatusNotFound, problemBody("no such intent"), nil
		}
		if !referencePattern.MatchString(in.Reference) || !referencePattern.MatchString(in.Refund) {
			return http.StatusBadRequest, problemBody("a reference and a refund are letters, digits and ._:-"), nil
		}
		refund := &Refund{
			ID: p.next("re"), Object: "refund", PaymentIntent: intent.ID, Amount: in.Amount,
			Currency: intent.Currency, Reference: in.Reference, Refund: in.Refund,
		}
		p.refunds[refund.ID] = refund
		eventType := EventRefundSucceeded
		if in.Amount < 1 || in.Amount > intent.Captured-intent.Refunded {
			refund.Status, eventType = "failed", EventRefundFailed
		} else {
			refund.Status = "succeeded"
			intent.Refunded += in.Amount
		}
		event := p.event(eventType, Object{
			ID: refund.ID, Object: "refund", Amount: refund.Amount, Currency: refund.Currency,
			Status: refund.Status, PaymentIntent: intent.ID, Reference: refund.Reference, Refund: refund.Refund,
		})
		refund.Event, refund.EventCreated = event.ID, event.Created
		return http.StatusOK, *refund, []Event{event}
	case len(parts) == 1 && parts[0] == "webhook_endpoints":
		var in struct {
			URL string `json:"url"`
		}
		if err := strict(body, &in); err != nil || !strings.HasPrefix(in.URL, "http") {
			return http.StatusBadRequest, problemBody("a webhook endpoint is {url}"), nil
		}
		p.webhookURL = in.URL
		return http.StatusOK, in, nil
	case len(parts) == 3 && parts[0] == "events" && parts[2] == "resend":
		for _, event := range p.events {
			if event.ID == parts[1] {
				return http.StatusOK, event, []Event{event}
			}
		}
		return http.StatusNotFound, problemBody("no such event"), nil
	}
	return http.StatusNotFound, problemBody("no such route"), nil
}

// intentEvent records an event about an intent and names it the intent's
// last.
func (p *Provider) intentEvent(eventType string, intent *Intent) Event {
	event := p.event(eventType, Object{
		ID: intent.ID, Object: "payment_intent", Amount: intent.Amount, Currency: intent.Currency,
		Status: intent.Status, Reference: intent.Reference, Attempt: intent.Attempt,
	})
	intent.Event, intent.EventCreated = event.ID, event.Created
	return event
}

func (p *Provider) event(eventType string, object Object) Event {
	event := Event{ID: p.next("evt"), Type: eventType, Created: p.cfg.Now().Unix()}
	event.Data.Object = object
	p.events = append(p.events, event)
	return event
}

// deliver posts an event to the webhook, signed, and returns its status;
// none is sent while no webhook is set.
func (p *Provider) deliver(ctx context.Context, event Event) (int, error) {
	p.mu.Lock()
	url := p.webhookURL
	p.mu.Unlock()
	if url == "" {
		return 0, nil
	}
	body, err := json.Marshal(event)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, Sign(p.cfg.SigningSecret, p.cfg.Now(), body))
	resp, err := p.cfg.Client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		p.mu.Lock()
		p.delivered[event.ID] = true
		p.mu.Unlock()
	}
	return resp.StatusCode, nil
}

// Sign is a webhook's signature header for body, sent at t, under secret.
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + mac(secret, ts, body)
}

func mac(secret, ts string, body []byte) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(ts))
	h.Write([]byte("."))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// ErrSignature is why Verify refuses a webhook.
var ErrSignature = errors.New("acmepay: the webhook's signature does not verify")

// Verify checks a webhook's signature header against its raw body: one of
// its v1 signatures must be the HMAC of "<t>.<body>" under one of secrets,
// compared in constant time, and t within tolerance of now, so an old
// delivery cannot be replayed. More than one secret verifies during a
// rotation: the current one and the next.
func Verify(header string, body []byte, now time.Time, tolerance time.Duration, secrets ...string) error {
	var ts string
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch name {
		case "t":
			ts = value
		case "v1":
			signatures = append(signatures, value)
		}
	}
	seconds, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(signatures) == 0 {
		return fmt.Errorf("%w: the header is not t=<seconds>,v1=<signature>", ErrSignature)
	}
	if skew := now.Sub(time.Unix(seconds, 0)); skew > tolerance || skew < -tolerance {
		return fmt.Errorf("%w: its timestamp is %s from now", ErrSignature, skew.Round(time.Second))
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		want := mac(secret, ts, body)
		for _, got := range signatures {
			if hmac.Equal([]byte(got), []byte(want)) {
				return nil
			}
		}
	}
	return ErrSignature
}

// strict decodes a body that holds only the members of v: a card number,
// or any field acmepay does not name, is refused.
func strict(body []byte, v any) error {
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("the body is not what the route takes: %w", err)
	}
	return nil
}

type problemMessage struct {
	Error string `json:"error"`
}

func problemBody(message string) problemMessage { return problemMessage{Error: message} }

func problem(w http.ResponseWriter, status int, message string) {
	respond(w, status, problemBody(message))
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
