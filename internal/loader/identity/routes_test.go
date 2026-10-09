package identity

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// routesDB returns userSchema with roles, whose Account table also names
// its display name, a field of its own of another scalar, and declares a
// scalar of its own for the login.
func routesDB() *ir.Schema {
	db := userSchema(true)
	db.Scalars["Identity.Name"] = &ir.ScalarDef{Name: "Identity.Name", LanguagePrimitive: ir.LanguageString}
	account := db.Types["Account"]
	account.User = &ir.UserTrait{Login: "email", Name: "displayName"}
	account.Fields = append(account.Fields, &ir.FieldDef{Name: "displayName", TypeRef: ir.TypeRef{Name: "Identity.Name"}, Required: true})
	db.Scalars["Contact.Email"].TypeMappings = map[string]string{"sql": "CITEXT"}
	return db
}

// routesAPI returns an API whose route sets carry sessions and
// administration.
func routesAPI(sessions *ir.UserSessionsConfig, admin *ir.UserAdministrationConfig) *ir.Schema {
	api := ir.NewSchema("users-api", ir.SchemaKindAPI)
	api.AuthDB = "users"
	if sessions != nil {
		api.OperationSets = append(api.OperationSets, &ir.OperationSet{Name: "Account", Operations: []*ir.FieldDef{}, UserSessions: sessions})
	}
	if admin != nil {
		api.OperationSets = append(api.OperationSets, &ir.OperationSet{Name: "AccountAdmin", Operations: []*ir.FieldDef{}, UserAdministration: admin})
	}
	api.OperationSets = append(api.OperationSets, &ir.OperationSet{Name: "Other", Operations: []*ir.FieldDef{{Name: "ping", TypeRef: ir.TypeRef{Name: "boolean"}, HTTPMethod: "GET"}}})
	return api
}

// route is what a test reads of an added operation.
type route struct {
	name, method, path, result string
	array                      bool
	args                       string // "name:type" each, comma-separated
	rule                       string // "public", "auth" or the permission
	rateLimit                  int
	hidden                     bool
}

func routeOf(op *ir.FieldDef) route {
	r := route{name: op.Name, method: op.HTTPMethod, path: op.RestPath, result: op.TypeRef.Name, array: op.TypeRef.IsArray}
	var args []string
	for _, arg := range op.Arguments {
		if !arg.Required {
			args = append(args, arg.Name+"?:"+arg.TypeRef.Name)
			continue
		}
		args = append(args, arg.Name+":"+arg.TypeRef.Name)
	}
	r.args = strings.Join(args, ",")
	switch {
	case op.Public:
		r.rule = "public"
	case op.Auth:
		r.rule = "auth"
	}
	if len(op.Permissions) > 0 {
		r.rule += strings.Join(op.Permissions, "|")
	}
	if op.Middleware != nil && op.Middleware.RateLimit != nil {
		r.rateLimit = *op.Middleware.RateLimit
	}
	if op.MCP != nil {
		r.hidden = op.MCP.Hidden && op.MCP.HiddenReason == ir.IdentityPasswordToolReason
	}
	return r
}

func routesOf(t *testing.T, set *ir.OperationSet) []route {
	t.Helper()
	var routes []route
	for _, op := range set.Operations {
		if op.IdentityOperation != op.Name || op.Origin != ir.OriginIdentity || !op.Required {
			t.Errorf("%s: IdentityOperation %q, Origin %q, Required %v", op.Name, op.IdentityOperation, op.Origin, op.Required)
		}
		if op.Comment == "" {
			t.Errorf("%s has no comment, which the OpenAPI document describes it with", op.Name)
		}
		routes = append(routes, routeOf(op))
	}
	return routes
}

