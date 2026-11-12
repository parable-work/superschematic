package loader

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite golden files")

// userRoutesFixture is the TypeScript-authored API whose @userSessions and
// @userAdministration sets read fixture-user-model-db, its authDb and its
// sibling.
const userRoutesFixture = "tsreader/testdata/services/fixture-user-routes-api"

// TestLoadUserRoutesExpandsFromTheAuthDb: the API loads with its route sets
// filled from the authDb beside it, which the loader reads, and the
// expanded IR is the golden's. The reader's IR, before the expansion, is
// the tsreader golden.
func TestLoadUserRoutesExpandsFromTheAuthDb(t *testing.T) {
	schema, err := LoadService(filepath.FromSlash(userRoutesFixture))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	counts := map[string]int{}
	for _, set := range schema.OperationSets {
		counts[set.Name] = len(set.Operations)
	}
	// Account has login, logout, me, capabilities, changePassword and
	// register, and createApiKey, listApiKeys and revokeApiKey with apiKeys
	// (D114); AccountAdmin its thirteen, and listUserApiKeys and
	// revokeUserApiKey.
	if counts["Account"] != 9 || counts["AccountAdmin"] != 15 || counts["GreetingQueries"] != 1 {
		t.Errorf("operations per set = %v, want 9, 15 and 1", counts)
	}
	for _, name := range ir.IdentityAPIKeyTypes {
		if td := schema.Types[name]; td == nil || td.Origin != ir.OriginIdentity {
			t.Errorf("the API key type %s was not added: %+v", name, td)
		}
	}
	// Generic.Int64 is CreateApiKeyInput.expiresInSeconds's (D114).
	for _, name := range []string{"Auth.Password", "Contact.Email", "Generic.Int64", "Identity.Name", "Identity.UUID", "Temporal.DateTime"} {
		def := schema.Scalars[name]
		if def == nil || def.Description == "" || def.TypeMappings["go"] == "" {
			t.Errorf("%s was not hydrated: %+v", name, def)
		}
	}

	got, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	goldenPath := filepath.Join("testdata", "golden", "fixture-user-routes-api.expanded.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("missing golden file (run with -update): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("expanded IR differs from %s; run with -update and review the diff", goldenPath)
	}
}

// TestLoadUserRoutesThroughTheDependencyLoader: a build's loader reads the
// authDb once, by its name, and a load of an API without a route set reads
// no authDb.
func TestLoadUserRoutesThroughTheDependencyLoader(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(userModelFixture))
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	load := WithDependencyLoader(func(name string) (*ir.Schema, error) {
		asked = append(asked, name)
		return db, nil
	})
	schema, err := LoadService(filepath.FromSlash(userRoutesFixture), load)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if len(asked) != 1 || asked[0] != "fixture-user-model-db" {
		t.Errorf("the dependency loader was asked for %v, want fixture-user-model-db once", asked)
	}
	if schema.OperationSets[0].Operations == nil || db.OperationSets != nil {
		t.Error("the routes were not expanded, or the authDb was changed")
	}

	asked = nil
	if _, err := LoadService(filepath.FromSlash("tsreader/testdata/services/fixture-authdb-import"), load); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 0 {
		t.Errorf("an API without a route set read its authDb: %v", asked)
	}

	failing := WithDependencyLoader(func(string) (*ir.Schema, error) { return nil, errors.New("no such service") })
	_, err = LoadService(filepath.FromSlash(userRoutesFixture), failing)
	const want = "fixture-user-routes-api: reading the authDb fixture-user-model-db, whose User table the user model's routes read: no such service"
	if err == nil || err.Error() != want {
		t.Errorf("LoadService = %v, want %q", err, want)
	}
}

// TestLoadUserRoutesTakeThePermissionPrefix: the administration routes'
// permissions take the naming file's identity_permission_prefix.
func TestLoadUserRoutesTakeThePermissionPrefix(t *testing.T) {
	names := naming.Default()
	names.IdentityPermissionPrefix = "acme.iam"
	schema, err := LoadService(filepath.FromSlash(userRoutesFixture), WithNaming(names))
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range schema.OperationSets {
		if set.UserAdministration == nil {
			continue
		}
		for _, op := range set.Operations {
			if len(op.Permissions) != 1 || !strings.HasPrefix(op.Permissions[0], "acme.iam.") {
				t.Errorf("%s needs %v, want a permission under acme.iam", op.Name, op.Permissions)
			}
		}
	}
}

