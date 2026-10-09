package verify

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// routesAPI returns an API schema whose authDb is "users", with a
// @userSessions set that lets anyone register and a @userAdministration
// set, neither with operations yet.
func routesAPI() *ir.Schema {
	schema := ir.NewSchema("users-api", ir.SchemaKindAPI)
	schema.AuthDB = "users"
	schema.OperationSets = []*ir.OperationSet{
		{Name: "Account", Operations: []*ir.FieldDef{}, UserSessions: &ir.UserSessionsConfig{Register: true}},
		{Name: "AccountAdmin", Operations: []*ir.FieldDef{}, UserAdministration: &ir.UserAdministrationConfig{}},
	}
	return schema
}

// TestUserModelRoutes runs every rule of the route sets once where it
// holds and once where it breaks, against userModel as the authDb. A case
// with no want must verify clean.
func TestUserModelRoutes(t *testing.T) {
	sessions := func(s *ir.Schema) *ir.OperationSet { return s.OperationSets[0] }
	admin := func(s *ir.Schema) *ir.OperationSet { return s.OperationSets[1] }
	account := func(db *ir.Schema) *ir.TypeDef { return db.Types["Account"] }
	addAccountField := func(db *ir.Schema, fd *ir.FieldDef) { account(db).Fields = append(account(db).Fields, fd) }
	dateTime := &ir.ScalarDef{Name: "Temporal.DateTime", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "TIMESTAMPTZ", "json_schema": "string"}}
	cases := []struct {
		name string
		api  func(s *ir.Schema)
		db   func(db *ir.Schema)
		noDB bool
		want string
	}{
		{name: "the valid routes"},
		{name: "sessions alone, without a UserRole table", api: func(s *ir.Schema) { s.OperationSets = s.OperationSets[:1] }, db: func(db *ir.Schema) { delete(db.Types, "Role") }},
		{name: "a path of segments", api: func(s *ir.Schema) {
			sessions(s).UserSessions.Path = "v1/account"
			admin(s).UserAdministration.Path = "v1/account/admin"
		}},
		{name: "an API without a route set needs no authDb", api: func(s *ir.Schema) {
			s.AuthDB = ""
			s.OperationSets = nil
		}, noDB: true},

		// The set's own form.
		{
			name: "a route set in a General schema",
			api:  func(s *ir.Schema) { s.Kind = ir.SchemaKindGeneral },
			want: "Account: @userSessions is only allowed in an API schema (this schema has kind General)",
		},
		{
			name: "both decorators on one set",
			api: func(s *ir.Schema) {
				sessions(s).UserAdministration = &ir.UserAdministrationConfig{}
				s.OperationSets = s.OperationSets[:1]
			},
			want: "Account: a class takes @userSessions or @userAdministration, not both",
		},
		{
			name: "register without the login",
			api:  func(s *ir.Schema) { sessions(s).UserSessions.NoLogin = true },
			want: "Account: @userSessions: register needs the login; drop register: true or login: false",
		},
		{
			name: "an authored operation",
			api: func(s *ir.Schema) {
				admin(s).Operations = []*ir.FieldDef{{Name: "audit", TypeRef: ir.TypeRef{Name: "string"}, HTTPMethod: "GET"}}
			},
			want: "AccountAdmin: a @userAdministration class has no members: the loader adds its operations, and the identity runtime serves them",
		},
		{
			name: "operations the expansion added",
			api: func(s *ir.Schema) {
				sessions(s).Operations = []*ir.FieldDef{{Name: ir.IdentityOpMe, IdentityOperation: ir.IdentityOpMe}}
			},
		},
		{
			name: "an Encrypted set",
			api:  func(s *ir.Schema) { sessions(s).Encrypted = true },
			want: "Account: a @userSessions class is not Encrypted: the identity runtime reads its routes' bodies as plain JSON",
		},
		{
			name: "a service clause",
			api: func(s *ir.Schema) {
				admin(s).ServiceCallers = &ir.ServiceCallers{Mode: ir.ServiceCallersRequire}
			},
			want: "AccountAdmin: a @userAdministration class takes no service clause: each of its routes takes the user model's rule",
		},
		{
			name: "a second @userSessions set",
			api: func(s *ir.Schema) {
				s.OperationSets = append(s.OperationSets, &ir.OperationSet{Name: "Me", UserSessions: &ir.UserSessionsConfig{NoLogin: true}})
			},
			want: "Me: an API takes one @userSessions class, and Account is one",
		},
		{
			name: "a second @userAdministration set",
			api: func(s *ir.Schema) {
				s.OperationSets = append(s.OperationSets, &ir.OperationSet{Name: "Staff", UserAdministration: &ir.UserAdministrationConfig{Path: "staff"}})
			},
			want: "Staff: an API takes one @userAdministration class, and AccountAdmin is one",
		},
		{
			name: "a path with a leading slash",
			api:  func(s *ir.Schema) { sessions(s).UserSessions.Path = "/auth" },
			want: `Account: @userSessions path "/auth" must be route segments joined by /, with no leading or trailing / and no {parameter}`,
		},
		{
			name: "a path with a parameter",
			api:  func(s *ir.Schema) { admin(s).UserAdministration.Path = "tenants/{tenant}/admin" },
			want: `AccountAdmin: @userAdministration path "tenants/{tenant}/admin" must be route segments joined by /, with no leading or trailing / and no {parameter}`,
		},

		// The reserved type names.
		{
			name: "an authored type named as an added one",
			api: func(s *ir.Schema) {
				s.Types["LoginInput"] = &ir.TypeDef{Name: "LoginInput", Owner: "src/account.schema.ts", Role: ir.RoleAPIInput}
			},
			want: "src/account.schema.ts: LoginInput: the user model's routes add a type named LoginInput, which the schema already defines; rename it",
		},
		{
			name: "an authored type named as one the sets do not use",
			api: func(s *ir.Schema) {
				s.OperationSets = s.OperationSets[:1]
				s.Types["IdentityRole"] = &ir.TypeDef{Name: "IdentityRole", Role: ir.RoleEmbeddedStruct}
			},
			want: "IdentityRole: the user model's routes add a type named IdentityRole, which the schema already defines; rename it",
		},
		{
			name: "an authored enum named as the added one",
			api:  func(s *ir.Schema) { s.Enums["SessionTransport"] = &ir.EnumDef{Name: "SessionTransport"} },
			want: "SessionTransport: the user model's routes add a type named SessionTransport, which the schema already defines; rename it",
		},
		{
			name: "a type the expansion added",
			api: func(s *ir.Schema) {
				s.Types["LoginInput"] = &ir.TypeDef{Name: "LoginInput", Role: ir.RoleAPIInput, Origin: ir.OriginIdentity}
			},
		},

		// The authDb.
		{
			name: "no authDb",
			api:  func(s *ir.Schema) { s.AuthDB = "" },
			noDB: true,
			want: "Account: @userSessions needs the API's config to name its authDb, the DB schema whose User table holds its users",
		},
		{
			name: "an authDb the load did not read",
			noDB: true,
			want: "Account: @userSessions reads the User table of the authDb users, which this load did not read",
		},
		{
			name: "an authDb that is not a DB schema",
			db:   func(db *ir.Schema) { db.Kind = ir.SchemaKindGeneral },
			want: "Account: @userSessions needs the authDb users to be a DB schema, and it has kind General",
		},
		{
			name: "an authDb without a User table",
			db: func(db *ir.Schema) {
				account(db).User = nil
				delete(db.Types, "Role")
			},
			want: "Account: @userSessions needs a User table in the authDb users, a table that implements User from @superschematic/db, and it has none",
		},
		{
			name: "administration without a UserRole table",
			db:   func(db *ir.Schema) { delete(db.Types, "Role") },
			want: "AccountAdmin: @userAdministration needs a UserRole table in the authDb users, a table that implements UserRole from @superschematic/db, and it has none",
		},
		{
			name: "administration alone names it first",
			api:  func(s *ir.Schema) { s.OperationSets = s.OperationSets[1:]; s.AuthDB = "" },
			noDB: true,
			want: "AccountAdmin: @userAdministration needs the API's config to name its authDb",
		},

		// The v1 rule: register and the administration routes write a
		// user's key, login and name alone.
		{
			name: "a required field without a default",
			db: func(db *ir.Schema) {
				addAccountField(db, &ir.FieldDef{Name: "plan", TypeRef: ir.TypeRef{Name: "string"}, Required: true})
			},
			want: "Account's @userSessions({ register: true }) and AccountAdmin's @userAdministration create users with their key, login and name alone, and Account.plan in the authDb users is required and has no default; make it optional or give it a @default",
		},
		{
			name: "a required field and administration alone",
			api:  func(s *ir.Schema) { s.OperationSets = s.OperationSets[1:] },
			db: func(db *ir.Schema) {
				addAccountField(db, &ir.FieldDef{Name: "plan", TypeRef: ir.TypeRef{Name: "string"}, Required: true})
			},
			want: "AccountAdmin's @userAdministration create users with their key, login and name alone, and Account.plan in the authDb users is required",
		},
		{
			name: "a required relation",
			db: func(db *ir.Schema) {
				addAccountField(db, &ir.FieldDef{Name: "team", TypeRef: ir.TypeRef{Name: "Role"}, Required: true, Relation: &ir.RelationDef{Type: "Role"}})
			},
			want: "Account.team in the authDb users is required and has no default",
		},
		{
			name: "a required list",
			db: func(db *ir.Schema) {
				addAccountField(db, &ir.FieldDef{Name: "tags", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true})
			},
			want: "Account.tags in the authDb users is required and has no default",
		},
		{
			name: "a required field and sessions without register",
			api: func(s *ir.Schema) {
				sessions(s).UserSessions.Register = false
				s.OperationSets = s.OperationSets[:1]
			},
			db: func(db *ir.Schema) {
				addAccountField(db, &ir.FieldDef{Name: "plan", TypeRef: ir.TypeRef{Name: "string"}, Required: true})
			},
		},
		{
			name: "a required field with a default",
			db: func(db *ir.Schema) {
				plan := "free"
				addAccountField(db, &ir.FieldDef{Name: "plan", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Default: &plan})
			},
		},
		{
			name: "a generated field",
			db: func(db *ir.Schema) {
				addAccountField(db, &ir.FieldDef{Name: "serial", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, AutoGenerated: true})
			},
		},
		{
			name: "an audit timestamp sqlgen defaults",
			db: func(db *ir.Schema) {
				db.Scalars[dateTime.Name] = dateTime
				addAccountField(db, &ir.FieldDef{Name: "createdAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true})
			},
		},
		{
			name: "a list of timestamps, which sqlgen does not default",
			db: func(db *ir.Schema) {
				db.Scalars[dateTime.Name] = dateTime
				addAccountField(db, &ir.FieldDef{Name: "seenAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime", IsArray: true}, Required: true})
			},
			want: "Account.seenAt in the authDb users is required and has no default",
		},
		{
			name: "a has-many relation",
			db: func(db *ir.Schema) {
				addAccountField(db, &ir.FieldDef{Name: "notes", TypeRef: ir.TypeRef{Name: "Role", IsArray: true}, Required: true, HasMany: true})
			},
		},
		{
			name: "a required field the trait does not name",
			db: func(db *ir.Schema) {
				account(db).User = &ir.UserTrait{Login: "email"}
			},
			want: "Account.displayName in the authDb users is required and has no default",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := routesAPI()
			if tc.api != nil {
				tc.api(api)
			}
			var in Input
			if !tc.noDB {
				in.AuthDB = userModel()
				if tc.db != nil {
					tc.db(in.AuthDB)
				}
			}
			r := Run(api, in)
			if tc.want == "" {
				if len(r.Errors) > 0 {
					t.Fatalf("want a clean verification, got %v", errorStrings(r))
				}
				return
			}
			if !hasError(r, tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, errorStrings(r))
			}
		})
	}
}
