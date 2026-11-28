package verify

import (
	"fmt"
	"regexp"
	"strings"

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
			if cfg.NoLogin && cfg.APIKeys {
				r.errorf("", "%s: @userSessions: apiKeys needs the login, since a key is made by a caller signed in with a session; drop apiKeys: true or login: false", set.Name)
			}
			checkIdentityRoutesPath(set, "@userSessions", cfg.Path, r)
			checkIdentityRegistration(set, cfg, r)
			checkIdentityOAuth(set, cfg, in, r)
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
		checkIdentityRoutesAuthDB(schema, in.AuthDB, sessions, admin, sqlutil.DDLPolicyOf(in.Naming.OrDefault()), r)
		checkSeededRoles(schema, in.AuthDB, sessions, in.Naming.OrDefault().IdentityPermissionPrefix, r)
	}
}

// checkIdentityOAuth checks @userSessions({ oauth }) (#695): it needs the
// login, since a person signs in to consent; its lifetimes are positive, an
// access token's shorter than a refresh token's and a refresh token's no
// longer than the grant's; it admits a kind of client; and the API's server
// is a TypeScript one that serves MCP. The Go and Rust servers do not serve
// the authorization server yet, and an API with no server output of the
// build's is left alone.
func checkIdentityOAuth(set *ir.OperationSet, cfg *ir.UserSessionsConfig, in Input, r *Result) {
	oauth := cfg.OAuth
	if oauth == nil {
		return
	}
	if cfg.NoLogin {
		r.errorf("", "%s: @userSessions: oauth needs the login, since a person signs in to consent; drop oauth or login: false", set.Name)
	}
	for _, ttl := range []struct {
		key   string
		value int64
	}{
		{"accessTokenTtlSeconds", oauth.AccessTokenTTLSeconds},
		{"refreshTokenTtlSeconds", oauth.RefreshTokenTTLSeconds},
		{"grantMaxTtlSeconds", oauth.GrantMaxTTLSeconds},
	} {
		if ttl.value < 0 {
			r.errorf("", "%s: @userSessions oauth.%s must be a positive whole number of seconds", set.Name, ttl.key)
		}
	}
	switch {
	case oauth.AccessTokenTTL() >= oauth.RefreshTokenTTL():
		r.errorf("", "%s: @userSessions oauth: an access token's lifetime (%d s) must be shorter than a refresh token's (%d s)", set.Name, oauth.AccessTokenTTL(), oauth.RefreshTokenTTL())
	case oauth.RefreshTokenTTL() > oauth.GrantMaxTTL():
		r.errorf("", "%s: @userSessions oauth: a refresh token's lifetime (%d s) must be no longer than a grant's (%d s)", set.Name, oauth.RefreshTokenTTL(), oauth.GrantMaxTTL())
	}
	if !oauth.MetadataDocuments() && !oauth.Registration() {
		r.errorf("", "%s: @userSessions oauth.clients turns off both kinds of client, so no client could connect; keep metadataDocuments or registration", set.Name)
	}
	switch {
	case in.GoServer:
		r.errorf("", "%s: @userSessions oauth: the Go server does not serve an OAuth authorization server yet (#695); serve the API in TypeScript, or drop oauth", set.Name)
	case in.RustOutput != "":
		r.errorf("", "%s: @userSessions oauth: the Rust server does not serve an OAuth authorization server yet (#695); serve the API in TypeScript, or drop oauth", set.Name)
	case in.TypeScriptServer && !in.MCPServer:
		r.errorf("", "%s: @userSessions oauth signs clients in to the API's MCP endpoint, which this API does not serve; set outputs.api.mcp: true, or drop oauth", set.Name)
	}
}

func checkIdentityRoutesPath(set *ir.OperationSet, decorator, path string, r *Result) {
	if path != "" && !identityRoutesPath.MatchString(path) {
		r.errorf("", "%s: %s path %q must be route segments joined by /, with no leading or trailing / and no {parameter}", set.Name, decorator, path)
	}
}

