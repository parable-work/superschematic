package ir

import (
	"reflect"
	"slices"
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
		{"a bucket on its provider", EdgeBucket, BucketConnection{Name: Output{Resource: "shop-media.bucket", Name: "name"}}},
		{"a bucket on an emulator", EdgeBucket, BucketConnection{Name: "shop-media", Endpoint: "http://127.0.0.1:24443"}},
		{"a bucket as json", EdgeBucket, map[string]any{"name": Concat{"acme-shop-media-", Parameter("pr")}}},
		{"a d1 binding", EdgeSQL, DatabaseConnection{D1: &D1Connection{
			Binding: "SHOP_DB_DATABASE", Database: Output{Resource: "shop-db.database", Name: "uuid"},
		}}},
		{"a service binding", EdgeHTTP, ServiceEndpoint{
			URL:     "https://shop-api",
			Binding: &ServiceBinding{Name: "SHOP_API_SERVICE", Worker: "shop-api"},
			Credential: &ServiceCredential{
				Source: CredentialSignedToken, Audience: "shop-api", Issuer: "Storefront",
				Key: Output{Resource: "Storefront.calls.shop-api.key", Name: "privateKeyPemPkcs8"},
			},
		}},
		{"an r2 binding with its credential", EdgeBucket, BucketConnection{
			Name:    Output{Resource: "shop-media.bucket", Name: "name"},
			Binding: "SHOP_MEDIA_BUCKET",
			R2: &R2Credential{
				Account:     "0123456789abcdef0123456789abcdef",
				AccessKeyID: Output{Resource: "Storefront.shop-media.token", Name: "id"},
				Token:       Output{Resource: "Storefront.shop-media.token", Name: "value"},
			},
		}},
		{"an r2 binding alone", EdgeBucket, BucketConnection{Name: "shop-media", Binding: "SHOP_MEDIA_BUCKET"}},
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
		{"neither form", EdgeSQL, DatabaseConnection{}, "sets url, cloudSql or d1"},
		{"both forms", EdgeSQL, DatabaseConnection{URL: "postgres://x", CloudSQL: &CloudSQLConnection{Instance: "a", Database: "b", User: "c"}}, "not url and cloudSql"},
		{"a url and a d1 binding", EdgeSQL, DatabaseConnection{URL: "postgres://x", D1: &D1Connection{Binding: "B", Database: "d"}}, "not url and d1"},
		{"a d1 binding without its database", EdgeSQL, DatabaseConnection{D1: &D1Connection{Binding: "B"}}, "d1.database is null"},
		{"a d1 binding's unknown member", EdgeSQL, map[string]any{"d1": map[string]any{"binding": "B", "database": "d", "account": "a"}}, "d1 has no member account"},
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
		{"a bucket without a name", EdgeBucket, BucketConnection{Endpoint: "http://127.0.0.1:24443"}, "name is null"},
		{"a bucket with a credential", EdgeBucket, map[string]any{"name": "shop-media", "key": "secret"}, "a bucket connection has no member key"},
		{"an empty endpoint", EdgeBucket, map[string]any{"name": "shop-media", "endpoint": ""}, `endpoint is ""`},
		{"a service binding without its worker", EdgeHTTP, ServiceEndpoint{URL: "https://x", Binding: &ServiceBinding{Name: "X_SERVICE"}}, "binding.worker is null"},
		{"an r2 credential without a binding", EdgeBucket, BucketConnection{Name: "m", R2: &R2Credential{Account: "a", AccessKeyID: "i", Token: Output{Resource: "t", Name: "value"}}}, "with r2 sets binding"},
		{"an r2 credential without its token", EdgeBucket, BucketConnection{Name: "m", Binding: "B", R2: &R2Credential{Account: "a", AccessKeyID: "i"}}, "r2.token is null"},
		{"an empty r2 jurisdiction", EdgeBucket, BucketConnection{Name: "m", Binding: "B", R2: &R2Credential{Account: "a", Jurisdiction: "", AccessKeyID: "i", Token: "t"}}, `r2.jurisdiction is ""`},
		{"an r2 binding on an emulator", EdgeBucket, BucketConnection{Name: "m", Binding: "B", Endpoint: "http://127.0.0.1:24443"}, "endpoint or binding, not both"},
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

	got, err = DerivedVariables("SHOP_MEDIA_BUCKET", BucketConnection{Name: "shop-media", Endpoint: "http://127.0.0.1:24443"})
	if err != nil {
		t.Fatal(err)
	}
	want = []DerivedVariable{
		{Name: "SHOP_MEDIA_BUCKET_ENDPOINT", Value: "http://127.0.0.1:24443"},
		{Name: "SHOP_MEDIA_BUCKET_NAME", Value: "shop-media"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bucket variables:\n got %#v\nwant %#v", got, want)
	}

	got, err = DerivedVariables("X", map[string]any{"port": 5432})
	if want := []DerivedVariable{{Name: "X_PORT", Value: "5432"}}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("a whole number: got %#v, %v; want its decimal", got, err)
	}
	if _, err := DerivedVariables("X", map[string]any{"tls": true}); err == nil {
		t.Error("a boolean became a variable")
	}
}

