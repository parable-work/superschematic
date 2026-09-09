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
	upstream.Types["User"] = &ir.TypeDef{Name: "User", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{{Name: "id"}, {Name: "name"}}}

	model, err := auth.Provider{}.Analyze(nil, upstream)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !model.HasPrincipalStore || model.HasSessionStore {
		t.Fatalf("model = %+v, want a principal store and no session store", model)
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

func TestStoresParseTheStringIDIntoTheScalarUUID(t *testing.T) {
	snippet, err := registry.AuthSnippetFunc(auth.Provider{})
	if err != nil {
		t.Fatalf("AuthSnippetFunc: %v", err)
	}
	data := map[string]any{
		"Auth":   &registry.AuthModel{HasPrincipalStore: true, Extra: auth.Stores{HasKeyStore: true}},
		"Naming": map[string]string{"HTTPRuntimeGoModule": "example.com/http", "ScalarGoModule": "example.com/scalars"},
	}
	stores, err := snippet("middlewareStores", data)
	if err != nil {
		t.Fatalf("render middlewareStores: %v", err)
	}
	if !strings.Contains(stores, "scalars.ParseUUID(id)") || strings.Contains(stores, "Eq: &id}") {
		t.Fatalf("the principal store must parse the id before filtering:\n%s", stores)
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
