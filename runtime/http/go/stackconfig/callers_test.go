package stackconfig

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
)

// edgeKey is an Ed25519 key pair as the local target's key pair node
// writes it: the private JWK the caller signs with, and the public JWK the
// callee's callers field holds.
func edgeKey(t *testing.T, kid string) (private, public []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	x := base64.RawURLEncoding.EncodeToString(pub)
	public, err = json.Marshal(map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": kid, "alg": "EdDSA", "use": "sig", "x": x})
	if err != nil {
		t.Fatal(err)
	}
	private, err = json.Marshal(map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": kid, "x": x, "d": base64.RawURLEncoding.EncodeToString(priv.Seed())})
	if err != nil {
		t.Fatal(err)
	}
	return private, public
}

// localCallers is SHOP_API_CALLERS as a local environment sets it: Orders
// is an issuer of its own, verified with its edge's public key.
func localCallers(publicJWK string) []string {
	return []string{
		"SHOP_API_CALLERS_ISSUERS=1",
		"SHOP_API_CALLERS_ISSUERS_0_ALGORITHMS=EdDSA",
		"SHOP_API_CALLERS_ISSUERS_0_AUDIENCE=shop-api",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS=1",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_DEPLOYABLE=Orders",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_SERVES=shop-orders",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_SUBJECT=Orders",
		"SHOP_API_CALLERS_ISSUERS_0_ISSUER=Orders",
		"SHOP_API_CALLERS_ISSUERS_0_KEYS=1",
		"SHOP_API_CALLERS_ISSUERS_0_KEYS_0_JWK=" + publicJWK,
		"SHOP_API_CALLERS_ISSUERS_0_MAX_LIFETIME_SECONDS=300",
		"SHOP_API_SERVICE_URL=http://unrelated",
	}
}

// TestLoadCallersVerifiesTheEdgesCaller: a local environment's callers
// field becomes a verifier config that admits the token the caller signs
// with its edge's key as Orders, and refuses one signed with another key.
func TestLoadCallersVerifiesTheEdgesCaller(t *testing.T) {
	private, public := edgeKey(t, "orders-key")
	cfg, err := loadCallers("SHOP_API_CALLERS", localCallers(string(public)))
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := serviceauth.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	authenticate := func(private []byte) (*serviceauth.Caller, error) {
		t.Helper()
		sign, err := serviceauth.SignedToken(private, "Orders", "Orders", "shop-api")
		if err != nil {
			t.Fatal(err)
		}
		token, err := sign(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		r, _ := http.NewRequest(http.MethodPost, "http://shop-api/api/stock/reindex", nil)
		r.Header.Set(serviceauth.HeaderName, "Bearer "+token)
		return verifier.Authenticate(r)
	}
	caller, err := authenticate(private)
	if err != nil {
		t.Fatal(err)
	}
	if want := (&serviceauth.Caller{Deployable: "Orders", Serves: []string{"shop-orders"}, Subject: "Orders"}); !reflect.DeepEqual(caller, want) {
		t.Errorf("caller = %+v, want %+v", caller, want)
	}
	other, _ := edgeKey(t, "orders-key")
	var refusal *serviceauth.Error
	if _, err := authenticate(other); !errors.As(err, &refusal) || refusal.Code != serviceauth.CodeUnauthorized {
		t.Errorf("a token signed with another key: %v, want %s", err, serviceauth.CodeUnauthorized)
	}
}

// TestLoadCallers reads a Google issuer with two callers, and no issuers.
func TestLoadCallers(t *testing.T) {
	cfg, err := loadCallers("SHOP_API_CALLERS", []string{
		"SHOP_API_CALLERS_ISSUERS=1",
		"SHOP_API_CALLERS_ISSUERS_0_ISSUER=https://accounts.google.com",
		"SHOP_API_CALLERS_ISSUERS_0_ISSUER_ALIASES=accounts.google.com",
		"SHOP_API_CALLERS_ISSUERS_0_AUDIENCE=//run.googleapis.com/projects/acme/locations/us-east1/services/shop-api",
		"SHOP_API_CALLERS_ISSUERS_0_ALGORITHMS=RS256",
		"SHOP_API_CALLERS_ISSUERS_0_JWKS_URL=https://www.googleapis.com/oauth2/v3/certs",
		"SHOP_API_CALLERS_ISSUERS_0_SUBJECT_CLAIM=email",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS=2",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_SUBJECT=billing@acme.iam.gserviceaccount.com",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_DEPLOYABLE=Billing",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_SERVES=billing-api,invoices-api",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_1_SUBJECT=orders@acme.iam.gserviceaccount.com",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_1_DEPLOYABLE=Orders",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_1_SERVES=shop-orders",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := serviceauth.Config{Issuers: []serviceauth.IssuerConfig{{
		Issuer:        "https://accounts.google.com",
		IssuerAliases: []string{"accounts.google.com"},
		Audience:      "//run.googleapis.com/projects/acme/locations/us-east1/services/shop-api",
		Algorithms:    []string{"RS256"},
		JWKSURL:       "https://www.googleapis.com/oauth2/v3/certs",
		SubjectClaim:  "email",
		Callers: map[string]serviceauth.CallerConfig{
			"billing@acme.iam.gserviceaccount.com": {Deployable: "Billing", Serves: []string{"billing-api", "invoices-api"}},
			"orders@acme.iam.gserviceaccount.com":  {Deployable: "Orders", Serves: []string{"shop-orders"}},
		},
	}}}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("config:\n got %+v\nwant %+v", cfg, want)
	}
	if _, err := serviceauth.New(cfg); err != nil {
		t.Errorf("the verifier refuses the config: %v", err)
	}

	cfg, err = loadCallers("SHOP_API_CALLERS", []string{"SHOP_API_CALLERS_ISSUERS=0"})
	if err != nil || len(cfg.Issuers) != 0 || cfg.Issuers == nil {
		t.Errorf("no issuers: %+v, %v", cfg, err)
	}
}

// TestLoadCallersRefusals: the error names the variable at fault.
func TestLoadCallersRefusals(t *testing.T) {
	_, public := edgeKey(t, "k")
	edit := func(drop string, add ...string) []string {
		var out []string
		for _, kv := range localCallers(string(public)) {
			if drop == "" || !strings.HasPrefix(kv, drop+"=") {
				out = append(out, kv)
			}
		}
		return append(out, add...)
	}
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"unset", nil, "required environment variable SHOP_API_CALLERS_ISSUERS is not set"},
		{"not a count", []string{"SHOP_API_CALLERS_ISSUERS=many"}, `SHOP_API_CALLERS_ISSUERS is "many"`},
		{"an issuer short", edit("SHOP_API_CALLERS_ISSUERS", "SHOP_API_CALLERS_ISSUERS=2"), "required environment variable SHOP_API_CALLERS_ISSUERS_1_ISSUER is not set"},
		{"an issuer over", edit("SHOP_API_CALLERS_ISSUERS", "SHOP_API_CALLERS_ISSUERS=0"), "environment variable SHOP_API_CALLERS_ISSUERS_0_AUDIENCE is no member of the callers field SHOP_API_CALLERS"},
		{"no audience", edit("SHOP_API_CALLERS_ISSUERS_0_AUDIENCE"), "SHOP_API_CALLERS_ISSUERS_0_AUDIENCE is not set"},
		{"no callers", edit("SHOP_API_CALLERS_ISSUERS_0_CALLERS", "SHOP_API_CALLERS_ISSUERS_0_CALLERS=0"), `SHOP_API_CALLERS_ISSUERS_0_CALLERS is "0"; want a number of entries from 1`},
		{"a key that is no JWK", edit("SHOP_API_CALLERS_ISSUERS_0_KEYS_0_JWK", "SHOP_API_CALLERS_ISSUERS_0_KEYS_0_JWK=key"), "SHOP_API_CALLERS_ISSUERS_0_KEYS_0_JWK is not a JWK"},
		{"a bad lifetime", edit("SHOP_API_CALLERS_ISSUERS_0_MAX_LIFETIME_SECONDS", "SHOP_API_CALLERS_ISSUERS_0_MAX_LIFETIME_SECONDS=soon"), `MAX_LIFETIME_SECONDS is "soon"`},
		{"an empty serves entry", edit("SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_SERVES", "SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_SERVES=shop-orders,"), "SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_SERVES has an empty entry"},
		{"a stray variable", edit("", "SHOP_API_CALLERS_ISSUERS_0_SECRET=x"), "SHOP_API_CALLERS_ISSUERS_0_SECRET is no member"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadCallers("SHOP_API_CALLERS", tc.env)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("loadCallers = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestLoadCallersFromTheEnvironment reads the process's environment.
func TestLoadCallersFromTheEnvironment(t *testing.T) {
	t.Setenv("PAYMENTS"+CallersSuffix+"_ISSUERS", "0")
	if cfg, err := LoadCallers("PAYMENTS" + CallersSuffix); err != nil || len(cfg.Issuers) != 0 {
		t.Errorf("LoadCallers = %+v, %v", cfg, err)
	}
}
