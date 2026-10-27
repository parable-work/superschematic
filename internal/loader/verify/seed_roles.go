package verify

import (
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// checkSeedRoles checks @seedRoles (D88) in every form: it is on a DB
// service's UserRole table; each role is named once, and each of its
// permissions has a permission's form and is listed once; and the table
// takes a row from a name and permissions alone, as the seed's insert
// writes it, so every other field is optional, generated, or defaulted by
// its column. Whether a name fits the name field's scalar is the loader's
// check of the values a schema writes.
func checkSeedRoles(schema *ir.Schema, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil || len(td.SeedRoles) == 0 {
			continue
		}
		if schema.Kind != ir.SchemaKindDB || td.UserRole == nil {
			r.errorf(td.Owner, "%s: @seedRoles seeds the roles of the table that implements UserRole from @superschematic/db, and %s does not", td.Name, td.Name)
			continue
		}
		seen := map[string]bool{}
		for _, seed := range td.SeedRoles {
			if seed == nil {
				continue
			}
			switch {
			case strings.TrimSpace(seed.Name) == "":
				r.errorf(td.Owner, "%s: @seedRoles holds a role with no name", td.Name)
			case seen[seed.Name]:
				r.errorf(td.Owner, "%s: @seedRoles names the role %q twice", td.Name, seed.Name)
			}
			seen[seed.Name] = true
			listed := map[string]bool{}
			for _, permission := range seed.Permissions {
				switch {
				case !identityPermission.MatchString(permission):
					r.errorf(td.Owner, "%s: @seedRoles role %q's permission %q is not dotted segments of letters, digits, _ and -, such as orders.read", td.Name, seed.Name, permission)
				case listed[permission]:
					r.errorf(td.Owner, "%s: @seedRoles role %q lists the permission %q twice", td.Name, seed.Name, permission)
				}
				listed[permission] = true
			}
		}
		scalarTypes := sqlutil.ScalarSQLTypes(schema)
		written := map[string]bool{ir.IdentityRoleNameField: true, ir.IdentityRolePermissionsField: true}
		for _, fd := range td.Fields {
			if fd == nil || fd.Key || written[fd.Name] || !needsValueOnCreate(schema, td, fd, scalarTypes) {
				continue
			}
			r.errorf(td.Owner, "%s: @seedRoles inserts each role with its name and permissions alone, and %s.%s is required and has no default its column carries; make it optional or give it a Default<T, V>", td.Name, td.Name, fd.Name)
		}
	}
}

// checkSeededRoles checks the roles an API's registration grants against
// the roles its authDb seeds (D88), and warns, since another API of the
// same authDb may need what this one does not:
//
//   - a role defaultRoles names that no seed holds: the first registration
//     creates it with no permissions, so a user approved into it can do
//     nothing until an administrator gives it some;
//   - a seeded permission that covers no permission the API requires, its
//     operations' and the user model's administration routes' (under
//     prefix): a misspelling grants nothing;
//   - firstUser's role when a seed holds it: the seed's permissions hold,
//     not firstUser's, which apply only to a role register creates;
//   - under registration "approval", a default role whose seeded
//     permissions the first administrator's do not cover: approveUser
//     needs the approver's permissions to cover the user's, so that
//     administrator could approve no one.
func checkSeededRoles(schema, authDB *ir.Schema, sessions *ir.OperationSet, prefix string, r *Result) {
	if authDB == nil || authDB.Kind != ir.SchemaKindDB {
		return
	}
	roles := authDB.UserRoleTable()
	if roles == nil {
		return
	}
	seeded := map[string]*ir.RoleSeed{}
	for _, seed := range roles.SeedRoles {
		if seed != nil {
			seeded[seed.Name] = seed
		}
	}
	required := apiPermissions(schema, prefix)
	names := make([]string, 0, len(seeded))
	for name := range seeded {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, permission := range seeded[name].Permissions {
			if !coversAny(permission, required) {
				r.warnf("", "%s's @seedRoles gives the role %s the permission %s, which covers no permission %s requires (%s)", authDB.Name, name, permission, schema.Name, strings.Join(required, ", "))
			}
		}
	}
	if sessions == nil || sessions.UserSessions == nil {
		return
	}
	cfg := sessions.UserSessions
	for _, role := range cfg.DefaultRoles {
		if seeded[role] == nil {
			r.warnf("", "%s: @userSessions defaultRoles grants %s, which %s's @seedRoles does not seed: the first registration creates it with no permissions, so a user approved into it can do nothing until an administrator gives it some; seed it with @seedRoles on %s", sessions.Name, role, authDB.Name, roles.Name)
		}
	}
	first := cfg.FirstUser
	if first == nil {
		return
	}
	admin := first.Permissions
	if seed := seeded[first.Role]; seed != nil {
		admin = seed.Permissions
		if strings.Join(seed.Permissions, ",") != strings.Join(first.Permissions, ",") {
			r.warnf("", "%s: @userSessions firstUser gives %s the permissions %s, and %s's @seedRoles seeds it with %s, which hold: firstUser's apply only to a role register creates", sessions.Name, first.Role, strings.Join(first.Permissions, ", "), authDB.Name, strings.Join(seed.Permissions, ", "))
		}
	}
	if !cfg.Approval() {
		return
	}
	for _, role := range cfg.DefaultRoles {
		seed := seeded[role]
		if seed == nil {
			continue
		}
		var uncovered []string
		for _, permission := range seed.Permissions {
			if !coveredBy(permission, admin) {
				uncovered = append(uncovered, permission)
			}
		}
		if len(uncovered) > 0 {
			r.warnf("", "%s: the first administrator's role %s does not cover %s, which the default role %s carries: approveUser needs the approver's permissions to cover the user's, so that administrator can approve no one", sessions.Name, first.Role, strings.Join(uncovered, ", "), role)
		}
	}
}

// apiPermissions are the permissions schema's operations require, and
// the user model's administration routes' under prefix, sorted and each
// once.
func apiPermissions(schema *ir.Schema, prefix string) []string {
	set := map[string]bool{}
	for _, p := range []string{ir.IdentityPermissionUsersRead, ir.IdentityPermissionUsersWrite, ir.IdentityPermissionRolesRead, ir.IdentityPermissionRolesWrite} {
		set[ir.IdentityPermission(prefix, p)] = true
	}
	for _, s := range schema.OperationSets {
		if s == nil {
			continue
		}
		for _, op := range s.Operations {
			if op == nil {
				continue
			}
			for _, p := range op.Permissions {
				set[p] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// covers reports whether a granted permission satisfies a required one,
// as the identity runtimes read them: they are equal, or the required one
// is nested under the granted one ("a" covers "a.b", not "ab").
func covers(granted, required string) bool {
	return granted == required || strings.HasPrefix(required, granted+".")
}

// coversAny reports whether granted covers one of required.
func coversAny(granted string, required []string) bool {
	for _, req := range required {
		if covers(granted, req) {
			return true
		}
	}
	return false
}

// coveredBy reports whether one of held covers permission.
func coveredBy(permission string, held []string) bool {
	for _, h := range held {
		if covers(h, permission) {
			return true
		}
	}
	return false
}
