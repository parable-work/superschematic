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

// userModelSchema is an upstream schema whose user and role tables carry the
// traits under names of their own, beside tables named User and Session
// that carry none.
func userModelSchema() *ir.Schema {
	upstream := ir.NewSchema("fixture-db", ir.SchemaKindDB)
	table := func(name string, fields ...*ir.FieldDef) *ir.TypeDef {
		td := &ir.TypeDef{Name: name, Role: ir.RoleDBTable, Fields: fields}
		upstream.Types[name] = td
		return td
	}
	key := func() *ir.FieldDef {
		return &ir.FieldDef{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true, Required: true}
	}
	account := table("Account", key(), &ir.FieldDef{Name: "email"}, &ir.FieldDef{Name: "displayName"})
	account.User = &ir.UserTrait{Login: "email", Name: "displayName"}
	table("Grade", key(), &ir.FieldDef{Name: "name"}, &ir.FieldDef{Name: "permissions"}).UserRole = &ir.UserRoleTrait{}
	table("User", key(), &ir.FieldDef{Name: "name"})
	table("Session", key(), &ir.FieldDef{Name: "jti"}, &ir.FieldDef{Name: "user"}, &ir.FieldDef{Name: "expiresAt"})
	return upstream
}

// TestAnalyzeReadsTheUserTrait: the user model is the table with the User
// trait and the one with the UserRole trait, whatever they are named, and
// the server then authenticates with the identity runtime (D50).
func TestAnalyzeReadsTheUserTrait(t *testing.T) {
	model, err := sessionauth.Provider{}.Analyze(nil, userModelSchema())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	want := apigen.UserModel{Type: "Account", Key: "id", KeyType: "Identity.UUID", Login: "email", Name: "displayName", RoleType: "Grade"}
	if model.User == nil || *model.User != want || !model.Identity {
		t.Fatalf("model = %+v (user %+v), want %+v with Identity", model, model.User, want)
	}
	if !model.User.KeyIsUUID() {
		t.Error("KeyIsUUID = false for an Identity.UUID key")
	}
	if model.Extra != nil {
		t.Fatalf("Extra = %v, want nil: the session model has no provider data", model.Extra)
	}
}

// TestAnalyzeFindsNoTableByName: tables named User and Session without the
// traits are no user model, and neither is a missing upstream schema.
func TestAnalyzeFindsNoTableByName(t *testing.T) {
	upstream := userModelSchema()
	delete(upstream.Types, "Account")
	delete(upstream.Types, "Grade")
	for name, schema := range map[string]*ir.Schema{"named tables": upstream, "no upstream": nil} {
		model, err := sessionauth.Provider{}.Analyze(nil, schema)
		if err != nil {
			t.Fatalf("%s: Analyze: %v", name, err)
		}
		if model.User != nil || model.Identity {
			t.Errorf("%s: model = %+v, want no user model", name, model)
		}
	}
}

// TestNoStoreAdapters: the session provider writes no ORM store adapter. A
// server over the user model authenticates with the identity runtime and
// gets no store banner and no store aliases; one without keeps the file's
// bytes.
func TestNoStoreAdapters(t *testing.T) {
	snippet, err := apigen.AuthSnippetFunc(sessionauth.Provider{})
	if err != nil {
		t.Fatalf("AuthSnippetFunc: %v", err)
	}
	naming := map[string]string{"HTTPRuntimeGoModule": "example.com/http", "ScalarGoModule": "example.com/scalars"}
	for _, identity := range []bool{false, true} {
		data := map[string]any{"Auth": &apigen.AuthModel{Identity: identity}, "Naming": naming}
		render := func(name string) string {
			t.Helper()
			out, err := snippet(name, data)
			if err != nil {
				t.Fatalf("render %s: %v", name, err)
			}
			return out
		}
		if std := render("middlewareStdImports"); std != "" {
			t.Errorf("identity %v: middlewareStdImports = %q, want none", identity, std)
		}
		if imports := render("middlewareImports"); strings.Contains(imports, "scalars") || !strings.Contains(imports, "runtimesession") {
			t.Errorf("identity %v: middlewareImports = %q", identity, imports)
		}
		stores, aliases := render("middlewareStores"), render("middlewareAliases")
		for _, absent := range []string{"FindByJTI", "GetByID", "NewSessionStore", "NewPrincipalStore"} {
			if strings.Contains(stores, absent) {
				t.Errorf("identity %v: middlewareStores has %s:\n%s", identity, absent, stores)
			}
		}
		if got := strings.Contains(stores, "ORM Store Adapters") || strings.Contains(aliases, "SessionRecord"); got == identity {
			t.Errorf("identity %v: the store banner and aliases are there: %v\n%s%s", identity, got, stores, aliases)
		}
		if !strings.Contains(aliases, "type Role = runtimesession.Role") {
			t.Errorf("identity %v: middlewareAliases lacks Role:\n%s", identity, aliases)
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
