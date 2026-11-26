// Package smsrelayb is sms-relay-b in Go (D159, D184): a second SMS
// channel adapter, for a relay of another shape: form posts to
// <URL>/accounts/<ACCOUNT>/send with basic authentication, and no
// callbacks. An environment swaps sms-relay-a for it by its config alone:
// the slot's value and the settings this one reads.
package smsrelayb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/parable-work/superschematic/runtime/http/go/providers"
)

// Definition is the provider a Go server links, over client, or
// http.DefaultClient when none is given.
func Definition(client ...*http.Client) providers.Definition {
	httpClient := http.DefaultClient
	if len(client) > 0 && client[0] != nil {
		httpClient = client[0]
	}
	return providers.MustDefine(providers.Definition{
		Name:       "sms-relay-b",
		Implements: providers.Channel,
		Config: map[string]providers.SettingSpec{
			"URL":     {Description: "The relay's base URL."},
			"ACCOUNT": {Description: "The account messages are sent under."},
			"TOKEN":   {Secret: true, Description: "The account's auth token."},
		},
		Create: func(_ context.Context, settings providers.Settings, _ providers.Context) (any, error) {
			base, err := settings.Text("URL")
			if err != nil {
				return nil, err
			}
			account, err := settings.Text("ACCOUNT")
			if err != nil {
				return nil, err
			}
			token, err := settings.Secret("TOKEN")
			if err != nil {
				return nil, err
			}
			return &adapter{base: strings.TrimRight(base, "/"), account: account, token: token, client: httpClient}, nil
		},
	})
}

type adapter struct {
	base, account string
	token         providers.SecretValue
	client        *http.Client
}

func (a *adapter) Kind() string { return "sms" }

func (a *adapter) request(ctx context.Context, method, path, form string) (*http.Response, error) {
	var body *strings.Reader
	if form != "" {
		body = strings.NewReader(form)
	} else {
		body = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(a.account, a.token.Reveal())
	if form != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return a.client.Do(req)
}

func (a *adapter) Send(ctx context.Context, message providers.OutboundMessage) (providers.SendResult, error) {
	form := url.Values{"To": {message.To}, "Body": {message.Body}}
	res, err := a.request(ctx, http.MethodPost, "/accounts/"+url.PathEscape(a.account)+"/send", form.Encode())
	if err != nil {
		return providers.SendResult{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return providers.SendResult{}, fmt.Errorf("sms-relay-b: the relay answered %d", res.StatusCode)
	}
	var sent struct {
		SID string `json:"sid"`
	}
	if err := json.NewDecoder(res.Body).Decode(&sent); err != nil {
		return providers.SendResult{}, err
	}
	return providers.SendResult{ProviderMessageID: sent.SID, State: providers.DeliverySent}, nil
}

func (a *adapter) Health(ctx context.Context) (providers.ChannelHealth, error) {
	res, err := a.request(ctx, http.MethodGet, "/accounts/"+url.PathEscape(a.account), "")
	if err != nil {
		return providers.ChannelHealth{}, err
	}
	_ = res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return providers.ChannelHealth{OK: false, Detail: fmt.Sprintf("the relay answered %d", res.StatusCode)}, nil
	}
	return providers.ChannelHealth{OK: true}, nil
}
