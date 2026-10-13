package stackconfig

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"reflect"
	"slices"
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

// pemKey is an Ed25519 key pair as the generic connector's key node, a tls
// provider PrivateKey, writes it (D63): the PKCS#8 PEM the caller signs
// with, which its platform's secret store holds, and the
// SubjectPublicKeyInfo PEM the callee's callers field holds.
func pemKey(t *testing.T) (private, public string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: spki}))
}

// TestLoadCallersVerifiesAGenericCaller: the callers field the generic
// connector writes, the caller's public key as a PEM in
// SHOP_API_CALLERS_ISSUERS_0_KEYS_0_PEM, becomes a verifier config whose
// key is the PEM's Ed25519 JWK, its kid its thumbprint. It admits the token
// the caller's SignedToken signs with the PKCS#8 PEM, whose kid is the same
// thumbprint, as Orders, and refuses one signed with another key.
func TestLoadCallersVerifiesAGenericCaller(t *testing.T) {
	private, public := pemKey(t)
	privateKey := private
	env := localCallers("")
	for i, kv := range env {
		if strings.HasPrefix(kv, "SHOP_API_CALLERS_ISSUERS_0_KEYS_0_JWK=") {
			env[i] = "SHOP_API_CALLERS_ISSUERS_0_KEYS_0_PEM=" + public
		}
	}
	cfg, err := loadCallers("SHOP_API_CALLERS", env)
	if err != nil {
		t.Fatal(err)
	}
	key := cfg.Issuers[0].Keys[0]
	if want, _ := serviceauth.PublicJWKFromPEM([]byte(public)); key != want || key.Kty != "OKP" || key.Crv != "Ed25519" || key.Kid == "" {
		t.Fatalf("key = %+v, want the PEM's Ed25519 JWK", key)
	}
	verifier, err := serviceauth.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	authenticate := func(private string) (*serviceauth.Caller, error) {
		t.Helper()
		sign, err := serviceauth.SignedToken([]byte(private), "Orders", "Orders", "shop-api")
		if err != nil {
			t.Fatal(err)
		}
		token, err := sign(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		var header struct {
			Kid string `json:"kid"`
		}
		segment, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
		if err := json.Unmarshal(segment, &header); err != nil {
			t.Fatal(err)
		}
		if mine, _ := serviceauth.PublicJWKFromPEM([]byte(public)); private == privateKey && header.Kid != mine.Kid {
			t.Errorf("token header %s; want the kid %s", segment, mine.Kid)
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
	other, _ := pemKey(t)
	var refusal *serviceauth.Error
	if _, err := authenticate(other); !errors.As(err, &refusal) || refusal.Code != serviceauth.CodeUnauthorized {
		t.Errorf("a token signed with another key: %v, want %s", err, serviceauth.CodeUnauthorized)
	}

	for _, tc := range []struct {
		name string
		edit func([]string) []string
		want string
	}{
		{"a JWK and a PEM", func(env []string) []string {
			return append(env, "SHOP_API_CALLERS_ISSUERS_0_KEYS_0_JWK={}")
		}, "environment variables SHOP_API_CALLERS_ISSUERS_0_KEYS_0_JWK and SHOP_API_CALLERS_ISSUERS_0_KEYS_0_PEM are both set"},
		{"a private key's PEM", func(env []string) []string {
			return append(env, "SHOP_API_CALLERS_ISSUERS_0_KEYS_0_PEM="+private)
		}, "environment variable SHOP_API_CALLERS_ISSUERS_0_KEYS_0_PEM is not an Ed25519 public key's PEM: no PEM PUBLIC KEY block"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadCallers("SHOP_API_CALLERS", tc.edit(slices.Clone(env))); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("loadCallers = %v, want %q", err, tc.want)
			}
		})
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
