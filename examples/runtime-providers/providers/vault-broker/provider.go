// Package vaultbroker is vault-broker in Go (D159, D184): a credential
// provider over a password manager's service API. Items are named by
// secret reference, vault://<vault>/<item>/<field>, and the broker holds a
// service account token limited to the vaults VAULTS names, which it also
// refuses others by. List gives each item's title and sites; Resolve reads
// one field and seals it in an Injection, which only the runner opens. The
// value is never returned, logged or held as a plain string outside the
// call that seals it.
package vaultbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/providers"
)

var reference = regexp.MustCompile(`^vault://([^/]+)/([^/]+)/([^/]+)$`)

// ParseReference reads a secret reference: vault://<vault>/<item>/<field>.
func ParseReference(ref string) (vault, item, field string, err error) {
	m := reference.FindStringSubmatch(ref)
	if m == nil {
		return "", "", "", fmt.Errorf("vault-broker: %q is no secret reference; one is vault://<vault>/<item>/<field>", ref)
	}
	parts := make([]string, 3)
	for i := range parts {
		if parts[i], err = url.PathUnescape(m[i+1]); err != nil {
			return "", "", "", err
		}
	}
	return parts[0], parts[1], parts[2], nil
}

// usableOn reports whether an item whose sites are urls may be used on
// site: same host.
func usableOn(urls []string, site string) bool {
	host := func(text string) string {
		if !strings.Contains(text, "://") {
			text = "https://" + text
		}
		u, err := url.Parse(text)
		if err != nil {
			return text
		}
		return u.Host
	}
	return slices.ContainsFunc(urls, func(u string) bool { return host(u) == host(site) })
}

// Definition is the provider a Go server links, over client, or
// http.DefaultClient when none is given.
func Definition(client ...*http.Client) providers.Definition {
	httpClient := http.DefaultClient
	if len(client) > 0 && client[0] != nil {
		httpClient = client[0]
	}
	return providers.MustDefine(providers.Definition{
		Name:       "vault-broker",
		Implements: providers.Credentials,
		Config: map[string]providers.SettingSpec{
			"URL":    {Description: "The password manager's service API."},
			"TOKEN":  {Secret: true, Description: "A service account token the password manager limits to the vaults below."},
			"VAULTS": {Description: "The vaults a run may use, comma-separated; every other is refused here as well."},
		},
		Create: func(_ context.Context, settings providers.Settings, pc providers.Context) (any, error) {
			base, err := settings.Text("URL")
			if err != nil {
				return nil, err
			}
			token, err := settings.Secret("TOKEN")
			if err != nil {
				return nil, err
			}
			named, err := settings.Text("VAULTS")
			if err != nil {
				return nil, err
			}
			var vaults []string
			for _, v := range strings.Split(named, ",") {
				if v = strings.TrimSpace(v); v != "" {
					vaults = append(vaults, v)
				}
			}
			return &broker{base: strings.TrimRight(base, "/"), token: token, vaults: vaults, client: httpClient, log: pc.Logger}, nil
		},
	})
}

type broker struct {
	base   string
	token  providers.SecretValue
	vaults []string
	client *http.Client
	log    *zap.Logger
}

type item struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	URLs   []string `json:"urls"`
	Fields []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"fields"`
}

func (b *broker) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.token.Reveal())
	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("vault-broker: the service answered %d for %s", res.StatusCode, path)
	}
	return json.NewDecoder(res.Body).Decode(into)
}

func (b *broker) List(ctx context.Context, query providers.CredentialQuery) ([]providers.CredentialListing, error) {
	var out []providers.CredentialListing
	for _, vault := range b.vaults {
		var items []item
		if err := b.get(ctx, "/v1/vaults/"+url.PathEscape(vault)+"/items", &items); err != nil {
			return nil, err
		}
		for _, it := range items {
			if query.Site != "" && !usableOn(it.URLs, query.Site) {
				continue
			}
			// Each item's password, the field a sign-in fills.
			out = append(out, providers.CredentialListing{Ref: "vault://" + url.PathEscape(vault) + "/" + url.PathEscape(it.ID) + "/password", Title: it.Title, Sites: append([]string{}, it.URLs...)})
		}
	}
	return out, nil
}

func (b *broker) Resolve(ctx context.Context, ref string, target providers.InjectionTarget) (*providers.Injection, error) {
	vault, itemID, label, err := ParseReference(ref)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(b.vaults, vault) {
		return nil, fmt.Errorf("vault-broker: vault %s is not one this broker may read (%s)", vault, strings.Join(b.vaults, ", "))
	}
	var it item
	if err := b.get(ctx, "/v1/vaults/"+url.PathEscape(vault)+"/items/"+url.PathEscape(itemID), &it); err != nil {
		return nil, err
	}
	if !usableOn(it.URLs, target.Site) {
		return nil, fmt.Errorf("vault-broker: %s is not for %s", ref, target.Site)
	}
	for _, f := range it.Fields {
		if f.Label != label {
			continue
		}
		value := providers.NewSecret(f.Value)
		b.log.Info("credential resolved", zap.String("ref", ref), zap.String("site", target.Site), zap.String("kind", target.Kind))
		if target.Kind == providers.TargetFill {
			return providers.FillInjection(ref, target.Site, target.Selector, value), nil
		}
		return providers.HeaderInjection(ref, target.Site, target.Name, value), nil
	}
	return nil, fmt.Errorf("vault-broker: %s names no field %s", ref, label)
}
