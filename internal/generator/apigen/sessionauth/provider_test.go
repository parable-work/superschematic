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

func TestEndpointNeverMarksTenantScope(t *testing.T) {
	ep := &apigen.EndpointInfo{Namespace: "tenant", PathParams: []apigen.Param{{Name: "tenantId"}}}
	if err := (sessionauth.Provider{}).Endpoint(nil, nil, ep); err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if ep.IsTenantEndpoint || ep.TenantParamName != "" {
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
		t.Fatalf("routePermissions = %q, must carry no Parable vocabulary", got)
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
