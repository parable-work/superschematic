package writer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRoundTripsUserRoutes: fixture-user-routes-api written in each format
// and read back over its authDb is the same expanded IR. Every format writes
// the route sets' decorators or keys, apiKeys on both (D114), and none
// writes the operations, types or enum the loader fills them with.
func TestRoundTripsUserRoutes(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-user-routes-api")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	authDB, err := loader.LoadService(tsFixtures + "/fixture-user-model-db")
	if err != nil {
		t.Fatalf("loading the authDb: %v", err)
	}
	want := normalizeIR(t, schema)
	contains := map[Format][]string{
		FormatTS: {
			`import { Authenticated, HttpMethod, rest, userAdministration, userSessions } from "@superschematic/api";`,
			"// administrator (D81). A signed-in user makes API keys (D114).\n@userSessions({ register: true, registration: \"approval\", defaultRoles: [\"member\"], firstUser: { role: \"admin\", permissions: [\"identity\"] }, apiKeys: true })\nexport class Account {}\n",
			"// any user's API keys.\n@userAdministration({ apiKeys: true })\nexport class AccountAdmin {}\n",
		},
		FormatJSON: {`"userSessions": {`, `"register": true`, `"registration": "approval"`, `"defaultRoles": [`, `"firstUser": {`, `"userAdministration": {`, `"apiKeys": true`, `"operations": []`},
		FormatYAML: {"userSessions:\n", "register: true", "registration: approval", "defaultRoles:", "firstUser:", "userAdministration:\n", "apiKeys: true", "operations: []"},
	}
	// Each set writes its own apiKeys, once.
	apiKeys := map[Format]string{FormatTS: "apiKeys: true", FormatJSON: `"apiKeys": true`, FormatYAML: "apiKeys: true"}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			dir := t.TempDir()
			if _, err := WriteService(schema, format, dir); err != nil {
				t.Fatalf("WriteService: %v", err)
			}
			cfg := fmt.Sprintf(`{"name": %q, "kind": "API", "authDb": "fixture-user-model-db", "outputs": {}}`+"\n", schema.Name)
			if err := os.WriteFile(filepath.Join(dir, "schema.config.json"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			if format == FormatTS {
				writeTSProject(t, dir, schema)
			}
			source := writtenSource(t, schema, format)
			for _, want := range contains[format] {
				if !strings.Contains(source, want) {
					t.Errorf("written %s lacks %q:\n%s", format, want, source)
				}
			}
			if got := strings.Count(source, apiKeys[format]); got != 2 {
				t.Errorf("written %s has %q %d times, want once for each route set", format, apiKeys[format], got)
			}
			added := []string{ir.IdentityLoginInputType, ir.IdentitySessionTransportEnum, "changePassword", "auth/login", "identity.users", "createApiKey", "listUserApiKeys", "auth/keys"}
			for _, added := range append(added, ir.IdentityAPIKeyTypes...) {
				if strings.Contains(source, added) {
					t.Errorf("written %s carries the loader's %s:\n%s", format, added, source)
				}
			}
			reloaded, err := loader.LoadService(dir, loader.WithDependencyLoader(func(string) (*ir.Schema, error) { return authDB, nil }))
			if err != nil {
				dumpService(t, dir)
				t.Fatalf("reloading %s output: %v", format, err)
			}
			if got := normalizeIR(t, reloaded); got != want {
				t.Errorf("IR mismatch after writing %s\nwant:\n%s\ngot:\n%s", format, want, got)
			}
		})
	}
}

// TestWriterKeepsAuthoredOperationsOfARouteSet: withoutExpansion leaves an
// operation set's authored operations, and only a set that holds the
// loader's changes.
func TestWriterKeepsAuthoredOperationsOfARouteSet(t *testing.T) {
	authored := &ir.FieldDef{Name: "ping", TypeRef: ir.TypeRef{Name: "boolean"}, HTTPMethod: "GET"}
	plain := &ir.OperationSet{Name: "Ping", Operations: []*ir.FieldDef{authored}}
	routes := &ir.OperationSet{Name: "Account", UserSessions: &ir.UserSessionsConfig{}, Operations: []*ir.FieldDef{
		{Name: ir.IdentityOpMe, IdentityOperation: ir.IdentityOpMe, Origin: ir.OriginIdentity},
	}}
	schema := ir.NewSchema("api", ir.SchemaKindAPI)
	schema.OperationSets = []*ir.OperationSet{plain, routes}
	view := withoutExpansion(schema)
	if view.OperationSets[0] != plain {
		t.Error("a set without the loader's operations was copied")
	}
	if got := view.OperationSets[1]; got == routes || got.Operations == nil || len(got.Operations) != 0 || got.UserSessions != routes.UserSessions {
		t.Errorf("route set view = %+v", got)
	}
	if len(routes.Operations) != 1 {
		t.Error("withoutExpansion changed the schema")
	}
}

