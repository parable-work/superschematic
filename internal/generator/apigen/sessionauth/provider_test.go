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
	for _, want := range []string{
		"scalars.ParseUUID(jti)",
		"scalars.ParseUUID(id)",
		"Eq: &jtiUUID",
		"Eq: &idUUID",
		"return runtimesession.Record{}, runtimesession.ErrNotFound",
		"return runtimesession.Principal{}, runtimesession.ErrNotFound",
		"ExpiresAt:   time.Time(session.ExpiresAt)",
	} {
		if !strings.Contains(stores, want) {
			t.Fatalf("middlewareStores lacks %q:\n%s", want, stores)
		}
	}
	// Without SessionSoftDelete the store leaves soft-deleted rows to the
	// ORM's filter: it could not report their DeletedAt.
	for _, reject := range []string{"Eq: &jti}", "Eq: &id}", "session.DeletedAt", "IncludeDeleted"} {
		if strings.Contains(stores, reject) {
			t.Fatalf("middlewareStores still has %q:\n%s", reject, stores)
		}
	}
	imports, err := snippet("middlewareImports", data)
	if err != nil {
		t.Fatalf("render middlewareImports: %v", err)
	}
	if !strings.Contains(imports, `scalars "example.com/scalars"`) {
		t.Fatalf("middlewareImports = %q, want the scalar import the stores use", imports)
	}
	std, err := snippet("middlewareStdImports", data)
	if err != nil {
		t.Fatalf("render middlewareStdImports: %v", err)
	}
	// middleware.go imports "time" for every provider; a second import
	// from the snippet would not compile without go/format removing it.
	if !strings.Contains(std, `"errors"`) || strings.Contains(std, `"time"`) {
		t.Fatalf("middlewareStdImports = %q, want errors and not time", std)
	}
	none, err := snippet("middlewareImports", map[string]any{"Auth": &apigen.AuthModel{}, "Naming": data["Naming"]})
	if err != nil {
		t.Fatalf("render middlewareImports without stores: %v", err)
	}
	if strings.Contains(none, "scalars") {
		t.Fatalf("middlewareImports = %q, must not import scalars when no store uses them", none)
	}
}

// A store that reads soft-deleted sessions must report their DeletedAt, or
// a revoked session authenticates; so it reads them only when the Session
// table's deletedAt is the nullable Temporal.DateTime it can report.
func TestAnalyzeReportsSessionSoftDeleteForANullableDateTime(t *testing.T) {
	for _, tc := range []struct {
		name      string
		deletedAt *ir.FieldDef
		want      bool
	}{
		{name: "no deletedAt"},
		{name: "nullable DateTime", deletedAt: &ir.FieldDef{TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}}, want: true},
		{name: "required DateTime", deletedAt: &ir.FieldDef{TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true}},
		{name: "nullable string", deletedAt: &ir.FieldDef{TypeRef: ir.TypeRef{Name: "string"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := ir.NewSchema("fixture-db", ir.SchemaKindDB)
			session := &ir.TypeDef{Name: "Session", Role: ir.RoleDBTable}
			for _, f := range []string{"id", "jti", "user", "expiresAt"} {
				session.Fields = append(session.Fields, &ir.FieldDef{Name: f})
			}
			if tc.deletedAt != nil {
				tc.deletedAt.Name = "deletedAt"
				session.Fields = append(session.Fields, tc.deletedAt)
			}
			upstream.Types["Session"] = session

			model, err := sessionauth.Provider{}.Analyze(nil, upstream)
			if err != nil {
				t.Fatalf("Analyze: %v", err)
			}
			if !model.HasSessionStore || model.SessionSoftDelete != tc.want {
				t.Fatalf("model = %+v, want a session store with SessionSoftDelete %v", model, tc.want)
			}
		})
	}
}

func TestSessionStoreReportsDeletedAtOnASoftDeletableSessionTable(t *testing.T) {
	snippet, err := apigen.AuthSnippetFunc(sessionauth.Provider{})
	if err != nil {
		t.Fatalf("AuthSnippetFunc: %v", err)
	}
	stores, err := snippet("middlewareStores", map[string]any{
		"Auth":   &apigen.AuthModel{HasSessionStore: true, SessionSoftDelete: true},
		"Naming": map[string]string{"HTTPRuntimeGoModule": "example.com/http", "ScalarGoModule": "example.com/scalars"},
	})
	if err != nil {
		t.Fatalf("render middlewareStores: %v", err)
	}
	for _, want := range []string{
		"IncludeDeleted: true",
		"deletedAt := time.Time(*session.DeletedAt)",
		"record.DeletedAt = &deletedAt",
	} {
		if !strings.Contains(stores, want) {
			t.Fatalf("middlewareStores lacks %q:\n%s", want, stores)
		}
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
