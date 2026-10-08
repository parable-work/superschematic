package tsreader

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestUserRoutesAreRecorded: the route classes of fixture-user-routes-api
// are operation sets with their decorators' configs and no operations,
// which the loader adds; the API's other set is read as before.
func TestUserRoutesAreRecorded(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-user-routes-api"))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	sets := map[string]*ir.OperationSet{}
	for _, set := range schema.OperationSets {
		sets[set.Name] = set
	}
	account, admin, greeting := sets["Account"], sets["AccountAdmin"], sets["GreetingQueries"]
	if account == nil || admin == nil || greeting == nil {
		t.Fatalf("operation sets = %v", sets)
	}
	if want := (&ir.UserSessionsConfig{Register: true}); !reflect.DeepEqual(account.UserSessions, want) || account.UserAdministration != nil {
		t.Errorf("Account = %+v, want UserSessions %+v", account, want)
	}
	if want := (&ir.UserAdministrationConfig{}); !reflect.DeepEqual(admin.UserAdministration, want) || admin.UserSessions != nil {
		t.Errorf("AccountAdmin = %+v, want UserAdministration %+v", admin, want)
	}
	if len(account.Operations) != 0 || len(admin.Operations) != 0 {
		t.Errorf("the reader added operations: %v, %v", account.Operations, admin.Operations)
	}
	if account.Comment != "Signs users in and out, and lets anyone register." {
		t.Errorf("Account.Comment = %q", account.Comment)
	}
	if greeting.IsIdentityRoutes() || len(greeting.Operations) != 1 {
		t.Errorf("GreetingQueries = %+v", greeting)
	}
	for name := range schema.Types {
		if strings.HasSuffix(name, "Input") || name == ir.IdentityLoginResultType {
			t.Errorf("the reader added the type %s", name)
		}
	}
}

// TestUserRoutesConfigs: each config form reads into the set's config.
func TestUserRoutesConfigs(t *testing.T) {
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { userAdministration, userSessions } from "@superschematic/api";
@userSessions({ path: "account", login: false })
export class Me {}
@userAdministration({ path: "staff/admin" })
export class Admin {}
`})
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if got, want := schema.OperationSets[0].UserSessions, (&ir.UserSessionsConfig{Path: "account", NoLogin: true}); !reflect.DeepEqual(got, want) {
		t.Errorf("Me.UserSessions = %+v, want %+v", got, want)
	}
	if got, want := schema.OperationSets[1].UserAdministration, (&ir.UserAdministrationConfig{Path: "staff/admin"}); !reflect.DeepEqual(got, want) {
		t.Errorf("Admin.UserAdministration = %+v, want %+v", got, want)
	}
}

// TestUserRoutesRefusals pins each refusal of a route class's form, at
// the node that carries it. A config the decorator's type already refuses
// reaches the walk only past a @ts-expect-error.
func TestUserRoutesRefusals(t *testing.T) {
	const imports = `import { Authenticated, Encrypted, HttpMethod, requireService, rest, userAdministration, userSessions } from "@superschematic/api";
`
	cases := []struct {
		name   string
		kind   string
		source string
		want   []string
	}{
		{
			name: "a method",
			kind: "API",
			source: imports + `@userSessions()
export class Account {
  @rest(HttpMethod.GET, "auth/whoami")
  whoami(): string {
    throw new Error("schema declaration only");
  }
}
`,
			want: []string{`a.schema.ts:4:3: Account: a @userSessions class has no members: the loader adds its operations, and the identity runtime serves them`},
		},
		{
			name: "a property",
			kind: "API",
			source: imports + `@userAdministration()
export class Admin {
  note: string;
}
`,
			want: []string{`a.schema.ts:4:3: Admin: a @userAdministration class has no members: the loader adds its operations, and the identity runtime serves them`},
		},
		{
			name: "both decorators on one class",
			kind: "API",
			source: imports + `@userSessions()
@userAdministration()
export class Account {}
`,
			want: []string{`a.schema.ts:3:1: @userAdministration contradicts @userSessions on the same class: a class takes one of them`},
		},
		{
			name: "one decorator twice",
			kind: "API",
			source: imports + `@userSessions()
@userSessions({ register: true })
export class Account {}
`,
			want: []string{`a.schema.ts:3:1: @userSessions is declared twice on the same class`},
		},
		{
			name: "register without the login",
			kind: "API",
			source: imports + `// @ts-expect-error register needs the login
@userSessions({ login: false, register: true })
export class Account {}
`,
			want: []string{`a.schema.ts:3:15: @userSessions: register needs the login; drop register: true or login: false`},
		},
		{
			name: "an unknown key",
			kind: "API",
			source: imports + `// @ts-expect-error an unknown key
@userSessions({ roles: true })
export class Account {}
`,
			want: []string{`a.schema.ts:3:15: @userSessions config has unknown key "roles"; it takes login, path and register`},
		},
		{
			name: "an Authenticated base",
			kind: "API",
			source: imports + `@userSessions()
export class Account extends Authenticated {}
`,
			want: []string{`a.schema.ts:3:30: Account: a @userSessions class does not extend Authenticated: each of its routes takes the user model's rule`},
		},
		{
			name: "an Encrypted base",
			kind: "API",
			source: imports + `@userAdministration()
export class Admin extends Encrypted {}
`,
			want: []string{`a.schema.ts:3:28: Admin: a @userAdministration class is not Encrypted: the identity runtime reads its routes' bodies as plain JSON`},
		},
		{
			name: "a service clause",
			kind: "API",
			source: imports + `@userSessions()
@requireService()
export class Account {}
`,
			want: []string{`a.schema.ts:3:1: Account: a @userSessions class takes no service clause: each of its routes takes the user model's rule`},
		},
		{
			name: "a second @userSessions class",
			kind: "API",
			source: imports + `@userSessions()
export class Account {}
@userSessions({ login: false })
export class Me {}
`,
			want: []string{`a.schema.ts:4:1: Me: an API takes one @userSessions class, and Account is one`},
		},
		{
			name: "a second @userAdministration class",
			kind: "API",
			source: imports + `@userAdministration()
export class Admin {}
@userAdministration({ path: "staff" })
export class Staff {}
`,
			want: []string{`a.schema.ts:4:1: Staff: an API takes one @userAdministration class, and Admin is one`},
		},
		{
			name: "a DB schema",
			kind: "DB",
			source: `import { userSessions } from "@superschematic/api";
@userSessions()
export class Account {}
`,
			want: []string{`a.schema.ts:2:1: @userSessions is only allowed in an API schema (this service is kind DB)`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := decoratorTestService(t, tc.kind, map[string]string{"src/a.schema.ts": tc.source})
			_, _, err := LoadService(dir)
			if err == nil {
				t.Fatal("expected schema errors")
			}
			msg := err.Error()
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Errorf("missing diagnostic %q in:\n%s", want, msg)
				}
			}
		})
	}
}
