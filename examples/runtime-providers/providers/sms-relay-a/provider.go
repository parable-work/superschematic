// Package smsrelaya is sms-relay-a in Go (D159, D184): a channel adapter
// for an SMS relay that takes JSON. It sends with POST <URL>/v1/messages
// and a Bearer token, and reads the relay's callbacks, delivery statuses
// and inbound messages, from the request a @webhook route received, after
// checking the relay's HMAC-SHA256 signature of the body with the same
// token. An inbound text's media it fetches from the relay, and STOP and
// START it reads as an opt-out and an opt-in (D157). It is the TypeScript
// package's src/index.ts, which package.json declares for both.
package smsrelaya

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
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/providers"
)

var (
	optOut = map[string]bool{"STOP": true, "STOPALL": true, "UNSUBSCRIBE": true, "CANCEL": true, "END": true, "QUIT": true}
	optIn  = map[string]bool{"START": true, "UNSTOP": true}
	states = map[string]providers.DeliveryState{"accepted": providers.DeliveryQueued, "sent": providers.DeliverySent, "delivered": providers.DeliveryDelivered, "undelivered": providers.DeliveryFailed, "failed": providers.DeliveryFailed}
)

func stateOf(status string) providers.DeliveryState {
	if s, ok := states[status]; ok {
		return s
	}
	return providers.DeliveryQueued
}

// Definition is the provider a Go server links, over client, or
// http.DefaultClient when none is given.
func Definition(client ...*http.Client) providers.Definition {
	httpClient := http.DefaultClient
	if len(client) > 0 && client[0] != nil {
		httpClient = client[0]
	}
	return providers.MustDefine(providers.Definition{
		Name:       "sms-relay-a",
		Implements: providers.Channel,
		Config: map[string]providers.SettingSpec{
			"URL":               {Description: "The relay's base URL."},
			"TOKEN":             {Secret: true, Description: "The relay's API token, which also signs its callbacks."},
			"SENDER":            {Optional: true, Description: "The number messages are sent from; the relay's default when unset."},
			providers.CallerKey: {Secret: true, Optional: true, Description: "The key the relay calls the app with, to read the deliveries it sent (#744): the API resolves a request bearing it to this provider's principal, which may relay messages and nothing else. An environment that lets the relay call nothing leaves it unset."},
		},
		Role: []string{"messages.relay"},
		Create: func(_ context.Context, settings providers.Settings, pc providers.Context) (any, error) {
			base, err := settings.Text("URL")
			if err != nil {
				return nil, err
			}
			token, err := settings.Secret("TOKEN")
			if err != nil {
				return nil, err
			}
			sender, _, err := settings.OptionalText("SENDER")
			if err != nil {
				return nil, err
			}
			return &adapter{base: strings.TrimRight(base, "/"), token: token, sender: sender, client: httpClient, log: pc.Logger}, nil
		},
	})
}

type adapter struct {
	base   string
	token  providers.SecretValue
	sender string
	client *http.Client
	log    *zap.Logger
}

func (a *adapter) Kind() string { return "sms" }

func (a *adapter) do(ctx context.Context, method, url string, body any, headers map[string]string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token.Reveal())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return a.client.Do(req)
}

