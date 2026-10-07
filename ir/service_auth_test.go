package ir

import (
	"reflect"
	"strings"
	"testing"
)

// localIssuer is the entry the local connector gives a callee: the caller
// is an issuer of its own, verified with the edge's public key.
func localIssuer(caller string) *ServiceAuthIssuer {
	return &ServiceAuthIssuer{
		Issuer:             caller,
		Audience:           "shop-api",
		Algorithms:         []string{AlgorithmEdDSA},
		Keys:               []ServiceAuthKey{{JWK: Output{Resource: caller + ".calls.shop-api.key", Name: "publicJwk"}}},
		MaxLifetimeSeconds: 300,
		Callers:            []ServiceAuthCaller{{Subject: caller, Deployable: caller, Serves: []string{"shop-orders"}}},
	}
}

// googleIssuer is the entry the gcp connector gives a callee: Google's
// issuer, keys fetched from its JWKS URL, the caller by its email.
func googleIssuer(subjects ...any) *ServiceAuthIssuer {
	issuer := &ServiceAuthIssuer{
		Issuer:        "https://accounts.google.com",
		IssuerAliases: []string{"accounts.google.com"},
		Audience:      Concat{"//run.googleapis.com/projects/acme/locations/us-east1/services/shop-api-", Parameter("pr")},
		Algorithms:    []string{AlgorithmRS256},
		JWKSURL:       "https://www.googleapis.com/oauth2/v3/certs",
		SubjectClaim:  "email",
	}
	for i, subject := range subjects {
		issuer.Callers = append(issuer.Callers, ServiceAuthCaller{Subject: subject, Deployable: []string{"Orders", "Billing"}[i], Serves: []string{"shop-orders"}})
	}
	return issuer
}

