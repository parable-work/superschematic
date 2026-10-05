package ir

import (
	"reflect"
	"strings"
	"testing"
)

// TestDerivedValuesMeetTheirContract: each form the contract has passes,
// as the typed value and as its JSON form.
func TestDerivedValuesMeetTheirContract(t *testing.T) {
	instance := Output{Resource: "shop-db.instance", Name: "connectionName"}
	cases := []struct {
		name  string
		kind  EdgeKind
		value any
	}{
		{"connection string", EdgeSQL, DatabaseConnection{URL: "postgres://shop@localhost:5432/shop_db"}},
		{"cloud sql", EdgeSQL, DatabaseConnection{CloudSQL: &CloudSQLConnection{
			Instance: instance,
			Database: Concat{"shop_db_", Parameter("pr")},
			User:     "shop-api@acme.iam",
		}}},
		{"cloud sql as json", EdgeSQL, map[string]any{"cloudSql": map[string]any{
			"instance": map[string]any{"$output": map[string]any{"resource": "shop-db.instance", "name": "connectionName"}},
			"database": "shop_db",
			"user":     "shop-api@acme.iam",
		}}},
		{"url only", EdgeHTTP, ServiceEndpoint{URL: Output{Resource: "shop-api.service", Name: "uri"}}},
		{"google id token", EdgeHTTP, ServiceEndpoint{
			URL: "https://shop-api.run.app",
			Credential: &ServiceCredential{
				Source:   CredentialGoogleIDToken,
				Audience: "https://shop-api.run.app",
				Headers:  []string{"Service-Authorization", "X-Serverless-Authorization"},
			},
		}},
		{"token file", EdgeHTTP, ServiceEndpoint{
			URL:        "http://shop-api",
			Credential: &ServiceCredential{Source: CredentialTokenFile, TokenFile: "/var/run/secrets/tokens/shop-api"},
		}},
		{"signed token", EdgeHTTP, ServiceEndpoint{
			URL: "http://127.0.0.1:8081",
			Credential: &ServiceCredential{
				Source:   CredentialSignedToken,
				Audience: "shop-api",
				Issuer:   "shop-orders",
				Key:      Output{Resource: "shop-orders.key.shop-api", Name: "privateJwk"},
			},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckDerivedValue(tc.kind, tc.value); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestDerivedValuesThatBreakTheContract: the error names the member.
func TestDerivedValuesThatBreakTheContract(t *testing.T) {
	cases := []struct {
		name  string
		kind  EdgeKind
		value any
		want  string
	}{
		{"free-form string", EdgeSQL, "postgres://localhost/shop", "a database connection must be an object"},
		{"neither form", EdgeSQL, DatabaseConnection{}, "sets url or cloudSql"},
		{"both forms", EdgeSQL, DatabaseConnection{URL: "postgres://x", CloudSQL: &CloudSQLConnection{Instance: "a", Database: "b", User: "c"}}, "not both"},
		{"unknown member", EdgeSQL, map[string]any{"instance": "acme:us:shop", "database": "shop_db"}, "has no member database"},
		{"missing cloud sql member", EdgeSQL, DatabaseConnection{CloudSQL: &CloudSQLConnection{Instance: "a", Database: "b"}}, "cloudSql.user is null"},
		{"empty url", EdgeSQL, DatabaseConnection{URL: ""}, `url is ""`},
		{"number", EdgeSQL, map[string]any{"url": 5432}, "url is 5432"},
		{"no url", EdgeHTTP, ServiceEndpoint{}, "url is null"},
		{"old free-form url", EdgeHTTP, map[string]any{"url": "http://x", "audience": "x"}, "has no member audience"},
		{"unknown source", EdgeHTTP, ServiceEndpoint{URL: "http://x", Credential: &ServiceCredential{Source: "api-key"}}, `credential.source is "api-key"`},
		{"source as a reference", EdgeHTTP, map[string]any{"url": "http://x", "credential": map[string]any{"source": map[string]any{"$parameter": "pr"}}}, "credential.source is"},
		{"missing audience", EdgeHTTP, ServiceEndpoint{URL: "http://x", Credential: &ServiceCredential{Source: CredentialGoogleIDToken}}, "credential.audience is missing"},
		{"a member the source does not read", EdgeHTTP, ServiceEndpoint{URL: "http://x", Credential: &ServiceCredential{Source: CredentialTokenFile, TokenFile: "/t", Audience: "x"}}, "credential.audience is set, which a token-file source does not read"},
		{"signed token without key", EdgeHTTP, ServiceEndpoint{URL: "http://x", Credential: &ServiceCredential{Source: CredentialSignedToken, Audience: "a", Issuer: "b"}}, "credential.key is missing"},
		{"headers without the service header", EdgeHTTP, ServiceEndpoint{URL: "http://x", Credential: &ServiceCredential{Source: CredentialTokenFile, TokenFile: "/t", Headers: []string{"Authorization"}}}, "lacks Service-Authorization"},
		{"a header that is not a name", EdgeHTTP, ServiceEndpoint{URL: "http://x", Credential: &ServiceCredential{Source: CredentialTokenFile, TokenFile: "/t", Headers: []string{"Service-Authorization", "A, B"}}}, "headers[1]"},
		{"a header twice", EdgeHTTP, ServiceEndpoint{URL: "http://x", Credential: &ServiceCredential{Source: CredentialTokenFile, TokenFile: "/t", Headers: []string{"Service-Authorization", "service-authorization"}}}, "twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckDerivedValue(tc.kind, tc.value)
			if err == nil {
				t.Fatalf("CheckDerivedValue(%s, %#v) passed", tc.kind, tc.value)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q lacks %q", err, tc.want)
			}
		})
	}
}

// TestDerivedVariables: one variable per member, named by the field and
// the member's path in upper snake case, a list joined with commas.
func TestDerivedVariables(t *testing.T) {
	instance := Output{Resource: "shop-db.instance", Name: "connectionName"}
	got, err := DerivedVariables("SHOP_DB_DATABASE", DatabaseConnection{CloudSQL: &CloudSQLConnection{
		Instance: instance, Database: "shop_db", User: "shop-api@acme.iam",
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []DerivedVariable{
		{Name: "SHOP_DB_DATABASE_CLOUD_SQL_DATABASE", Value: "shop_db"},
		{Name: "SHOP_DB_DATABASE_CLOUD_SQL_INSTANCE", Value: instance},
		{Name: "SHOP_DB_DATABASE_CLOUD_SQL_USER", Value: "shop-api@acme.iam"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("database variables:\n got %#v\nwant %#v", got, want)
	}

	got, err = DerivedVariables("SHOP_API_SERVICE", ServiceEndpoint{
		URL: "https://shop-api.run.app",
		Credential: &ServiceCredential{
			Source:    CredentialTokenFile,
			TokenFile: "/var/run/secrets/tokens/shop-api",
			Headers:   []string{"Service-Authorization", "X-Serverless-Authorization"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want = []DerivedVariable{
		{Name: "SHOP_API_SERVICE_CREDENTIAL_HEADERS", Value: "Service-Authorization,X-Serverless-Authorization"},
		{Name: "SHOP_API_SERVICE_CREDENTIAL_SOURCE", Value: "token-file"},
		{Name: "SHOP_API_SERVICE_CREDENTIAL_TOKEN_FILE", Value: "/var/run/secrets/tokens/shop-api"},
		{Name: "SHOP_API_SERVICE_URL", Value: "https://shop-api.run.app"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("service variables:\n got %#v\nwant %#v", got, want)
	}

	if _, err := DerivedVariables("X", map[string]any{"port": 5432}); err == nil {
		t.Error("a number became a variable")
	}
}

func TestDerivedVariableSegment(t *testing.T) {
	for member, want := range map[string]string{
		"url": "URL", "cloudSql": "CLOUD_SQL", "tokenFile": "TOKEN_FILE", "instance": "INSTANCE", "v2Key": "V2_KEY",
	} {
		if got := DerivedVariableSegment(member); got != want {
			t.Errorf("DerivedVariableSegment(%q) = %q, want %q", member, got, want)
		}
	}
}
