package identitydesc_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/identitydesc"
	"github.com/parable-work/superschematic/internal/generator/identitydesc/identitytest"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// withRoles is the descriptor of the schema with a Role table whose User
// trait names displayName: every member, in the order the runtimes read.
const withRoles = `{
  "version": 1,
  "user": {
    "type": "Account",
    "table": "account",
    "columns": {
      "key": "id",
      "login": "email",
      "name": "display_name"
    },
    "keyScalar": "Identity.UUID",
    "loginScalar": "Contact.Email"
  },
  "session": {
    "table": "session",
    "columns": {
      "id": "id",
      "user": "user_id",
      "tokenHash": "token_hash",
      "createdAt": "created_at",
      "expiresAt": "expires_at",
      "lastSeenAt": "last_seen_at",
      "revokedAt": "revoked_at"
    }
  },
  "credential": {
    "table": "user_credential",
    "columns": {
      "id": "id",
      "user": "user_id",
      "passwordHash": "password_hash",
      "passwordChangedAt": "password_changed_at",
      "disabledAt": "disabled_at"
    }
  },
  "role": {
    "type": "Role",
    "table": "role",
    "columns": {
      "key": "id",
      "name": "name",
      "permissions": "permissions"
    }
  },
  "roleGrant": {
    "table": "user_role_grant",
    "columns": {
      "id": "id",
      "user": "user_id",
      "role": "role_id",
      "grantedAt": "granted_at"
    }
  }
}
`

func describe(t *testing.T, schema *ir.Schema) identitydesc.Descriptor {
	t.Helper()
	d, ok, err := identitydesc.Describe(schema)
	if err != nil || !ok {
		t.Fatalf("Describe = %v, %v; want a descriptor", ok, err)
	}
	return d
}