// TestExpandRoutesSessions: @userSessions gets login, logout, me,
// capabilities and changePassword under its path, each with its rule, rate
// limit and tool, and register with register: true.
func TestExpandRoutesSessions(t *testing.T) {
	api := routesAPI(&ir.UserSessionsConfig{Register: true}, nil)
	ExpandRoutes(api, routesDB(), ir.DefaultIdentityPermissionPrefix)
	want := []route{
		{name: "login", method: "POST", path: "auth/login", result: "LoginResult", args: "input:LoginInput", rule: "public", rateLimit: 10, hidden: true},
		{name: "logout", method: "POST", path: "auth/logout", result: "boolean", rule: "auth"},
		{name: "me", method: "GET", path: "auth/me", result: "CurrentUser", rule: "auth"},
		{name: "capabilities", method: "GET", path: "auth/capabilities", result: "Capabilities", rule: "auth"},
		{name: "changePassword", method: "POST", path: "auth/password", result: "boolean", args: "input:ChangePasswordInput", rule: "auth", rateLimit: 10, hidden: true},
		{name: "register", method: "POST", path: "auth/register", result: "LoginResult", args: "input:RegisterInput", rule: "public", rateLimit: 5, hidden: true},
	}
	if got := routesOf(t, api.OperationSets[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("@userSessions({ register: true }) operations:\n got %+v\nwant %+v", got, want)
	}
	if other := api.OperationSets[1]; len(other.Operations) != 1 || other.Operations[0].IdentityOperation != "" {
		t.Errorf("another set changed: %+v", other.Operations)
	}
}

// TestExpandRoutesWithoutLogin: login: false leaves me and capabilities,
// under the set's own path, and the types they use alone.
func TestExpandRoutesWithoutLogin(t *testing.T) {
	api := routesAPI(&ir.UserSessionsConfig{Path: "account", NoLogin: true}, nil)
	scalars := ExpandRoutes(api, routesDB(), ir.DefaultIdentityPermissionPrefix)
	want := []route{
		{name: "me", method: "GET", path: "account/me", result: "CurrentUser", rule: "auth"},
		{name: "capabilities", method: "GET", path: "account/capabilities", result: "Capabilities", rule: "auth"},
	}
	if got := routesOf(t, api.OperationSets[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("@userSessions({ login: false }) operations:\n got %+v\nwant %+v", got, want)
	}
	if got, want := sortedNames(api.Types), []string{"Capabilities", "CurrentUser", "RoleRef", "SessionUser"}; !slices.Equal(got, want) {
		t.Errorf("types = %v, want %v", got, want)
	}
	if len(api.Enums) != 0 {
		t.Errorf("enums = %v, want none: no input takes a session", api.Enums)
	}
	if want := []string{"Identity.UUID", "Contact.Email", "Identity.Name"}; !slices.Equal(scalars, want) {
		t.Errorf("scalars = %v, want %v", scalars, want)
	}
}

// TestExpandRoutesWithoutRegister: the default config has no register,
// and so no RegisterInput.
func TestExpandRoutesWithoutRegister(t *testing.T) {
	api := routesAPI(&ir.UserSessionsConfig{}, nil)
	ExpandRoutes(api, routesDB(), ir.DefaultIdentityPermissionPrefix)
	var names []string
	for _, op := range api.OperationSets[0].Operations {
		names = append(names, op.Name)
	}
	if want := []string{"login", "logout", "me", "capabilities", "changePassword"}; !slices.Equal(names, want) {
		t.Errorf("operations = %v, want %v", names, want)
	}
	if api.Types[ir.IdentityRegisterInputType] != nil {
		t.Error("RegisterInput added without register")
	}
}

// TestExpandRoutesAdministration: @userAdministration gets the twelve
// routes, each needing its permission under the prefix, with the user's
// and the role's keys as path arguments.
func TestExpandRoutesAdministration(t *testing.T) {
	db := routesDB()
	db.Scalars["Identity.Slug"] = &ir.ScalarDef{Name: "Identity.Slug", LanguagePrimitive: ir.LanguageString}
	for _, fd := range db.Types["Role"].Fields {
		if fd.Key {
			fd.TypeRef.Name = "Identity.Slug"
		}
	}
	api := routesAPI(nil, &ir.UserAdministrationConfig{Path: "staff/admin"})
	ExpandRoutes(api, db, "acme.iam")
	const (
		usersRead  = "acme.iam.users.read"
		usersWrite = "acme.iam.users.write"
		rolesRead  = "acme.iam.roles.read"
		rolesWrite = "acme.iam.roles.write"
	)
	want := []route{
		{name: "createUser", method: "POST", path: "staff/admin/users", result: "IdentityUser", args: "input:CreateUserInput", rule: usersWrite, hidden: true},
		{name: "listUsers", method: "GET", path: "staff/admin/users", result: "IdentityUser", array: true, rule: usersRead},
		{name: "getUser", method: "GET", path: "staff/admin/users/{id}", result: "IdentityUser", args: "id:Identity.UUID", rule: usersRead},
		{name: "disableUser", method: "POST", path: "staff/admin/users/{id}/disable", result: "IdentityUser", args: "id:Identity.UUID", rule: usersWrite},
		{name: "enableUser", method: "POST", path: "staff/admin/users/{id}/enable", result: "IdentityUser", args: "id:Identity.UUID", rule: usersWrite},
		{name: "setUserPassword", method: "PUT", path: "staff/admin/users/{id}/password", result: "boolean", args: "id:Identity.UUID,input:SetPasswordInput", rule: usersWrite, hidden: true},
		{name: "listRoles", method: "GET", path: "staff/admin/roles", result: "IdentityRole", array: true, rule: rolesRead},
		{name: "createRole", method: "POST", path: "staff/admin/roles", result: "IdentityRole", args: "input:RoleInput", rule: rolesWrite},
		{name: "updateRole", method: "PUT", path: "staff/admin/roles/{id}", result: "IdentityRole", args: "id:Identity.Slug,input:RoleInput", rule: rolesWrite},
		{name: "deleteRole", method: "DELETE", path: "staff/admin/roles/{id}", result: "boolean", args: "id:Identity.Slug", rule: rolesWrite},
		{name: "grantRole", method: "PUT", path: "staff/admin/users/{id}/roles/{roleId}", result: "IdentityUser", args: "id:Identity.UUID,roleId:Identity.Slug", rule: rolesWrite},
		{name: "revokeRole", method: "DELETE", path: "staff/admin/users/{id}/roles/{roleId}", result: "IdentityUser", args: "id:Identity.UUID,roleId:Identity.Slug", rule: rolesWrite},
	}
	if got := routesOf(t, api.OperationSets[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("@userAdministration operations:\n got %+v\nwant %+v", got, want)
	}
	if got := fieldsOf(api.Types[ir.IdentityRoleRefType]); got != "id:Identity.Slug!,name:string!" {
		t.Errorf("RoleRef = %s", got)
	}
}

// fieldsOf renders td's fields as "name:type" with [] for a list, {} for a
// map, ! when required and * when secret.
func fieldsOf(td *ir.TypeDef) string {
	if td == nil {
		return "<missing>"
	}
	var out []string
	for _, fd := range td.Fields {
		s := fd.Name + ":" + fd.TypeRef.Name
		if fd.TypeRef.IsArray {
			s += "[]"
		}
		if fd.TypeRef.IsMap {
			s += "{}"
		}
		if fd.Required {
			s += "!"
		}
		if fd.Secret {
			s += "*"
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

// TestExpandRoutesTypes: every type the routes use, with the authDb's
// login, key and name types, Auth.Password for each password, a Secret
// token, a Temporal.DateTime expiry and a map of booleans, each of the
// identity origin and the role its use gives it.
func TestExpandRoutesTypes(t *testing.T) {
	api := routesAPI(&ir.UserSessionsConfig{Register: true}, &ir.UserAdministrationConfig{})
	scalars := ExpandRoutes(api, routesDB(), ir.DefaultIdentityPermissionPrefix)
	want := map[string]string{
		"LoginInput":          "login:Contact.Email!,password:Auth.Password!*,session:SessionTransport",
		"RegisterInput":       "login:Contact.Email!,name:Identity.Name,password:Auth.Password!*,session:SessionTransport",
		"LoginResult":         "user:SessionUser!,expiresAt:Temporal.DateTime!,token:string*",
		"SessionUser":         "id:Identity.UUID!,login:Contact.Email!,name:Identity.Name!",
		"RoleRef":             "id:Identity.UUID!,name:string!",
		"CurrentUser":         "user:SessionUser!,roles:RoleRef[]!,permissions:string[]!",
		"Capabilities":        "operations:boolean{}!",
		"ChangePasswordInput": "current:Auth.Password!*,password:Auth.Password!*",
		"CreateUserInput":     "login:Contact.Email!,name:Identity.Name,password:Auth.Password!*",
		"SetPasswordInput":    "password:Auth.Password!*",
		"IdentityUser":        "id:Identity.UUID!,login:Contact.Email!,name:Identity.Name!,disabled:boolean!,roles:RoleRef[]!",
		"RoleInput":           "name:string!,permissions:string[]!",
		"IdentityRole":        "id:Identity.UUID!,name:string!,permissions:string[]!",
	}
	for name, fields := range want {
		td := api.Types[name]
		if got := fieldsOf(td); got != fields {
			t.Errorf("%s = %s, want %s", name, got, fields)
			continue
		}
		role := ir.RoleEmbeddedStruct
		if strings.HasSuffix(name, "Input") {
			role = ir.RoleAPIInput
		}
		if td.Role != role || td.Origin != ir.OriginIdentity || td.Owner != api.Name || td.Comment == "" {
			t.Errorf("%s: role %s, origin %q, owner %q, comment %q", name, td.Role, td.Origin, td.Owner, td.Comment)
		}
		for _, fd := range td.Fields {
			if fd.Origin != ir.OriginIdentity {
				t.Errorf("%s.%s has origin %q", name, fd.Name, fd.Origin)
			}
		}
	}
	if got := sortedNames(api.Types); len(got) != len(want) {
		t.Errorf("types = %v, want the %d above", got, len(want))
	}
	transport := api.Enums[ir.IdentitySessionTransportEnum]
	if transport == nil || transport.Origin != ir.OriginIdentity || len(transport.Values) != 2 ||
		transport.Values[0].SerializedAs != "bearer" || transport.Values[1].SerializedAs != "cookie" {
		t.Errorf("SessionTransport = %+v", transport)
	}
	if want := []string{"Identity.UUID", "Contact.Email", "Identity.Name", "Temporal.DateTime", "Auth.Password"}; !slices.Equal(scalars, want) {
		t.Errorf("scalars = %v, want %v", scalars, want)
	}
	for _, name := range scalars {
		if api.Scalars[name] == nil {
			t.Errorf("api.Scalars lacks %s", name)
		}
	}
	// A scalar the authDb declares is copied from it, apart from it.
	email := api.Scalars["Contact.Email"]
	if !email.CaseInsensitive || email.TypeMappings["sql"] != "CITEXT" {
		t.Errorf("Contact.Email = %+v, want the authDb's declaration", email)
	}
	email.TypeMappings["sql"] = "TEXT"
	if routesDB().Scalars["Contact.Email"].TypeMappings["sql"] != "CITEXT" {
		t.Error("the copy shares the authDb's mappings")
	}
	if api.Scalars["Auth.Password"].Description != "" {
		t.Error("a catalog scalar the authDb lacks is added bare, for hydration")
	}
}

// TestExpandRoutesWithoutNameOrRoles: a User trait without a name gives
// the inputs no name and a principal the login's type; an authDb without
// a UserRole table gives a role reference strings.
func TestExpandRoutesWithoutNameOrRoles(t *testing.T) {
	db := userSchema(false)
	api := routesAPI(&ir.UserSessionsConfig{Register: true}, nil)
	ExpandRoutes(api, db, ir.DefaultIdentityPermissionPrefix)
	for name, fields := range map[string]string{
		"RegisterInput": "login:Contact.Email!,password:Auth.Password!*,session:SessionTransport",
		"SessionUser":   "id:Identity.UUID!,login:Contact.Email!,name:Contact.Email!",
		"RoleRef":       "id:string!,name:string!",
	} {
		if got := fieldsOf(api.Types[name]); got != fields {
			t.Errorf("%s = %s, want %s", name, got, fields)
		}
	}
}

// TestExpandRoutesIsDeterministic: two expansions of one schema are the
// same IR, and an API without a route set is left as it was.
func TestExpandRoutesIsDeterministic(t *testing.T) {
	render := func() string {
		api := routesAPI(&ir.UserSessionsConfig{Register: true}, &ir.UserAdministrationConfig{})
		scalars := ExpandRoutes(api, routesDB(), ir.DefaultIdentityPermissionPrefix)
		data, err := json.Marshal(struct {
			API     *ir.Schema
			Scalars []string
		}{api, scalars})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	first := render()
	for i := 0; i < 5; i++ {
		if got := render(); got != first {
			t.Fatalf("expansion %d differs:\n%s\n%s", i, got, first)
		}
	}

	plain := routesAPI(nil, nil)
	before, _ := json.Marshal(plain)
	if scalars := ExpandRoutes(plain, routesDB(), ir.DefaultIdentityPermissionPrefix); scalars != nil {
		t.Errorf("scalars = %v for an API without a route set", scalars)
	}
	if after, _ := json.Marshal(plain); string(after) != string(before) {
		t.Errorf("an API without a route set changed:\n%s\n%s", before, after)
	}
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
