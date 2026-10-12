package loader

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
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
	if counts["Account"] != 6 || counts["AccountAdmin"] != 12 || counts["GreetingQueries"] != 1 {
		t.Errorf("operations per set = %v, want 6, 12 and 1", counts)
	}
	for _, name := range []string{"Auth.Password", "Contact.Email", "Identity.Name", "Identity.UUID", "Temporal.DateTime"} {
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
    {"name": "Account", "comment": "Signs users in and out, and lets anyone register.", "operations": [], "userSessions": {"register": true}},
    {"name": "AccountAdmin", "comment": "Manages users, roles and the grants between them.", "operations": [], "userAdministration": {}}
  ]
}`},
		{"src/account.schema.yaml", `operationSets:
  - name: Account
    comment: Signs users in and out, and lets anyone register.
    operations: []
    userSessions: { register: true }
  - name: AccountAdmin
    comment: Manages users, roles and the grants between them.
    operations: []
    userAdministration: {}
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