func encode(t *testing.T, d identitydesc.Descriptor) string {
	t.Helper()
	b, err := d.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestDescribeWithRoles checks the whole document for the schema with a
// Role table, and that two encodings of it are the same bytes.
func TestDescribeWithRoles(t *testing.T) {
	schema := identitytest.Schema(t, identitytest.Options{Roles: true, Name: "displayName"})
	got := encode(t, describe(t, schema))
	if got != withRoles {
		t.Fatalf("descriptor:\n%s\nwant:\n%s", got, withRoles)
	}
	if again := encode(t, describe(t, schema)); again != got {
		t.Fatalf("a second encoding differs:\n%s", again)
	}
}

// TestDescribeVariants covers a schema without roles, whose descriptor has
// no role members; a user key that is not a UUID; and a login whose field
// name and column differ, which is also the name when the trait names
// none.
func TestDescribeVariants(t *testing.T) {
	t.Run("no roles", func(t *testing.T) {
		d := describe(t, identitytest.Schema(t, identitytest.Options{Name: "displayName"}))
		if d.Role != nil || d.RoleGrant != nil {
			t.Fatalf("role %+v, roleGrant %+v; want neither", d.Role, d.RoleGrant)
		}
		text := encode(t, d)
		if strings.Contains(text, `"role`) {
			t.Fatalf("the descriptor names a role member:\n%s", text)
		}
		want, _, _ := strings.Cut(withRoles, `,
  "role": {`)
		if text != want+"\n}\n" {
			t.Fatalf("descriptor:\n%s\nwant the roles' descriptor without its role members", text)
		}
	})
	t.Run("integer key", func(t *testing.T) {
		d := describe(t, identitytest.Schema(t, identitytest.Options{Roles: true, Key: identitytest.Int64}))
		if d.User.KeyScalar != identitytest.Int64 || d.User.Columns.Key != "id" {
			t.Fatalf("user key %q of %s, want id of %s", d.User.Columns.Key, d.User.KeyScalar, identitytest.Int64)
		}
		if d.Session.Columns.User != "user_id" || d.RoleGrant.Columns.User != "user_id" {
			t.Fatalf("session user %q, grant user %q; want user_id", d.Session.Columns.User, d.RoleGrant.Columns.User)
		}
	})
	t.Run("login column", func(t *testing.T) {
		d := describe(t, identitytest.Schema(t, identitytest.Options{Login: "emailAddress"}))
		if d.User.Columns.Login != "email_address" || d.User.Columns.Name != "email_address" {
			t.Fatalf("login %q, name %q; want email_address for both", d.User.Columns.Login, d.User.Columns.Name)
		}
		if d.User.LoginScalar != identitytest.Email {
			t.Fatalf("login scalar %q, want %s", d.User.LoginScalar, identitytest.Email)
		}
	})
}

// TestDescribeWithoutAUserTable: a schema with no User table has no
// descriptor and no error.
func TestDescribeWithoutAUserTable(t *testing.T) {
	schema, err := loader.LoadService(filepath.FromSlash("../../loader/tsreader/testdata/services/fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := identitydesc.Describe(schema); ok || err != nil {
		t.Fatalf("Describe = %v, %v; want no descriptor and no error", ok, err)
	}
}

// TestDescribeRefuses checks each schema the descriptor cannot name: an
// added table missing, or authored rather than added, a field missing, and
// a relation to another table.
func TestDescribeRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ir.Schema)
		want string
	}{
		{"no Session table", func(s *ir.Schema) { delete(s.Types, ir.IdentitySessionTable) },
			"identitydesc: the schema has a User table but no Session table, which the loader adds beside it"},
		{"no UserCredential table", func(s *ir.Schema) { delete(s.Types, ir.IdentityCredentialTable) },
			"identitydesc: the schema has a User table but no UserCredential table, which the loader adds beside it"},
		{"no UserRoleGrant table", func(s *ir.Schema) { delete(s.Types, ir.IdentityRoleGrantTable) },
			"identitydesc: the schema has a User table but no UserRoleGrant table, which the loader adds beside it"},
		{"an authored Session table", func(s *ir.Schema) { s.Types[ir.IdentitySessionTable].Origin = "" },
			`identitydesc: the schema's Session table is not the one the loader adds beside its User table: its origin is "", not "identity"`},
		{"no login field", func(s *ir.Schema) { s.Types["Account"].User.Login = "handle" },
			"identitydesc: table Account has no field handle"},
		{"no role permissions", func(s *ir.Schema) { dropField(s.Types["Role"], ir.IdentityRolePermissionsField) },
			"identitydesc: table Role has no field permissions"},
		{"no session key", func(s *ir.Schema) { dropField(s.Types[ir.IdentitySessionTable], "id") },
			"identitydesc: table Session has no key field"},
		{"no revokedAt", func(s *ir.Schema) { dropField(s.Types[ir.IdentitySessionTable], ir.IdentitySessionRevokedAtField) },
			"identitydesc: table Session has no field revokedAt"},
		{"a grant's role names the user table", func(s *ir.Schema) {
			for _, fd := range s.Types[ir.IdentityRoleGrantTable].Fields {
				if fd.Name == ir.IdentityRoleGrantRoleField {
					fd.TypeRef.Name, fd.Relation.Type = "Account", "Account"
				}
			}
		}, "identitydesc: UserRoleGrant.role is not a relation to Role"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := identitytest.Schema(t, identitytest.Options{Roles: true})
			tc.edit(schema)
			_, ok, err := identitydesc.Describe(schema)
			if ok || err == nil || err.Error() != tc.want {
				t.Fatalf("Describe = %v, %v; want the error %q", ok, err, tc.want)
			}
		})
	}
}

func dropField(td *ir.TypeDef, name string) {
	kept := td.Fields[:0]
	for _, fd := range td.Fields {
		if fd.Name != name {
			kept = append(kept, fd)
		}
	}
	td.Fields = kept
}

