package ir

import (
	"strings"
	"testing"
)

// TestMemoryStoreDeclarationName: a class's name in kebab case.
func TestMemoryStoreDeclarationName(t *testing.T) {
	for symbol, want := range map[string]string{
		"StockChanged": "stock-changed", "ReceiptSent": "receipt-sent", "HTTPRequests": "http-requests",
		"ProductViews2": "product-views2", "Views": "views", "Stock_Level": "stock-level", "ABTest": "ab-test",
	} {
		if got := MemoryStoreDeclarationName(symbol); got != want {
			t.Errorf("%s: %q, want %q", symbol, got, want)
		}
	}
	if err := CheckMemoryStoreDeclarationName("stock.samples", false); err == nil {
		t.Error("a derived name with a dot passes")
	}
	if err := CheckMemoryStoreDeclarationName("stock.samples_v2", true); err != nil {
		t.Errorf("an explicit name with a dot and an underscore: %v", err)
	}
}

// TestMemoryStoreConnectionContract: a memoryStore edge's value takes a
// store, a URL and an authentication, a CA and a member, and refuses what
// a runtime's reader would (D75).
func TestMemoryStoreConnectionContract(t *testing.T) {
	ok := []any{
		MemoryStoreConnection{Store: "shop-cache", URL: "rediss://10.1.2.3:6379", CA: "-----BEGIN CERTIFICATE-----", Auth: MemoryStoreAuthIAM},
		MemoryStoreConnection{Store: "shop-cache", URL: "redis://127.0.0.1:26379/0", Auth: MemoryStoreAuthNone, Member: Parameter("pr")},
		MemoryStoreConnection{Store: "shop-cache", URL: Output{Resource: "shop-cache.instance", Name: "url"}, CA: Output{Resource: "shop-cache.instance", Name: "ca"}, Auth: MemoryStoreAuthIAM},
	}
	for _, v := range ok {
		if err := CheckDerivedValue(EdgeMemoryStore, v); err != nil {
			t.Errorf("%+v: %v", v, err)
		}
	}
	for _, tc := range []struct {
		value any
		want  string
	}{
		{map[string]any{"url": "redis://h:1", "auth": "none"}, "store is missing"},
		{map[string]any{"store": "s", "url": "http://h", "auth": "none"}, "redis:// or rediss://"},
		{map[string]any{"store": "s", "url": "redis://h:1", "ca": "pem", "auth": "none"}, "sets no ca"},
		{map[string]any{"store": "s", "url": "redis://h:1", "auth": "iam"}, "has a rediss:// url"},
		{map[string]any{"store": "s", "url": "rediss://h:1", "auth": "password"}, `auth is "password"`},
		{map[string]any{"store": "s", "url": "rediss://h:1", "auth": "none", "token": "x"}, "has no member token"},
	} {
		if err := CheckDerivedValue(EdgeMemoryStore, tc.value); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.value, err, tc.want)
		}
	}
}

// TestMemoryStoreDerivedFields: an API's memory stores each derive a
// field after the store, the rateLimitStore's from rateLimitStore, and
// the naming file's template names it.
func TestMemoryStoreDerivedFields(t *testing.T) {
	s := NewSchema("shop-api", SchemaKindAPI)
	cache, limits := ServiceRef{Name: "shop-cache", Kind: SchemaKindMemoryStore}, ServiceRef{Name: "shop-limits", Kind: SchemaKindMemoryStore}
	s.MemoryStores = []ServiceRef{cache, limits}
	s.RateLimitStore = &limits
	fields := s.DerivedConfigFields(DerivedFieldNames{})
	if len(fields) != 2 || fields[0].Name != "SHOP_CACHE_MEMORY_STORE" || fields[0].From != "memoryStores" ||
		fields[1].Name != "SHOP_LIMITS_MEMORY_STORE" || fields[1].From != "rateLimitStore" || fields[1].Kind != EdgeMemoryStore {
		t.Errorf("fields = %+v", fields)
	}
	if got := (DerivedFieldNames{MemoryStore: "CACHE_{SERVICE}"}).Field(EdgeMemoryStore, "shop-cache"); got != "CACHE_SHOP_CACHE" {
		t.Errorf("templated field = %s", got)
	}
	if err := (DerivedFieldNames{MemoryStore: "CACHE"}).Validate(); err == nil || !strings.Contains(err.Error(), "derived_fields.memory_store") {
		t.Errorf("a template without {SERVICE}: %v", err)
	}
	vars, err := DerivedVariables("SHOP_CACHE_MEMORY_STORE", MemoryStoreConnection{Store: "shop-cache", URL: "redis://127.0.0.1:26379/0", Auth: MemoryStoreAuthNone})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, v := range vars {
		names = append(names, v.Name)
	}
	if strings.Join(names, ",") != "SHOP_CACHE_MEMORY_STORE_AUTH,SHOP_CACHE_MEMORY_STORE_STORE,SHOP_CACHE_MEMORY_STORE_URL" {
		t.Errorf("variables = %v", names)
	}
}

// TestReservedRateLimitDeclaration: every MemoryStore schema holds the
// core's rate-limit declaration once, sorted among its own; a schema of
// another kind gets none.
func TestReservedRateLimitDeclaration(t *testing.T) {
	s := NewSchema("shop-cache", SchemaKindMemoryStore)
	s.MemoryStoreDeclarations = []*MemoryStoreDeclaration{{Name: "views"}, {Name: "claims"}}
	s.AddReservedMemoryStoreDeclarations()
	s.AddReservedMemoryStoreDeclarations()
	var names []string
	for _, d := range s.MemoryStoreDeclarations {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "claims,rate-limit,views" {
		t.Errorf("declarations = %v", names)
	}
	if d := s.MemoryStoreDeclaration(MemoryStoreRateLimit); !d.Reserved || d.NamePattern() != "{store}:rate-limit:{service}:{operation}:{window}:{client}" || d.MaxTTLSeconds != 86405 {
		t.Errorf("rate-limit = %+v", d)
	}
	api := NewSchema("shop-api", SchemaKindAPI)
	api.AddReservedMemoryStoreDeclarations()
	if len(api.MemoryStoreDeclarations) != 0 {
		t.Error("an API got a reserved declaration")
	}
}
