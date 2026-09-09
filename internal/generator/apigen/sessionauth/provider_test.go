package sessionauth_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	ir "github.com/parable-work/superschematic/ir"
)

func TestProviderDefinesEverySnippet(t *testing.T) {
	if _, err := apigen.AuthSnippetFunc(sessionauth.Provider{}); err != nil {
		t.Fatalf("AuthSnippetFunc: %v", err)
	}
}

func TestAnalyzeReportsOnlyTheCoreStores(t *testing.T) {
	upstream := ir.NewSchema("fixture-db", ir.SchemaKindDB)
	session := &ir.TypeDef{Name: "Session", Role: ir.RoleDBTable}
	for _, f := range []string{"id", "jti", "user", "expiresAt"} {
		session.Fields = append(session.Fields, &ir.FieldDef{Name: f})
	}
	upstream.Types["Session"] = session
	upstream.Types["Tenant"] = &ir.TypeDef{Name: "Tenant", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{{Name: "id"}, {Name: "name"}, {Name: "slug"}}}

	model, err := sessionauth.Provider{}.Analyze(nil, upstream)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !model.HasSessionStore || model.HasPrincipalStore {
		t.Fatalf("model = %+v, want session store only (no User table)", model)
	}
	if model.Extra != nil {
		t.Fatalf("Extra = %v, want nil: the session model has no provider data", model.Extra)
	}
}

// The ORM filters take the scalar UUID type, so the stores must parse the
// string ids the runtime hands them instead of taking their address. Found
// by examples/acme-schematic, whose upstream DB has a User table.
func TestStoresParseStringIDsIntoScalarUUIDs(t *testing.T) {
	snippet, err := apigen.AuthSnippetFunc(sessionauth.Provider{})
	if err != nil {
		t.Fatalf("AuthSnippetFunc: %v", err)
	}
	data := map[string]any{
		"Auth":   &apigen.AuthModel{HasSessionStore: true, HasPrincipalStore: true},
		"Naming": map[string]string{"HTTPRuntimeGoModule": "example.com/http", "ScalarGoModule": "example.com/scalars"},
	}
	stores, err := snippet("middlewareStores", data)
	if err != nil {
		t.Fatalf("render middlewareStores: %v", err)
	}
	for _, want := range []string{"scalars.ParseUUID(jti)", "scalars.ParseUUID(id)", "Eq: &jtiUUID", "Eq: &idUUID"} {
		if !strings.Contains(stores, want) {
			t.Fatalf("middlewareStores lacks %q:\n%s", want, stores)
		}
	}
	for _, reject := range []string{"Eq: &jti}", "Eq: &id}"} {
		if strings.Contains(stores, reject) {
			t.Fatalf("middlewareStores still passes a *string as a UUID filter (%q):\n%s", reject, stores)
		}
	}
	imports, err := snippet("middlewareImports", data)
	if err != nil {
		t.Fatalf("render middlewareImports: %v", err)
	}
	if !strings.Contains(imports, `scalars "example.com/scalars"`) {
		t.Fatalf("middlewareImports = %q, want the scalar import the stores use", imports)
	}
	none, err := snippet("middlewareImports", map[string]any{"Auth": &apigen.AuthModel{}, "Naming": data["Naming"]})
	if err != nil {
		t.Fatalf("render middlewareImports without stores: %v", err)
	}
	if strings.Contains(none, "scalars") {
		t.Fatalf("middlewareImports = %q, must not import scalars when no store uses them", none)
	}
}

func TestEndpointNeverMarksTenantScope(t *testing.T) {
	ep := &apigen.EndpointInfo{Namespace: "tenant", PathParams: []apigen.Param{{Name: "tenantId"}}}
	if err := (sessionauth.Provider{}).Endpoint(nil, nil, ep); err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if ep.IsScopedEndpoint || ep.ScopeParamName != "" {
		t.Fatalf("endpoint = %+v, want no tenant scope", ep)
	}
}

func TestRoutePermissionsRendersPlainStringPermissions(t *testing.T) {
	snippet, err := apigen.AuthSnippetFunc(sessionauth.Provider{})
	if err != nil {
		t.Fatalf("AuthSnippetFunc: %v", err)
	}
	got, err := snippet("routePermissions", apigen.EndpointInfo{RequiredPerms: []string{"tenants.read", "tenants.write"}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(got, `runtimesession.RequirePermissions("tenants.read", "tenants.write")`) {
		t.Fatalf("routePermissions = %q, want plain-string RequirePermissions", got)
	}
	if strings.Contains(got, "scalars.") || strings.Contains(got, "Tenant") {
		t.Fatalf("routePermissions = %q, must carry no Acme vocabulary", got)
	}
	files := sessionauth.Provider{}.Files(&apigen.APIOutput{SchemaName: "web-api"})
	if len(files) != 0 {
		t.Fatalf("Files = %v, want none even for web-api", files)
	}
	params := sessionauth.Provider{}.OpenAPIParameters(&apigen.APIOutput{IsPublic: true})
	if params != nil {
		t.Fatalf("OpenAPIParameters = %v, want none", params)
	}
}