// TestDerivedVariablesMarkSecretMembers: the variable of a signed-token
// credential's key is Secret, which a platform sets from its secret store,
// and no other variable is (D63).
func TestDerivedVariablesMarkSecretMembers(t *testing.T) {
	key := Output{Resource: "Orders.calls.shop-edge.key", Name: "privateKeyPemPkcs8"}
	got, err := DerivedVariables("SHOP_EDGE_SERVICE", ServiceEndpoint{
		URL:        "https://shop-edge.acme.dev",
		Credential: &ServiceCredential{Source: CredentialSignedToken, Audience: "shop-edge", Issuer: "Orders", Key: key},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []DerivedVariable{
		{Name: "SHOP_EDGE_SERVICE_CREDENTIAL_AUDIENCE", Value: "shop-edge"},
		{Name: "SHOP_EDGE_SERVICE_CREDENTIAL_ISSUER", Value: "Orders"},
		{Name: "SHOP_EDGE_SERVICE_CREDENTIAL_KEY", Value: key, Secret: true},
		{Name: "SHOP_EDGE_SERVICE_CREDENTIAL_SOURCE", Value: "signed-token"},
		{Name: "SHOP_EDGE_SERVICE_URL", Value: "https://shop-edge.acme.dev"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("variables:\n got %#v\nwant %#v", got, want)
	}
	secrets := []string{"credential.key", "r2.token"}
	for _, path := range secrets {
		if !SecretDerivedMember(path) {
			t.Errorf("%s is no secret member", path)
		}
	}
	for _, kind := range []EdgeKind{EdgeSQL, EdgeHTTP, EdgeSite, EdgeBucket} {
		for _, path := range DerivedMembers(kind) {
			if SecretDerivedMember(path) != slices.Contains(secrets, path) {
				t.Errorf("%s %s: SecretDerivedMember = %v", kind, path, SecretDerivedMember(path))
			}
		}
	}
	// An R2 credential's token is one too (D64).
	token := Output{Resource: "Storefront.shop-media.token", Name: "value"}
	got, err = DerivedVariables("SHOP_MEDIA_BUCKET", BucketConnection{
		Name: "shop-media", Binding: "SHOP_MEDIA_BUCKET",
		R2: &R2Credential{Account: "acct", AccessKeyID: "id", Token: token},
	})
	if err != nil {
		t.Fatal(err)
	}
	want = []DerivedVariable{
		{Name: "SHOP_MEDIA_BUCKET_BINDING", Value: "SHOP_MEDIA_BUCKET"},
		{Name: "SHOP_MEDIA_BUCKET_NAME", Value: "shop-media"},
		{Name: "SHOP_MEDIA_BUCKET_R2_ACCESS_KEY_ID", Value: "id"},
		{Name: "SHOP_MEDIA_BUCKET_R2_ACCOUNT", Value: "acct"},
		{Name: "SHOP_MEDIA_BUCKET_R2_TOKEN", Value: token, Secret: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("r2 variables:\n got %#v\nwant %#v", got, want)
	}
	// A jurisdiction is one more plain variable (D64, amended).
	got, err = DerivedVariables("SHOP_MEDIA_BUCKET", BucketConnection{
		Name: "shop-media", Binding: "SHOP_MEDIA_BUCKET",
		R2: &R2Credential{Account: "acct", Jurisdiction: "eu", AccessKeyID: "id", Token: token},
	})
	if err != nil {
		t.Fatal(err)
	}
	want = slices.Insert(want, 4, DerivedVariable{Name: "SHOP_MEDIA_BUCKET_R2_JURISDICTION", Value: "eu"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("r2 variables with a jurisdiction:\n got %#v\nwant %#v", got, want)
	}
	// A member of the same name deeper in another value is no secret.
	got, err = DerivedVariables("F", map[string]any{"a": map[string]any{"credential": map[string]any{"key": "k"}}})
	if err != nil || len(got) != 1 || got[0].Secret {
		t.Errorf("a.credential.key: %#v, %v", got, err)
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

// TestDerivedFieldNames: the core's rule, a naming file's templates, and
// the templates Validate refuses.
func TestDerivedFieldNames(t *testing.T) {
	var core DerivedFieldNames
	if got := core.Field(EdgeSQL, "shop-db"); got != "SHOP_DB_DATABASE" {
		t.Errorf("core sql field = %s", got)
	}
	if got := core.Field(EdgeHTTP, "shop.api"); got != "SHOP_API_SERVICE" {
		t.Errorf("core http field = %s", got)
	}
	if got := core.Field(EdgeBucket, "shop-media"); got != "SHOP_MEDIA_BUCKET" {
		t.Errorf("core bucket field = %s", got)
	}
	named := DerivedFieldNames{Database: "DB_{SERVICE}", Service: "{SERVICE}_API", Bucket: "STORE_{SERVICE}"}
	if err := named.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := named.Field(EdgeSQL, "shop-db"); got != "DB_SHOP_DB" {
		t.Errorf("named sql field = %s", got)
	}
	if got := named.Field(EdgeHTTP, "shop-api"); got != "SHOP_API_API" {
		t.Errorf("named http field = %s", got)
	}
	if got := named.Field(EdgeBucket, "shop-media"); got != "STORE_SHOP_MEDIA" {
		t.Errorf("named bucket field = %s", got)
	}
	for _, bad := range []DerivedFieldNames{
		{Database: "DATABASE"},
		{Service: "{SERVICE}_{SERVICE}"},
		{Service: "{SERVICE}-url"},
		{Database: "9{SERVICE}"},
		{Bucket: "BUCKET"},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("Validate(%+v) passed", bad)
		}
	}
}

func TestDerivedFieldClaims(t *testing.T) {
	for name, want := range map[string]bool{
		"SHOP_DB_DATABASE":         true,
		"SHOP_DB_DATABASE_URL":     true,
		"SHOP_DB_DATABASE_TIMEOUT": true,
		"SHOP_DB_DATABASES":        false,
		"SHOP_DB":                  false,
	} {
		if got := DerivedFieldClaims("SHOP_DB_DATABASE", name); got != want {
			t.Errorf("DerivedFieldClaims(SHOP_DB_DATABASE, %s) = %v, want %v", name, got, want)
		}
	}
}

// TestDerivedConfigFields: an API's database comes from its authDb, or its
// one DB-kind dependency, each calls entry adds a service field, and each
// buckets entry a bucket field (D54).
func TestDerivedConfigFields(t *testing.T) {
	api := &Schema{Name: "shop-orders", Kind: SchemaKindAPI, AuthDB: "shop-db",
		Calls:   []ServiceRef{{Name: "shop-api", Kind: SchemaKindAPI}, {Name: "payments", Kind: SchemaKindAPI}},
		Buckets: []ServiceRef{{Name: "shop-media", Kind: SchemaKindBucket}}}
	want := []DerivedConfigField{
		{Name: "SHOP_DB_DATABASE", Kind: EdgeSQL, Service: "shop-db", From: "authDb"},
		{Name: "SHOP_API_SERVICE", Kind: EdgeHTTP, Service: "shop-api", From: "calls"},
		{Name: "PAYMENTS_SERVICE", Kind: EdgeHTTP, Service: "payments", From: "calls"},
		{Name: "SHOP_MEDIA_BUCKET", Kind: EdgeBucket, Service: "shop-media", From: "buckets"},
	}
	if got := api.DerivedConfigFields(DerivedFieldNames{}); !reflect.DeepEqual(got, want) {
		t.Errorf("fields:\n got %+v\nwant %+v", got, want)
	}

	oneDB := &Schema{Name: "a", Kind: SchemaKindAPI, Dependencies: []ServiceRef{{Name: "common", Kind: SchemaKindGeneral}, {Name: "a-db", Kind: SchemaKindDB}}}
	if db, from, ok := oneDB.Database(); !ok || db != "a-db" || from != "dependencies" {
		t.Errorf("Database() = %s, %s, %v; want a-db from dependencies", db, from, ok)
	}
	twoDBs := &Schema{Name: "a", Kind: SchemaKindAPI, Dependencies: []ServiceRef{{Name: "x", Kind: SchemaKindDB}, {Name: "y", Kind: SchemaKindDB}}}
	if _, _, ok := twoDBs.Database(); ok {
		t.Error("an API with two DB dependencies and no authDb has a database")
	}
	db := &Schema{Name: "shop-db", Kind: SchemaKindDB, Dependencies: []ServiceRef{{Name: "x", Kind: SchemaKindDB}}}
	if fields := db.DerivedConfigFields(DerivedFieldNames{}); fields != nil {
		t.Errorf("a DB schema derives %+v", fields)
	}
}

// TestDerivedMembersNameTheVariables: DerivedVariables names a value's
// variables as DerivedVariableName names its members.
func TestDerivedMembersNameTheVariables(t *testing.T) {
	values := map[EdgeKind][]any{
		EdgeSQL: {
			DatabaseConnection{CloudSQL: &CloudSQLConnection{Instance: "i", Database: "d", User: "u"}},
			DatabaseConnection{D1: &D1Connection{Binding: "b", Database: "d"}},
		},
		EdgeHTTP: {ServiceEndpoint{URL: "u", Binding: &ServiceBinding{Name: "n", Worker: "w"}, Credential: &ServiceCredential{
			Source: CredentialSignedToken, Audience: "a", Issuer: "i", Key: "k", Headers: []string{ServiceAuthorizationHeader},
		}}},
		EdgeBucket: {
			BucketConnection{Name: "n", Endpoint: "e"},
			BucketConnection{Name: "n", Binding: "b", R2: &R2Credential{Account: "a", AccessKeyID: "i", Token: "t"}},
			BucketConnection{Name: "n", Binding: "b", R2: &R2Credential{Account: "a", Jurisdiction: "eu", AccessKeyID: "i", Token: "t"}},
		},
	}
	for kind, list := range values {
		for _, value := range list {
			checkVariablesAreMembers(t, kind, value)
		}
	}
}

// checkVariablesAreMembers checks each variable DerivedVariables makes of
// value is a member DerivedMembers lists for kind.
func checkVariablesAreMembers(t *testing.T, kind EdgeKind, value any) {
	t.Helper()
	names := map[string]bool{}
	for _, path := range DerivedMembers(kind) {
		names[DerivedVariableName("F", path)] = true
	}
	vars, err := DerivedVariables("F", value)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vars {
		if !names[v.Name] {
			t.Errorf("%s: variable %s is no member DerivedMembers lists", kind, v.Name)
		}
	}
}