func (a *adapter) Send(ctx context.Context, message providers.OutboundMessage) (providers.SendResult, error) {
	body := map[string]string{"to": message.To, "text": message.Body}
	if a.sender != "" {
		body["from"] = a.sender
	}
	if message.Reference != "" {
		body["reference"] = message.Reference
	}
	headers := map[string]string{}
	if message.IdempotencyKey != "" {
		headers["Idempotency-Key"] = message.IdempotencyKey
	}
	res, err := a.do(ctx, http.MethodPost, a.base+"/v1/messages", body, headers)
	if err != nil {
		return providers.SendResult{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return providers.SendResult{}, fmt.Errorf("sms-relay-a: the relay answered %d", res.StatusCode)
	}
	var sent struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(res.Body).Decode(&sent); err != nil {
		return providers.SendResult{}, err
	}
	a.log.Info("sms sent", zap.String("messageId", sent.ID))
	return providers.SendResult{ProviderMessageID: sent.ID, State: stateOf(sent.Status)}, nil
}

func (a *adapter) Health(ctx context.Context) (providers.ChannelHealth, error) {
	res, err := a.do(ctx, http.MethodGet, a.base+"/v1/health", nil, nil)
	if err != nil {
		return providers.ChannelHealth{}, err
	}
	_ = res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return providers.ChannelHealth{OK: false, Detail: fmt.Sprintf("the relay answered %d", res.StatusCode)}, nil
	}
	return providers.ChannelHealth{OK: true}, nil
}

// verified is the callback's body, once its signature is the relay
// token's.
func (a *adapter) verified(request providers.WebhookRequest, into any) error {
	mac := hmac.New(sha256.New, []byte(a.token.Reveal()))
	mac.Write([]byte(request.Body))
	if !hmac.Equal([]byte(request.Headers["x-relay-signature"]), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return errors.New("sms-relay-a: the callback is not signed with the relay token")
	}
	return json.Unmarshal([]byte(request.Body), into)
}

func (a *adapter) Status(_ context.Context, request providers.WebhookRequest) ([]providers.DeliveryStatus, error) {
	var body struct {
		Events []struct {
			ID     string    `json:"id"`
			Status string    `json:"status"`
			At     time.Time `json:"at"`
			Error  string    `json:"error"`
		} `json:"events"`
	}
	if err := a.verified(request, &body); err != nil {
		return nil, err
	}
	out := make([]providers.DeliveryStatus, 0, len(body.Events))
	for _, e := range body.Events {
		out = append(out, providers.DeliveryStatus{ProviderMessageID: e.ID, State: stateOf(e.Status), At: e.At, Detail: e.Error})
	}
	return out, nil
}

func (a *adapter) Inbound(ctx context.Context, request providers.WebhookRequest) ([]providers.InboundMessage, error) {
	var body struct {
		Messages []struct {
			ID      string    `json:"id"`
			From    string    `json:"from"`
			Text    string    `json:"text"`
			At      time.Time `json:"at"`
			ReplyTo string    `json:"replyTo"`
			Media   []struct {
				URL  string `json:"url"`
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"media"`
		} `json:"messages"`
	}
	if err := a.verified(request, &body); err != nil {
		return nil, err
	}
	out := make([]providers.InboundMessage, 0, len(body.Messages))
	for _, m := range body.Messages {
		// A photo the text came with is the relay's to keep: the adapter
		// fetches its bytes, with the relay's token, so the core files them
		// (D157).
		var attachments []providers.InboundAttachment
		for i, media := range m.Media {
			res, err := a.do(ctx, http.MethodGet, media.URL, nil, nil)
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if err != nil {
				return nil, err
			}
			if res.StatusCode < 200 || res.StatusCode > 299 {
				return nil, fmt.Errorf("sms-relay-a: the relay's media answered %d", res.StatusCode)
			}
			name := media.Name
			if name == "" {
				name = fmt.Sprintf("media-%d", i+1)
			}
			attachments = append(attachments, providers.InboundAttachment{Name: name, MediaType: media.Type, Bytes: data})
		}
		message := providers.InboundMessage{From: m.From, Body: m.Text, ReceivedAt: m.At, ProviderMessageID: m.ID, InReplyTo: m.ReplyTo, Attachments: attachments}
		// The opt-out and opt-in keywords of SMS are the adapter's to read;
		// the core records them and sends nothing after STOP.
		switch keyword := strings.ToUpper(strings.TrimSpace(m.Text)); {
		case optOut[keyword]:
			message.OptOut = providers.OptOut
		case optIn[keyword]:
			message.OptOut = providers.OptIn
		}
		out = append(out, message)
	}
	return out, nil
}