// TestServiceAuthMeetsItsContract: each connector's entry passes, as the
// typed value and its JSON form, and so does a field of several issuers,
// or none.
func TestServiceAuthMeetsItsContract(t *testing.T) {
	email := Concat{"orders-pr", Parameter("pr"), "@acme.iam.gserviceaccount.com"}
	for name, issuer := range map[string]*ServiceAuthIssuer{
		"local":  localIssuer("Orders"),
		"google": googleIssuer(email, "billing@acme.iam.gserviceaccount.com"),
		"literal key": {
			Issuer: "Orders", Audience: "shop-api", Algorithms: []string{AlgorithmEdDSA},
			Keys:    []ServiceAuthKey{{JWK: `{"kty":"OKP","crv":"Ed25519","kid":"k1","x":"AAAA"}`}},
			Callers: []ServiceAuthCaller{{Subject: "Orders", Deployable: "Orders", Serves: []string{"shop-orders"}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := CheckServiceAuthIssuer(issuer); err != nil {
				t.Fatal(err)
			}
			form, err := jsonForm(issuer)
			if err != nil {
				t.Fatal(err)
			}
			if err := CheckServiceAuthIssuer(form); err != nil {
				t.Fatalf("JSON form: %v", err)
			}
		})
	}
	for name, value := range map[string]ServiceAuth{
		"two issuers": {Issuers: []*ServiceAuthIssuer{googleIssuer(email), localIssuer("Orders")}},
		"no issuers":  {Issuers: []*ServiceAuthIssuer{}},
	} {
		if err := CheckServiceAuth(value); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestServiceAuthThatBreaksItsContract: the error names the member.
func TestServiceAuthThatBreaksItsContract(t *testing.T) {
	edit := func(f func(*ServiceAuthIssuer)) *ServiceAuthIssuer {
		issuer := localIssuer("Orders")
		f(issuer)
		return issuer
	}
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"no issuer", edit(func(i *ServiceAuthIssuer) { i.Issuer = nil }), "issuer is null"},
		{"no audience", edit(func(i *ServiceAuthIssuer) { i.Audience = "" }), `audience is ""`},
		{"no algorithms", edit(func(i *ServiceAuthIssuer) { i.Algorithms = nil }), "algorithms is null"},
		{"an algorithm the verifier lacks", edit(func(i *ServiceAuthIssuer) { i.Algorithms = []string{"HS256"} }), "algorithms[0] is HS256; want RS256, ES256, EdDSA"},
		{"keys and a key set", edit(func(i *ServiceAuthIssuer) { i.JWKSURL = "https://keys" }), "jwksUrl or keys, not both"},
		{"neither keys nor a key set", edit(func(i *ServiceAuthIssuer) { i.Keys = nil }), "sets jwksUrl or keys"},
		{"a private key", edit(func(i *ServiceAuthIssuer) {
			i.Keys = []ServiceAuthKey{{JWK: `{"kty":"OKP","kid":"k","x":"AA","d":"BB"}`}}
		}), "keys[0].jwk holds the private member d"},
		{"a key without a kid", edit(func(i *ServiceAuthIssuer) { i.Keys = []ServiceAuthKey{{JWK: `{"kty":"OKP","x":"AA"}`}} }), "needs a kty and a kid"},
		{"a key that is no JSON", edit(func(i *ServiceAuthIssuer) { i.Keys = []ServiceAuthKey{{JWK: "key"}} }), "is not a JWK's JSON object"},
		{"a negative lifetime", map[string]any{
			"issuer": "Orders", "audience": "shop-api", "algorithms": []any{"EdDSA"}, "jwksUrl": "https://keys", "maxLifetimeSeconds": -1,
			"callers": []any{map[string]any{"subject": "Orders", "deployable": "Orders", "serves": []any{"shop-orders"}}},
		}, "maxLifetimeSeconds is -1"},
		{"an alias that is the issuer", edit(func(i *ServiceAuthIssuer) { i.IssuerAliases = []string{"Orders"} }), "issuerAliases repeats the issuer"},
		{"no callers", edit(func(i *ServiceAuthIssuer) { i.Callers = nil }), "callers is null"},
		{"a caller without a deployable", edit(func(i *ServiceAuthIssuer) { i.Callers[0].Deployable = "" }), "callers[0].deployable"},
		{"a caller that serves nothing", edit(func(i *ServiceAuthIssuer) { i.Callers[0].Serves = nil }), "callers[0].serves is null"},
		{"a subject twice", edit(func(i *ServiceAuthIssuer) { i.Callers = append(i.Callers, i.Callers[0]) }), "callers[1].subject Orders is another caller's"},
		{"a member the contract lacks", map[string]any{"issuer": "Orders", "secret": "x"}, "an issuer has no member secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckServiceAuthIssuer(tc.value)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CheckServiceAuthIssuer = %v, want an error containing %q", err, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"no issuers member", map[string]any{}, "issuers is null"},
		{"two entries with one issuer", ServiceAuth{Issuers: []*ServiceAuthIssuer{localIssuer("Orders"), localIssuer("Orders")}}, `issuers[0] and issuers[1] both name the issuer "Orders"`},
		{"an alias another entry's issuer", ServiceAuth{Issuers: []*ServiceAuthIssuer{
			googleIssuer("a@b"), edit(func(i *ServiceAuthIssuer) { i.Issuer = "accounts.google.com" }),
		}}, `both name the issuer "accounts.google.com"`},
		{"a bad issuer", ServiceAuth{Issuers: []*ServiceAuthIssuer{edit(func(i *ServiceAuthIssuer) { i.Audience = nil })}}, "issuers[0]: audience is null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckServiceAuth(tc.value)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CheckServiceAuth = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestServiceAuthVariables: a callers field is one variable per member, a
// list of objects its length and each object's members under its index,
// a whole number its decimal, and no issuers one variable holding 0.
func TestServiceAuthVariables(t *testing.T) {
	key := Output{Resource: "Orders.calls.shop-api.key", Name: "publicJwk"}
	got, err := DerivedVariables(CallersField("shop-api"), ServiceAuth{Issuers: []*ServiceAuthIssuer{localIssuer("Orders")}})
	if err != nil {
		t.Fatal(err)
	}
	want := []DerivedVariable{
		{Name: "SHOP_API_CALLERS_ISSUERS", Value: "1"},
		{Name: "SHOP_API_CALLERS_ISSUERS_0_ALGORITHMS", Value: "EdDSA"},
		{Name: "SHOP_API_CALLERS_ISSUERS_0_AUDIENCE", Value: "shop-api"},
		{Name: "SHOP_API_CALLERS_ISSUERS_0_CALLERS", Value: "1"},
		{Name: "SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_DEPLOYABLE", Value: "Orders"},
		{Name: "SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_SERVES", Value: "shop-orders"},
		{Name: "SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_SUBJECT", Value: "Orders"},
		{Name: "SHOP_API_CALLERS_ISSUERS_0_ISSUER", Value: "Orders"},
		{Name: "SHOP_API_CALLERS_ISSUERS_0_KEYS", Value: "1"},
		{Name: "SHOP_API_CALLERS_ISSUERS_0_KEYS_0_JWK", Value: key},
		{Name: "SHOP_API_CALLERS_ISSUERS_0_MAX_LIFETIME_SECONDS", Value: "300"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("variables:\n got %#v\nwant %#v", got, want)
	}

	got, err = DerivedVariables("SHOP_API_CALLERS", ServiceAuth{Issuers: []*ServiceAuthIssuer{}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []DerivedVariable{{Name: "SHOP_API_CALLERS_ISSUERS", Value: "0"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("no issuers: got %#v, want %#v", got, want)
	}

	for _, tc := range []struct {
		value any
		want  string
	}{
		{map[string]any{"list": []any{map[string]any{"a": "b"}, "c"}}, "a list holds objects or strings, not both"},
		{map[string]any{"n": 1.5}, "1.5 is not a whole number"},
	} {
		if _, err := DerivedVariables("F", tc.value); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("DerivedVariables(%v) = %v, want an error containing %q", tc.value, err, tc.want)
		}
	}
}

// TestCallersField: an API's callers field is its name in upper snake case
// with _CALLERS, which no other derived field of the core's rule claims.
func TestCallersField(t *testing.T) {
	if got := CallersField("shop-api"); got != "SHOP_API_CALLERS" {
		t.Errorf("CallersField(shop-api) = %s", got)
	}
	names := DerivedFieldNames{}
	for _, kind := range []EdgeKind{EdgeSQL, EdgeHTTP} {
		other := names.Field(kind, "shop-api")
		if DerivedFieldClaims(other, CallersField("shop-api")) || DerivedFieldClaims(CallersField("shop-api"), other) {
			t.Errorf("%s and %s claim each other", other, CallersField("shop-api"))
		}
	}
}

// TestHasServiceCallers: a clause of an operation or of its set counts,
// unless @publicRoute opens the operation.
func TestHasServiceCallers(t *testing.T) {
	clause := &ServiceCallers{Mode: ServiceCallersRequire}
	for _, tc := range []struct {
		name string
		set  *OperationSet
		want bool
	}{
		{"none", &OperationSet{Name: "S", Operations: []*FieldDef{{Name: "op"}}}, false},
		{"the operation's", &OperationSet{Name: "S", Operations: []*FieldDef{{Name: "op", ServiceCallers: clause}}}, true},
		{"the set's", &OperationSet{Name: "S", ServiceCallers: clause, Operations: []*FieldDef{{Name: "op"}}}, true},
		{"the set's on a public route", &OperationSet{Name: "S", ServiceCallers: clause, Operations: []*FieldDef{{Name: "op", Public: true}}}, false},
	} {
		s := &Schema{Kind: SchemaKindAPI, OperationSets: []*OperationSet{tc.set}}
		if got := s.HasServiceCallers(); got != tc.want {
			t.Errorf("%s: HasServiceCallers = %v, want %v", tc.name, got, tc.want)
		}
	}
}
