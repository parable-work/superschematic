package verify

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader/identity"
	ir "github.com/parable-work/superschematic/ir"
)

// IdentityRoutesDecorator names the decorator of a user model route set
// (D50), "@userSessions" or "@userAdministration", or "" for another set.
// A set with both is named by the first.
func IdentityRoutesDecorator(set *ir.OperationSet) string {
	switch {
	case set == nil:
		return ""
	case set.UserSessions != nil:
		return "@userSessions"
	case set.UserAdministration != nil:
		return "@userAdministration"
	}
	return ""
}

// IdentityRoutesProblem is a refusal of a route set's own form that the
// TypeScript reader reports at its node and the pass for every form.
type IdentityRoutesProblem int

const (
	// IdentityRoutesMember is a method, or any member, of the class: the
	// loader adds the set's operations.
	IdentityRoutesMember IdentityRoutesProblem = iota
	// IdentityRoutesAuthenticated is an Authenticated base, which the IR
	// keeps only on the operations, so only the TypeScript reader sees it.
	IdentityRoutesAuthenticated
	// IdentityRoutesEncrypted is an Encrypted base.
	IdentityRoutesEncrypted
	// IdentityRoutesServiceClause is @requireService or @allowService on
	// the class.
	IdentityRoutesServiceClause
)

// IdentityRoutesMessage words a refusal of set's own form.
func IdentityRoutesMessage(set *ir.OperationSet, problem IdentityRoutesProblem) string {
	decorator := IdentityRoutesDecorator(set)
	switch problem {
	case IdentityRoutesMember:
		return fmt.Sprintf("%s: a %s class has no members: the loader adds its operations, and the identity runtime serves them", set.Name, decorator)
	case IdentityRoutesAuthenticated:
		return fmt.Sprintf("%s: a %s class does not extend Authenticated: each of its routes takes the user model's rule", set.Name, decorator)
	case IdentityRoutesEncrypted:
		return fmt.Sprintf("%s: a %s class is not Encrypted: the identity runtime reads its routes' bodies as plain JSON", set.Name, decorator)
	case IdentityRoutesServiceClause:
		return fmt.Sprintf("%s: a %s class takes no service clause: each of its routes takes the user model's rule", set.Name, decorator)
	}
	return ""
}

// IdentityRoutesSecond words the refusal of set, a second route set of
// other's decorator in one API.
func IdentityRoutesSecond(set, other *ir.OperationSet, decorator string) string {
	return fmt.Sprintf("%s: an API takes one %s class, and %s is one", set.Name, decorator, other.Name)
}

// identityRoutesPath is the form of a route set's path: route segments
// joined by /, without a leading or trailing / or a {parameter}.
var identityRoutesPath = regexp.MustCompile(`^[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*$`)

// checkIdentityRoutes validates the user model's route sets (D50),
// @userSessions and @userAdministration, before the loader fills them
// (identity.ExpandRoutes): each set's own form, one of each per API, the
// type names the expansion takes, and the authDb the routes read their
// users from (in.AuthDB). Everything the expansion relies on is checked
// here, so the expansion itself cannot fail.
func checkIdentityRoutes(schema *ir.Schema, in Input, r *Result) {
	var sessions, admin *ir.OperationSet
	declared := false
	for _, set := range schema.OperationSets {
		if !set.IsIdentityRoutes() {
			continue
		}
		declared = true
		decorator := IdentityRoutesDecorator(set)
		if schema.Kind != ir.SchemaKindAPI {
			r.errorf("", "%s: %s is only allowed in an API schema (this schema has kind %s)", set.Name, decorator, schema.Kind)
		}
		if set.UserSessions != nil && set.UserAdministration != nil {
			r.errorf("", "%s: a class takes @userSessions or @userAdministration, not both", set.Name)
		}
		if cfg := set.UserSessions; cfg != nil {
			if cfg.NoLogin && cfg.Register {
				r.errorf("", "%s: @userSessions: register needs the login; drop register: true or login: false", set.Name)
			}
			checkIdentityRoutesPath(set, "@userSessions", cfg.Path, r)
			if sessions != nil {
				r.errorf("", "%s", IdentityRoutesSecond(set, sessions, "@userSessions"))
			} else {
				sessions = set
			}
		}
		if cfg := set.UserAdministration; cfg != nil {
			checkIdentityRoutesPath(set, "@userAdministration", cfg.Path, r)
			if admin != nil {
				r.errorf("", "%s", IdentityRoutesSecond(set, admin, "@userAdministration"))
			} else {
				admin = set
			}
		}
		if set.Encrypted {
			r.errorf("", "%s", IdentityRoutesMessage(set, IdentityRoutesEncrypted))
		}
		if set.ServiceCallers != nil {
			r.errorf("", "%s", IdentityRoutesMessage(set, IdentityRoutesServiceClause))
		}
		for _, op := range set.Operations {
			// An operation the expansion added is the set's own; any other
			// was authored.
			if op.IdentityOperation == "" {
				r.errorf("", "%s", IdentityRoutesMessage(set, IdentityRoutesMember))
				break
			}
		}
	}
	if !declared {
		return
	}
	checkIdentityRouteNames(schema, r)
	if schema.Kind == ir.SchemaKindAPI {
		checkIdentityRoutesAuthDB(schema, in.AuthDB, sessions, admin, r)
	}
}