// userRoutesDataService writes the API of fixture-user-routes-api's route
// sets in a data form, file being src/account.schema.json or .yaml, as a
// sibling of a copy of fixture-user-model-db, so the default loader reads
// its authDb as it does the TypeScript fixture's.
func userRoutesDataService(t *testing.T, file, body string) string {
	t.Helper()
	root := t.TempDir()
	db, err := os.ReadFile(filepath.FromSlash(userModelFixture + "/src/users.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	dbConfig, err := os.ReadFile(filepath.FromSlash(userModelFixture + "/schema.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for rel, contents := range map[string]string{
		"fixture-user-model-db/schema.config.json":          string(dbConfig),
		"fixture-user-model-db/src/users.schema.json":       string(db),
		"fixture-user-routes-api/schema.config.json":        `{"name": "fixture-user-routes-api", "kind": "API", "authDb": "fixture-user-model-db", "outputs": {}}`,
		"fixture-user-routes-api/" + filepath.ToSlash(file): body,
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "fixture-user-routes-api")
}

// TestLoadUserRoutesInTheDataForms: the JSON and YAML forms read
// userSessions and userAdministration, and the loader fills them as it
// fills the TypeScript form's, operation for operation.
func TestLoadUserRoutesInTheDataForms(t *testing.T) {
	ts, err := LoadService(filepath.FromSlash(userRoutesFixture))
	if err != nil {
		t.Fatal(err)
	}
	routes := func(schema *ir.Schema) string {
		var sets []*ir.OperationSet
		for _, set := range schema.OperationSets {
			if set.IsIdentityRoutes() {
				sets = append(sets, set)
			}
		}
		data, err := json.Marshal(sets)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	want := routes(ts)
	for _, tc := range []struct{ file, body string }{
		{"src/account.schema.json", `{
  "operationSets": [
    {"name": "Account", "comment": "Signs users in and out, and lets anyone register: a new user waits for\nan administrator's approval as a member, and the first is the\nadministrator (D81). A signed-in user makes API keys (D114).", "operations": [],
     "userSessions": {"register": true, "registration": "approval", "defaultRoles": ["member"], "firstUser": {"role": "admin", "permissions": ["identity"]}, "apiKeys": true}},
    {"name": "AccountAdmin", "comment": "Manages users, roles and the grants between them, and lists and revokes\nany user's API keys.", "operations": [], "userAdministration": {"apiKeys": true}}
  ]
}`},
		{"src/account.schema.yaml", `operationSets:
  - name: Account
    comment: "Signs users in and out, and lets anyone register: a new user waits for\nan administrator's approval as a member, and the first is the\nadministrator (D81). A signed-in user makes API keys (D114)."
    operations: []
    userSessions:
      register: true
      registration: approval
      defaultRoles: [member]
      firstUser: { role: admin, permissions: [identity] }
      apiKeys: true
  - name: AccountAdmin
    comment: "Manages users, roles and the grants between them, and lists and revokes\nany user's API keys."
    operations: []
    userAdministration:
      apiKeys: true
`},
	} {
		t.Run(filepath.Ext(tc.file), func(t *testing.T) {
			schema, err := LoadService(userRoutesDataService(t, tc.file, tc.body))
			if err != nil {
				t.Fatalf("LoadService: %v", err)
			}
			if got := routes(schema); got != want {
				t.Errorf("route sets differ from the TypeScript form's\n got %s\nwant %s", got, want)
			}
			if schema.Types[ir.IdentityLoginInputType] == nil || schema.Enums[ir.IdentitySessionTransportEnum] == nil {
				t.Error("the types were not added")
			}
			for _, name := range ir.IdentityAPIKeyTypes {
				if schema.Types[name] == nil {
					t.Errorf("the API key type %s was not added", name)
				}
			}
		})
	}
}

// TestLoadUserRoutesAPIKeysAreOptIn: the API key routes (D114) are each
// set's own opt-in. A set without apiKeys has none of the five operations,
// and an API none of whose sets has it gets none of the three types and may
// declare a type named as one of them. With apiKeys on either set, the API
// reserves all three names, so the authored type is refused.
func TestLoadUserRoutesAPIKeysAreOptIn(t *testing.T) {
	// The API's own ApiKey, beside the route sets with the configs given.
	service := func(sessions, admin string) string {
		return `{
  "types": {"ApiKey": {"name": "ApiKey", "role": "EmbeddedStruct",
    "fields": [{"name": "label", "typeRef": {"name": "string"}, "required": true}]}},
  "operationSets": [
    {"name": "Account", "operations": [], "userSessions": ` + sessions + `},
    {"name": "AccountAdmin", "operations": [], "userAdministration": ` + admin + `}
  ]
}`
	}
	sessionOps := []string{"login", "logout", "me", "capabilities", "changePassword", "register"}
	adminOps := []string{
		"createUser", "listUsers", "getUser", "disableUser", "enableUser", "approveUser", "setUserPassword",
		"listRoles", "createRole", "updateRole", "deleteRole", "grantRole", "revokeRole",
	}
	operations := func(schema *ir.Schema, set string) []string {
		var names []string
		for _, s := range schema.OperationSets {
			if s.Name != set {
				continue
			}
			for _, op := range s.Operations {
				names = append(names, op.Name)
			}
		}
		return names
	}

	schema, err := LoadService(userRoutesDataService(t, "src/account.schema.json", service(`{"register": true}`, `{}`)))
	if err != nil {
		t.Fatalf("an API without apiKeys that declares ApiKey: LoadService = %v", err)
	}
	if got := operations(schema, "Account"); !slices.Equal(got, sessionOps) {
		t.Errorf("Account without apiKeys = %v, want %v", got, sessionOps)
	}
	if got := operations(schema, "AccountAdmin"); !slices.Equal(got, adminOps) {
		t.Errorf("AccountAdmin without apiKeys = %v, want %v", got, adminOps)
	}
	if own := schema.Types[ir.IdentityAPIKeyType]; own == nil || own.Origin != "" || len(own.Fields) != 1 || own.Fields[0].Name != "label" {
		t.Errorf("ApiKey = %+v, want the API's own type", own)
	}
	for _, name := range []string{ir.IdentityCreateAPIKeyInputType, ir.IdentityCreatedAPIKeyType} {
		if schema.Types[name] != nil {
			t.Errorf("the type %s was added without apiKeys", name)
		}
	}

	const refusal = "src/account.schema.json: ApiKey: the user model's routes add a type named ApiKey, which the schema already defines; rename it"
	for name, configs := range map[string][2]string{
		"apiKeys on the sessions":       {`{"register": true, "apiKeys": true}`, `{}`},
		"apiKeys on the administration": {`{"register": true}`, `{"apiKeys": true}`},
	} {
		_, err := LoadService(userRoutesDataService(t, "src/account.schema.json", service(configs[0], configs[1])))
		if err == nil || err.Error() != refusal {
			t.Errorf("%s: LoadService = %v, want %q", name, err, refusal)
		}
	}

	// Without the authored type, a set without apiKeys adds no operation
	// and no type of the API keys', and each set's apiKeys adds its own
	// operations and the types they use, and none of the other set's.
	routes := func(t *testing.T, sessions, admin string) *ir.Schema {
		t.Helper()
		body := `{"operationSets": [
  {"name": "Account", "operations": [], "userSessions": ` + sessions + `},
  {"name": "AccountAdmin", "operations": [], "userAdministration": ` + admin + `}
]}`
		schema, err := LoadService(userRoutesDataService(t, "src/account.schema.json", body))
		if err != nil {
			t.Fatalf("LoadService: %v", err)
		}
		return schema
	}
	sessionKeyOps := append(slices.Clone(sessionOps), "createApiKey", "listApiKeys", "revokeApiKey")
	adminKeyOps := append(slices.Clone(adminOps), "listUserApiKeys", "revokeUserApiKey")
	for _, tc := range []struct {
		name, sessions, admin   string
		wantSessions, wantAdmin []string
		wantTypes               []string
	}{
		{
			name:     "apiKeys on neither",
			sessions: `{"register": true}`, admin: `{}`,
			wantSessions: sessionOps, wantAdmin: adminOps,
		},
		{
			name:     "apiKeys written as false",
			sessions: `{"register": true, "apiKeys": false}`, admin: `{"apiKeys": false}`,
			wantSessions: sessionOps, wantAdmin: adminOps,
		},
		{
			name:     "apiKeys on the sessions alone",
			sessions: `{"register": true, "apiKeys": true}`, admin: `{}`,
			wantSessions: sessionKeyOps, wantAdmin: adminOps,
			wantTypes: []string{"ApiKey", "CreateApiKeyInput", "CreatedApiKey"},
		},
		{
			name:     "apiKeys on the administration alone",
			sessions: `{"register": true}`, admin: `{"apiKeys": true}`,
			wantSessions: sessionOps, wantAdmin: adminKeyOps,
			wantTypes: []string{"ApiKey"},
		},
		{
			name:     "apiKeys on both",
			sessions: `{"register": true, "apiKeys": true}`, admin: `{"apiKeys": true}`,
			wantSessions: sessionKeyOps, wantAdmin: adminKeyOps,
			wantTypes: []string{"ApiKey", "CreateApiKeyInput", "CreatedApiKey"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := routes(t, tc.sessions, tc.admin)
			if got := operations(schema, "Account"); !slices.Equal(got, tc.wantSessions) {
				t.Errorf("Account = %v, want %v", got, tc.wantSessions)
			}
			if got := operations(schema, "AccountAdmin"); !slices.Equal(got, tc.wantAdmin) {
				t.Errorf("AccountAdmin = %v, want %v", got, tc.wantAdmin)
			}
			var added []string
			for _, name := range ir.IdentityAPIKeyTypes {
				if td := schema.Types[name]; td != nil {
					if td.Origin != ir.OriginIdentity {
						t.Errorf("%s has origin %q, want the identity expansion's", name, td.Origin)
					}
					added = append(added, name)
				}
			}
			slices.Sort(added)
			if !slices.Equal(added, tc.wantTypes) {
				t.Errorf("API key types = %v, want %v", added, tc.wantTypes)
			}
		})
	}
}

// TestLoadUserRoutesRefusals: the verification pass refuses, in a data
// form, a route set with an authored operation and an authDb without the
// tables the sets need.
func TestLoadUserRoutesRefusals(t *testing.T) {
	dir := userRoutesDataService(t, "src/account.schema.json", `{
  "operationSets": [
    {"name": "Account", "operations": [{"name": "whoami", "typeRef": {"name": "string"}, "required": true, "httpMethod": "GET"}], "userSessions": {}}
  ]
}`)
	_, err := LoadService(dir)
	if want := "Account: a @userSessions class has no members: the loader adds its operations, and the identity runtime serves them"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("LoadService = %v, want an error containing %q", err, want)
	}

	db, err := LoadService(filepath.FromSlash("tsreader/testdata/services/fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	dir = userRoutesDataService(t, "src/account.schema.json", `{
  "operationSets": [{"name": "Account", "operations": [], "userSessions": {}}]
}`)
	_, err = LoadService(dir, WithDependencyLoader(func(string) (*ir.Schema, error) { return db, nil }))
	if want := "Account: @userSessions needs a User table in the authDb fixture-user-model-db"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("LoadService = %v, want an error containing %q", err, want)
	}
}

// accountsConfig is an @envVars type, in the JSON form, with one field
// named name.
func accountsConfig(name string) string {
	return `{"types": {"AccountsConfig": {"name": "AccountsConfig", "role": "EmbeddedStruct", "envVars": true,
  "fields": [{"name": "` + name + `", "typeRef": {"name": "string"}, "required": true}]}}}`
}

// TestLoadRefusesAnEnvVarsFieldThatTakesTheIdentityConfigField: outside a
// stack, an API whose authDb has a User table refuses an @envVars field
// named as its identity config field, or beginning with it and an
// underscore, without a route set: the loader reads the authDb for it. An
// authDb without a User table gives the API no such field, and an API none
// of whose fields takes the name reads no authDb.
func TestLoadRefusesAnEnvVarsFieldThatTakesTheIdentityConfigField(t *testing.T) {
	for _, name := range []string{"FIXTURE_USER_ROUTES_API_IDENTITY", "FIXTURE_USER_ROUTES_API_IDENTITY_TTL"} {
		_, err := LoadService(userRoutesDataService(t, "src/config.schema.json", accountsConfig(name)))
		want := "@envVars field " + name + " of AccountsConfig collides with FIXTURE_USER_ROUTES_API_IDENTITY, the identity config field of fixture-user-routes-api, whose authDb fixture-user-model-db has a User table; a stack's platform sets it, so rename the setting"
		if err == nil || err.Error() != want {
			t.Errorf("%s: LoadService = %v, want %q", name, err, want)
		}
	}

	db, err := LoadService(filepath.FromSlash("tsreader/testdata/services/fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	load := WithDependencyLoader(func(name string) (*ir.Schema, error) {
		asked = append(asked, name)
		return db, nil
	})
	dir := userRoutesDataService(t, "src/config.schema.json", accountsConfig("FIXTURE_USER_ROUTES_API_IDENTITY"))
	if _, err := LoadService(dir, load); err != nil {
		t.Errorf("an authDb without a User table: LoadService = %v", err)
	}
	if len(asked) != 1 || asked[0] != "fixture-user-model-db" {
		t.Errorf("the dependency loader was asked for %v, want fixture-user-model-db once", asked)
	}

	asked = nil
	dir = userRoutesDataService(t, "src/config.schema.json", accountsConfig("FIXTURE_USER_ROUTES_API_IDENTITIES"))
	if _, err := LoadService(dir, load); err != nil {
		t.Errorf("a field that only shares a prefix: LoadService = %v", err)
	}
	if len(asked) != 0 {
		t.Errorf("an API none of whose fields takes the identity config field's name read its authDb: %v", asked)
	}

	failing := WithDependencyLoader(func(string) (*ir.Schema, error) { return nil, errors.New("no such service") })
	_, err = LoadService(userRoutesDataService(t, "src/config.schema.json", accountsConfig("FIXTURE_USER_ROUTES_API_IDENTITY")), failing)
	const want = "fixture-user-routes-api: reading the authDb fixture-user-model-db, whose User table decides whether an @envVars field collides with FIXTURE_USER_ROUTES_API_IDENTITY, the identity config field: no such service"
	if err == nil || err.Error() != want {
		t.Errorf("LoadService = %v, want %q", err, want)
	}
}
