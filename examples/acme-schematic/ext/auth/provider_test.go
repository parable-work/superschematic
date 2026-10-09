package auth_test

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"

	"example.com/acme/schematic/ext/auth"
)

func TestProviderDefinesEverySnippetTheAPIGeneratorCalls(t *testing.T) {
	if _, err := registry.AuthSnippetFunc(auth.Provider{}); err != nil {
		t.Fatalf("AuthSnippetFunc: %v", err)
	}
}

func TestAnalyzeReportsTheKeyStoreOnlyWithAnApiKeyTable(t *testing.T) {
	upstream := ir.NewSchema("shop-db", ir.SchemaKindDB)
	upstream.Types["User"] = &ir.TypeDef{Name: "User", Role: ir.RoleDBTable, User: &ir.UserTrait{Login: "email", Name: "name"}, Fields: []*ir.FieldDef{
		{Name: "id", Key: true, TypeRef: ir.TypeRef{Name: "Identity.UUID"}}, {Name: "email"}, {Name: "name"},
	}}

	model, err := auth.Provider{}.Analyze(nil, upstream)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	// The users are the table with the User trait; API keys, not the
	// identity runtime, authenticate them.
	if model.User == nil || !model.User.KeyIsUUID() || model.User.Name != "name" || model.Identity {
		t.Fatalf("model = %+v (user %+v), want the user model without the identity runtime", model, model.User)
	}
	if model.Extra.(auth.Stores).HasKeyStore {
		t.Fatal("HasKeyStore = true without an ApiKey table")
	}

	upstream.Types["ApiKey"] = &ir.TypeDef{Name: "ApiKey", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{{Name: "id"}, {Name: "secret"}, {Name: "user"}}}
	model, err = auth.Provider{}.Analyze(nil, upstream)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !model.Extra.(auth.Stores).HasKeyStore {
		t.Fatal("HasKeyStore = false with an ApiKey(id, secret, user) table")
	}
}

// TestAnalyzeFindsTheUsersByTheirTrait: a table named User without the
// trait is no user model, so no principal store reads it.
func TestAnalyzeFindsTheUsersByTheirTrait(t *testing.T) {
	upstream := ir.NewSchema("shop-db", ir.SchemaKindDB)
	upstream.Types["User"] = &ir.TypeDef{Name: "User", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{{Name: "id", Key: true}, {Name: "name"}}}
	model, err := auth.Provider{}.Analyze(nil, upstream)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if model.User != nil {
		t.Fatalf("model.User = %+v for a table named User without the trait", model.User)
	}
}

// TestStoresParseTheStringIDIntoTheScalarUUID: the principal store reads
// the user table the trait names, under its own name, and parses the
// string id into the scalar UUID its ORM filter takes.
func TestStoresParseTheStringIDIntoTheScalarUUID(t *testing.T) {
	snippet, err := registry.AuthSnippetFunc(auth.Provider{})
	if err != nil {
		t.Fatalf("AuthSnippetFunc: %v", err)
	}
	data := map[string]any{
		"Auth": &registry.AuthModel{
			User:  &registry.UserModel{Type: "Shopper", Key: "id", KeyType: "Identity.UUID", Login: "email", Name: "displayName"},
			Extra: auth.Stores{HasKeyStore: true},
		},
		"Naming": map[string]string{"HTTPRuntimeGoModule": "example.com/http", "ScalarGoModule": "example.com/scalars"},
	}
	stores, err := snippet("middlewareStores", data)
	if err != nil {
		t.Fatalf("render middlewareStores: %v", err)
	}
	if !strings.Contains(stores, "scalars.ParseUUID(id)") || strings.Contains(stores, "Eq: &id}") {
		t.Fatalf("the principal store must parse the id before filtering:\n%s", stores)
	}
	for _, want := range []string{"GetShopperRepository()", "orm.ShopperFilter{", "Id: &orm.UUIDFilter{Eq: &idUUID}", "string(user.DisplayName)"} {
		if !strings.Contains(stores, want) {
			t.Fatalf("the principal store does not read the trait's table and fields (%s):\n%s", want, stores)
		}
	}
	if !strings.Contains(stores, "X-API-Key") && !strings.Contains(stores, "ApiKey") {
		t.Fatalf("the key store is missing:\n%s", stores)
	}
}

func TestEndpointScopesNothing(t *testing.T) {
	ep := &registry.EndpointInfo{Namespace: "shop", PathParams: []registry.EndpointParam{{Name: "shopId"}}}
	if err := (auth.Provider{}).Endpoint(nil, nil, ep); err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if ep.IsScopedEndpoint || ep.ScopeParamName != "" {
		t.Fatalf("endpoint = %+v, want no scope", ep)
	}
}