// TestTSWriterWritesRouteConfigs: the TypeScript writer writes each config
// key that differs from its default, and the set's middleware.
func TestTSWriterWritesRouteConfigs(t *testing.T) {
	limit := 30
	schema := ir.NewSchema("api", ir.SchemaKindAPI)
	schema.OperationSets = []*ir.OperationSet{
		{Name: "Me", Operations: []*ir.FieldDef{}, UserSessions: &ir.UserSessionsConfig{Path: "account", NoLogin: true}},
		{Name: "Staff", Operations: []*ir.FieldDef{}, UserAdministration: &ir.UserAdministrationConfig{Path: "staff/admin"}, Middleware: &ir.MiddlewareConfig{RateLimit: &limit}},
	}
	source := writtenSource(t, schema, FormatTS)
	for _, want := range []string{
		"@userSessions({ path: \"account\", login: false })\nexport class Me {}\n",
		"@userAdministration({ path: \"staff/admin\" })\n@rateLimit({ requestsPerMinute: 30 })\nexport class Staff {}\n",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("written TypeScript lacks %q:\n%s", want, source)
		}
	}
}

// TestTSWriterWritesAPIKeys: the TypeScript writer writes apiKeys: true
// (D114) on the decorator of the set that has it, after its other keys,
// and nothing for a set without it.
func TestTSWriterWritesAPIKeys(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sessions ir.UserSessionsConfig
		admin    ir.UserAdministrationConfig
		want     []string
	}{
		{
			name:     "both sets",
			sessions: ir.UserSessionsConfig{APIKeys: true},
			admin:    ir.UserAdministrationConfig{APIKeys: true},
			want: []string{
				"@userSessions({ apiKeys: true })\nexport class Account {}\n",
				"@userAdministration({ apiKeys: true })\nexport class Staff {}\n",
			},
		},
		{
			name:     "after the other keys",
			sessions: ir.UserSessionsConfig{Path: "account", Register: true, APIKeys: true},
			admin:    ir.UserAdministrationConfig{Path: "staff/admin", APIKeys: true},
			want: []string{
				"@userSessions({ path: \"account\", register: true, apiKeys: true })\nexport class Account {}\n",
				"@userAdministration({ path: \"staff/admin\", apiKeys: true })\nexport class Staff {}\n",
			},
		},
		{
			name:     "the sessions alone",
			sessions: ir.UserSessionsConfig{APIKeys: true},
			want: []string{
				"@userSessions({ apiKeys: true })\nexport class Account {}\n",
				"@userAdministration()\nexport class Staff {}\n",
			},
		},
		{
			name:  "the administration alone",
			admin: ir.UserAdministrationConfig{APIKeys: true},
			want: []string{
				"@userSessions()\nexport class Account {}\n",
				"@userAdministration({ apiKeys: true })\nexport class Staff {}\n",
			},
		},
		{
			name: "neither",
			want: []string{
				"@userSessions()\nexport class Account {}\n",
				"@userAdministration()\nexport class Staff {}\n",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := ir.NewSchema("api", ir.SchemaKindAPI)
			schema.OperationSets = []*ir.OperationSet{
				{Name: "Account", Operations: []*ir.FieldDef{}, UserSessions: &tc.sessions},
				{Name: "Staff", Operations: []*ir.FieldDef{}, UserAdministration: &tc.admin},
			}
			source := writtenSource(t, schema, FormatTS)
			for _, want := range tc.want {
				if !strings.Contains(source, want) {
					t.Errorf("written TypeScript lacks %q:\n%s", want, source)
				}
			}
			wantCount := 0
			if tc.sessions.APIKeys {
				wantCount++
			}
			if tc.admin.APIKeys {
				wantCount++
			}
			if got := strings.Count(source, "apiKeys"); got != wantCount {
				t.Errorf("written TypeScript mentions apiKeys %d times, want %d:\n%s", got, wantCount, source)
			}
		})
	}
}