// TestDescriptorNamesTheGeneratedTables checks each variant's descriptor
// against the DDL the sql generator writes for the same schema: every
// table and column it names exists, the column a store writes quoted is
// the one the DDL created, and each relation column is a foreign key to
// the user or role table's key.
func TestDescriptorNamesTheGeneratedTables(t *testing.T) {
	for name, opts := range map[string]identitytest.Options{
		"roles":         {Roles: true, Name: "displayName"},
		"no roles":      {},
		"integer key":   {Roles: true, Key: identitytest.Int64},
		"login column":  {Roles: true, Login: "emailAddress"},
		"name is login": {Roles: true},
	} {
		t.Run(name, func(t *testing.T) {
			schema := identitytest.Schema(t, opts)
			d := describe(t, schema)
			ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: identitytest.Service})
			if err != nil {
				t.Fatal(err)
			}
			tables := map[string]sqlgen.Table{}
			for _, table := range ddl.Tables {
				tables[table.Name] = table
			}
			check := func(table string, columns map[string]string, refs map[string]string) {
				t.Helper()
				created, ok := tables[table]
				if !ok {
					t.Errorf("the descriptor names table %s, which the DDL does not create", table)
					return
				}
				if created.QuotedName != table && created.QuotedName != `"`+table+`"` {
					t.Errorf("the DDL creates table %s as %s, which quoting %s does not name", table, created.QuotedName, table)
				}
				for role, column := range columns {
					var found bool
					for _, col := range created.Columns {
						if col.Name != column {
							continue
						}
						found = true
						if col.QuotedName != column && col.QuotedName != `"`+column+`"` {
							t.Errorf("%s.%s (%s) is created as %s, which quoting it does not name", table, column, role, col.QuotedName)
						}
					}
					if !found {
						t.Errorf("the descriptor names %s.%s as %s, which the DDL does not create", table, column, role)
					}
				}
				for column, target := range refs {
					var found bool
					for _, fk := range created.ForeignKeys {
						if fk.Column == column && fk.RefTable == target && fk.OnDelete == "CASCADE" {
							found = true
						}
					}
					if !found {
						t.Errorf("%s.%s is no cascading foreign key to %s", table, column, target)
					}
				}
			}
			u := d.User.Columns
			check(d.User.Table, map[string]string{"key": u.Key, "login": u.Login, "name": u.Name}, nil)
			if pk := tables[d.User.Table].PrimaryKey; pk != u.Key && pk != `"`+u.Key+`"` {
				t.Errorf("the user table's primary key is %s, the descriptor's key %s", tables[d.User.Table].PrimaryKey, u.Key)
			}
			s := d.Session.Columns
			check(d.Session.Table, map[string]string{
				"id": s.ID, "user": s.User, "tokenHash": s.TokenHash, "createdAt": s.CreatedAt,
				"expiresAt": s.ExpiresAt, "lastSeenAt": s.LastSeenAt, "revokedAt": s.RevokedAt,
			}, map[string]string{s.User: d.User.Table})
			c := d.Credential.Columns
			check(d.Credential.Table, map[string]string{
				"id": c.ID, "user": c.User, "passwordHash": c.PasswordHash,
				"passwordChangedAt": c.PasswordChangedAt, "disabledAt": c.DisabledAt,
			}, map[string]string{c.User: d.User.Table})
			if (d.Role != nil) != opts.Roles || (d.RoleGrant != nil) != opts.Roles {
				t.Fatalf("role %v, roleGrant %v; want both only with roles", d.Role != nil, d.RoleGrant != nil)
			}
			if !opts.Roles {
				return
			}
			r := d.Role.Columns
			check(d.Role.Table, map[string]string{"key": r.Key, "name": r.Name, "permissions": r.Permissions}, nil)
			g := d.RoleGrant.Columns
			check(d.RoleGrant.Table, map[string]string{"id": g.ID, "user": g.User, "role": g.Role, "grantedAt": g.GrantedAt},
				map[string]string{g.User: d.User.Table, g.Role: d.Role.Table})
		})
	}
}