// identityPermission is a permission's form: dotted segments of ASCII
// letters, digits, _ and -, as the identity runtimes check a role's.
var identityPermission = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)

// checkIdentityRegistration checks how register admits users (D81):
// registration is open or approval, and it, defaultRoles and firstUser
// need register; the role names are non-empty and named once, and
// firstUser names its role and at least one permission of a permission's
// form. The authDb's UserRole table is checkIdentityRoutesAuthDB's.
func checkIdentityRegistration(set *ir.OperationSet, cfg *ir.UserSessionsConfig, r *Result) {
	switch cfg.Registration {
	case "", ir.IdentityRegistrationOpen, ir.IdentityRegistrationApproval:
	default:
		r.errorf("", "%s: @userSessions registration %q must be %q or %q", set.Name, cfg.Registration, ir.IdentityRegistrationOpen, ir.IdentityRegistrationApproval)
	}
	var keys []string
	if cfg.Registration != "" {
		keys = append(keys, "registration")
	}
	if len(cfg.DefaultRoles) > 0 {
		keys = append(keys, "defaultRoles")
	}
	if cfg.FirstUser != nil {
		keys = append(keys, "firstUser")
	}
	if len(keys) > 0 && (!cfg.Register || cfg.NoLogin) {
		verb := "configures"
		if len(keys) > 1 {
			verb = "configure"
		}
		r.errorf("", "%s: @userSessions %s %s register, which needs register: true and the login", set.Name, joinAnd(keys), verb)
	}
	seen := map[string]bool{}
	for _, role := range cfg.DefaultRoles {
		switch {
		case role == "":
			r.errorf("", "%s: @userSessions defaultRoles holds an empty role name", set.Name)
		case seen[role]:
			r.errorf("", "%s: @userSessions defaultRoles names %q twice", set.Name, role)
		}
		seen[role] = true
	}
	if first := cfg.FirstUser; first != nil {
		if first.Role == "" {
			r.errorf("", "%s: @userSessions firstUser needs role, the name of the administrator's role", set.Name)
		}
		if len(first.Permissions) == 0 {
			r.errorf("", "%s: @userSessions firstUser needs permissions, at least one, which the role carries when register creates it", set.Name)
		}
		for _, permission := range first.Permissions {
			if !identityPermission.MatchString(permission) {
				r.errorf("", "%s: @userSessions firstUser permission %q is not dotted segments of letters, digits, _ and -, such as orders.read", set.Name, permission)
			}
		}
	}
}

