package stackconfig

import (
	"reflect"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestLoadDatabase(t *testing.T) {
	got, err := loadDatabase("SHOP_DB_DATABASE", env(map[string]string{"SHOP_DB_DATABASE_URL": "postgres://localhost/shop_db"}))
	if err != nil || got.URL != "postgres://localhost/shop_db" || got.CloudSQL != nil {
		t.Errorf("connection string: %+v, %v", got, err)
	}

	got, err = loadDatabase("SHOP_DB_DATABASE", env(map[string]string{
		"SHOP_DB_DATABASE_CLOUD_SQL_INSTANCE": "acme:us-central1:shop",
		"SHOP_DB_DATABASE_CLOUD_SQL_DATABASE": "shop_db",
		"SHOP_DB_DATABASE_CLOUD_SQL_USER":     "shop-api@acme.iam",
	}))
	want := Database{CloudSQL: &CloudSQL{Instance: "acme:us-central1:shop", Database: "shop_db", User: "shop-api@acme.iam"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("cloud sql: %+v, %v", got, err)
	}

	for _, tc := range []struct {
		vars map[string]string
		want string
	}{
		{map[string]string{}, "required environment variable SHOP_DB_DATABASE_URL, or SHOP_DB_DATABASE_CLOUD_SQL_INSTANCE"},
		{map[string]string{"SHOP_DB_DATABASE_URL": "x", "SHOP_DB_DATABASE_CLOUD_SQL_USER": "u"}, "not both"},
		{map[string]string{"SHOP_DB_DATABASE_CLOUD_SQL_INSTANCE": "i"}, "SHOP_DB_DATABASE_CLOUD_SQL_DATABASE, SHOP_DB_DATABASE_CLOUD_SQL_USER is not"},
	} {
		if _, err := loadDatabase("SHOP_DB_DATABASE", env(tc.vars)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("loadDatabase(%v) = %v, want an error containing %q", tc.vars, err, tc.want)
		}
	}
}

func TestLoadService(t *testing.T) {
	got, err := loadService("SHOP_API_SERVICE", env(map[string]string{"SHOP_API_SERVICE_URL": "http://127.0.0.1:8080"}))
	if err != nil || got.URL != "http://127.0.0.1:8080" || got.Credential != nil {
		t.Errorf("url only: %+v, %v", got, err)
	}

	got, err = loadService("SHOP_API_SERVICE", env(map[string]string{
		"SHOP_API_SERVICE_URL":                 "https://shop-api.run.app",
		"SHOP_API_SERVICE_CREDENTIAL_SOURCE":   "google-id-token",
		"SHOP_API_SERVICE_CREDENTIAL_AUDIENCE": "https://shop-api.run.app",
		"SHOP_API_SERVICE_CREDENTIAL_HEADERS":  "Service-Authorization,X-Serverless-Authorization",
	}))
	want := Service{URL: "https://shop-api.run.app", Credential: &Credential{
		Source: SourceGoogleIDToken, Audience: "https://shop-api.run.app",
		Headers: []string{"Service-Authorization", "X-Serverless-Authorization"},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("google id token: %+v, %v", got, err)
	}

	got, err = loadService("SHOP_API_SERVICE", env(map[string]string{
		"SHOP_API_SERVICE_URL":                 "http://127.0.0.1:8081",
		"SHOP_API_SERVICE_CREDENTIAL_SOURCE":   "signed-token",
		"SHOP_API_SERVICE_CREDENTIAL_AUDIENCE": "shop-api",
		"SHOP_API_SERVICE_CREDENTIAL_ISSUER":   "shop-orders",
		"SHOP_API_SERVICE_CREDENTIAL_KEY":      `{"kty":"OKP"}`,
	}))
	if err != nil || got.Credential.Issuer != "shop-orders" || got.Credential.Key != `{"kty":"OKP"}` {
		t.Errorf("signed token: %+v, %v", got, err)
	}
	if headers := got.Credential.HeaderNames(); !reflect.DeepEqual(headers, []string{"Service-Authorization"}) {
		t.Errorf("default headers = %v", headers)
	}

	for _, tc := range []struct {
		vars map[string]string
		want string
	}{
		{map[string]string{}, "required environment variable SHOP_API_SERVICE_URL is not set"},
		{map[string]string{"SHOP_API_SERVICE_URL": "u", "SHOP_API_SERVICE_CREDENTIAL_AUDIENCE": "a"}, "SHOP_API_SERVICE_CREDENTIAL_AUDIENCE is set and SHOP_API_SERVICE_CREDENTIAL_SOURCE is not"},
		{map[string]string{"SHOP_API_SERVICE_URL": "u", "SHOP_API_SERVICE_CREDENTIAL_SOURCE": "api-key"}, `is "api-key"`},
		{map[string]string{"SHOP_API_SERVICE_URL": "u", "SHOP_API_SERVICE_CREDENTIAL_SOURCE": "token-file"}, "SHOP_API_SERVICE_CREDENTIAL_TOKEN_FILE is not set"},
		{map[string]string{"SHOP_API_SERVICE_URL": "u", "SHOP_API_SERVICE_CREDENTIAL_SOURCE": "token-file", "SHOP_API_SERVICE_CREDENTIAL_TOKEN_FILE": "/t", "SHOP_API_SERVICE_CREDENTIAL_KEY": "k"}, "KEY is set, which a token-file credential does not read"},
		{map[string]string{"SHOP_API_SERVICE_URL": "u", "SHOP_API_SERVICE_CREDENTIAL_SOURCE": "token-file", "SHOP_API_SERVICE_CREDENTIAL_TOKEN_FILE": "/t", "SHOP_API_SERVICE_CREDENTIAL_HEADERS": "Authorization"}, "lacks Service-Authorization"},
	} {
		if _, err := loadService("SHOP_API_SERVICE", env(tc.vars)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("loadService(%v) = %v, want an error containing %q", tc.vars, err, tc.want)
		}
	}
}

func TestLoadFromTheEnvironment(t *testing.T) {
	t.Setenv("ORDERS_DB_DATABASE_URL", "postgres://orders")
	t.Setenv("PAYMENTS_SERVICE_URL", "http://payments")
	if db, err := LoadDatabase("ORDERS_DB_DATABASE"); err != nil || db.URL != "postgres://orders" {
		t.Errorf("LoadDatabase = %+v, %v", db, err)
	}
	if svc, err := LoadService("PAYMENTS_SERVICE"); err != nil || svc.URL != "http://payments" {
		t.Errorf("LoadService = %+v, %v", svc, err)
	}
}