func checkIdentityRoutesPath(set *ir.OperationSet, decorator, path string, r *Result) {
	if path != "" && !identityRoutesPath.MatchString(path) {
		r.errorf("", "%s: %s path %q must be route segments joined by /, with no leading or trailing / and no {parameter}", set.Name, decorator, path)
	}
}

// checkIdentityRouteNames refuses an authored type, enum or union named as
// one of the types the route expansion adds: an API with a route set
// reserves every one, whichever its sets use.
func checkIdentityRouteNames(schema *ir.Schema, r *Result) {
	for _, name := range identity.RouteTypes {
		owner := ""
		switch {
		case schema.Types[name] != nil && schema.Types[name].Origin == "":
			owner = schema.Types[name].Owner
		case schema.Enums[name] != nil && schema.Enums[name].Origin == "":
			owner = schema.Enums[name].Owner
		case schema.Unions[name] != nil:
		default:
			continue
		}
		r.errorf(owner, "%s: the user model's routes add a type named %s, which the schema already defines; rename it", name, name)
	}
}

// checkIdentityRoutesAuthDB checks the authDb the route sets read their
// users from: the config names one, it is a DB schema with a User table,
// and, for @userAdministration, a UserRole table. The routes that create
// users, register and the administration routes, need the User table to
// take a row from its key, login and name alone.
func checkIdentityRoutesAuthDB(schema, authDB *ir.Schema, sessions, admin *ir.OperationSet, r *Result) {
	first := sessions
	if first == nil {
		first = admin
	}
	decorator := IdentityRoutesDecorator(first)
	switch {
	case schema.AuthDB == "":
		r.errorf("", "%s: %s needs the API's config to name its authDb, the DB schema whose User table holds its users", first.Name, decorator)
		return
	case authDB == nil:
		r.errorf("", "%s: %s reads the User table of the authDb %s, which this load did not read", first.Name, decorator, schema.AuthDB)
		return
	case authDB.Kind != ir.SchemaKindDB:
		r.errorf("", "%s: %s needs the authDb %s to be a DB schema, and it has kind %s", first.Name, decorator, schema.AuthDB, authDB.Kind)
		return
	}
	user := authDB.UserTable()
	if user == nil {
		r.errorf("", "%s: %s needs a User table in the authDb %s, a table that implements User from @superschematic/db, and it has none", first.Name, decorator, schema.AuthDB)
		return
	}
	if admin != nil && authDB.UserRoleTable() == nil {
		r.errorf("", "%s: @userAdministration needs a UserRole table in the authDb %s, a table that implements UserRole from @superschematic/db, and it has none", admin.Name, schema.AuthDB)
	}
	var creators []string
	if sessions != nil && sessions.UserSessions.Register && !sessions.UserSessions.NoLogin {
		creators = append(creators, sessions.Name+"'s @userSessions({ register: true })")
	}
	if admin != nil {
		creators = append(creators, admin.Name+"'s @userAdministration")
	}
	if len(creators) > 0 {
		checkUserCreatable(authDB, user, strings.Join(creators, " and "), schema.AuthDB, r)
	}
}

// checkUserCreatable refuses a User table that a row cannot be written to
// from its key, login and name alone, the fields the routes that create
// users write (D50, v1): every other field must be optional, take a
// default, be generated, or be a timestamp sqlgen defaults to now.
func checkUserCreatable(db *ir.Schema, user *ir.TypeDef, creators, authDB string, r *Result) {
	written := map[string]bool{user.User.Login: true, user.User.NameField(): true}
	scalarTypes := sqlutil.ScalarSQLTypes(db)
	for _, fd := range user.Fields {
		if fd.Key || written[fd.Name] || !needsValueOnCreate(fd, scalarTypes) {
			continue
		}
		r.errorf("", "%s create users with their key, login and name alone, and %s.%s in the authDb %s is required and has no default; make it optional or give it a @default", creators, user.Name, fd.Name, authDB)
	}
}

// needsValueOnCreate reports whether an insert must write fd: a required
// column with no default of its own, no generated value and none sqlgen
// gives it.
func needsValueOnCreate(fd *ir.FieldDef, scalarTypes map[string]string) bool {
	switch {
	case !fd.Required, fd.AutoGenerated, fd.Default != nil, fd.PlatformDefault != "":
		return false
	case fd.HasMany, fd.ManyToMany:
		// The rows that hold the relation are another table's.
		return false
	}
	return !sqlDefaultsToNow(fd, scalarTypes)
}

// sqlDefaultsToNow reports whether sqlgen gives fd's required column the
// CURRENT_TIMESTAMP, CURRENT_DATE or CURRENT_TIME default, as it does an
// audit timestamp: a single date, time or timestamp column.
func sqlDefaultsToNow(fd *ir.FieldDef, scalarTypes map[string]string) bool {
	if fd.JsonField || fd.TypeRef.IsArray || fd.TypeRef.IsMap || fd.Relation != nil {
		return false
	}
	columnType := sqlutil.ColumnType(fd, scalarTypes)
	traits := codegen.GuessScalarTraits(fd.TypeRef.Name)
	return columnType == "TIMESTAMPTZ" || traits.IsDateTimeLike ||
		columnType == "DATE" || traits.IsDateLike ||
		columnType == "TIME" || traits.IsTimeLike
}