// checkIdentityRouteNames refuses an authored type, enum or union named as
// one of the types the route expansion adds: an API with a route set
// reserves every one, whichever its sets use, the types of the API key
// routes (D114) when one of its sets has apiKeys, and those of the OAuth
// routes (#695) when one has oauth.
func checkIdentityRouteNames(schema *ir.Schema, r *Result) {
	names := identity.RouteTypes
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		if (set.UserSessions != nil && set.UserSessions.APIKeys) || (set.UserAdministration != nil && set.UserAdministration.APIKeys) {
			names = append(append([]string{}, names...), ir.IdentityAPIKeyTypes...)
			break
		}
	}
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		if (set.UserSessions != nil && set.UserSessions.OAuth != nil) || (set.UserAdministration != nil && set.UserAdministration.OAuth) {
			names = append(append([]string{}, names...), ir.IdentityOAuthTypes...)
			break
		}
	}
	for _, name := range names {
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
func checkIdentityRoutesAuthDB(schema, authDB *ir.Schema, sessions, admin *ir.OperationSet, policy sqlutil.DDLPolicy, r *Result) {
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
	if sessions != nil && (len(sessions.UserSessions.DefaultRoles) > 0 || sessions.UserSessions.FirstUser != nil) && authDB.UserRoleTable() == nil {
		r.errorf("", "%s: @userSessions defaultRoles and firstUser grant roles, which need a UserRole table in the authDb %s, a table that implements UserRole from @superschematic/db, and it has none", sessions.Name, schema.AuthDB)
	}
	var creators []string
	if sessions != nil && sessions.UserSessions.Register && !sessions.UserSessions.NoLogin {
		creators = append(creators, sessions.Name+"'s @userSessions({ register: true })")
	}
	if admin != nil {
		creators = append(creators, admin.Name+"'s @userAdministration")
	}
	if len(creators) > 0 {
		checkUserCreatable(authDB, user, strings.Join(creators, " and "), schema.AuthDB, policy, r)
	}
}

// checkUserCreatable refuses a User table that a row cannot be written to
// from its key, login and name alone, the fields the routes that create
// users write (D50, v1): every other field must be optional, take a
// default its column carries (D83), be generated, or be an audit
// timestamp sqlgen defaults to now (D88), as the naming file's [sql]
// table gives defaults (policy).
func checkUserCreatable(db *ir.Schema, user *ir.TypeDef, creators, authDB string, policy sqlutil.DDLPolicy, r *Result) {
	written := map[string]bool{user.User.Login: true, user.User.NameField(): true}
	scalarTypes := sqlutil.ScalarSQLTypes(db)
	for _, fd := range user.Fields {
		if fd.Key || written[fd.Name] || !needsValueOnCreate(db, user, fd, scalarTypes, policy) {
			continue
		}
		if policy.NoColumnDefaults {
			r.errorf("", "%s create users with their key, login and name alone, and %s.%s in the authDb %s is required and has no default its column carries; make it optional, or give a date or time Default<T, \"now\"> (the naming file's sql.column_defaults is \"off\", so no other default is its column's)", creators, user.Name, fd.Name, authDB)
			continue
		}
		r.errorf("", "%s create users with their key, login and name alone, and %s.%s in the authDb %s is required and has no default its column carries; make it optional or give it a Default<T, V> whose V is text, a number, a boolean or an enum member, or \"now\" for a date or time", creators, user.Name, fd.Name, authDB)
	}
}

// needsValueOnCreate reports whether an insert must write fd, a field of
// the table td: a required column with no generated value and no default
// sqlgen gives it, from its Default<T, V> (DDLPolicy.ColumnDefault) or,
// for an audit timestamp, its type (DDLPolicy.ImpliedNowDefault).
func needsValueOnCreate(db *ir.Schema, td *ir.TypeDef, fd *ir.FieldDef, scalarTypes map[string]string, policy sqlutil.DDLPolicy) bool {
	if _, ok := policy.ColumnDefault(fd, sqlutil.ColumnType(db, fd, scalarTypes)); ok {
		return false
	}
	switch {
	case !fd.Required, fd.AutoGenerated, fd.PlatformDefault != "":
		return false
	case fd.HasMany, fd.ManyToMany:
		// The rows that hold the relation are another table's.
		return false
	}
	return !sqlDefaultsToNow(db, td, fd, scalarTypes, policy)
}

// sqlDefaultsToNow reports whether sqlgen gives fd's required column the
// CURRENT_TIMESTAMP, CURRENT_DATE or CURRENT_TIME default with no
// Default<T, "now">: a single date, time or timestamp column that is an
// audit timestamp (DDLPolicy.ImpliedNowDefault, D88), or any such column
// under now_defaults = "required".
func sqlDefaultsToNow(db *ir.Schema, td *ir.TypeDef, fd *ir.FieldDef, scalarTypes map[string]string, policy sqlutil.DDLPolicy) bool {
	if fd.JsonField || fd.TypeRef.IsArray || fd.TypeRef.IsMap || fd.Relation != nil || !policy.ImpliedNowDefault(td, fd) {
		return false
	}
	return sqlutil.NowDefault(sqlutil.ColumnType(db, fd, scalarTypes), fd.TypeRef.Name) != ""
}
