package ir

// The user model's routes (D50). An API serves them by marking a class with
// no methods @userSessions or @userAdministration, from @superschematic/api:
//
//	@userSessions()
//	export class Account {}
//
//	@userAdministration()
//	export class AccountAdmin {}
//
// The reader records them as OperationSet.UserSessions and
// OperationSet.UserAdministration. The loader then fills the set with the
// operations below and adds the types they take and return, each with
// Origin OriginIdentity, and sets FieldDef.IdentityOperation on each
// operation. The OpenAPI document, the SDKs, the docs and the tool manifest
// read them as any other operation. A server routes an operation whose
// IdentityOperation is set to its runtime's identity package: the
// implementation the project writes has no method for it.
//
// An operation's input type travels as its argument IdentityInputArgument,
// and the {id} and {roleId} of its route are its arguments IdentityIDArgument
// and IdentityRoleIDArgument, typed by the User or UserRole table's key. An
// operation that answers nothing else answers the boolean true, since every
// route answers a value. login, register, changePassword, createUser and
// setUserPassword carry a password an agent does not hold, so each is @mcp
// hidden with IdentityPasswordToolReason; the rest are tools as any
// operation is.

// IsIdentityRoutes reports whether the set is @userSessions or
// @userAdministration, whose operations the loader adds.
func (s *OperationSet) IsIdentityRoutes() bool {
	return s != nil && (s.UserSessions != nil || s.UserAdministration != nil)
}

// The argument names of the expanded operations.
const (
	// IdentityInputArgument is the argument an operation's input type
	// travels as: the request body.
	IdentityInputArgument = "input"
	// IdentityIDArgument is the {id} of a route: a user's under users/, a
	// role's under roles/.
	IdentityIDArgument = "id"
	// IdentityRoleIDArgument is the {roleId} of a grant's route,
	// users/{id}/roles/{roleId}.
	IdentityRoleIDArgument = "roleId"
)

// The requests per minute the rate-limited operations allow, per client.
const (
	IdentityLoginRateLimit          = 10
	IdentityRegisterRateLimit       = 5
	IdentityChangePasswordRateLimit = 10
)

// IdentityPasswordToolReason is the @mcp hidden reason of the operations
// that carry a password.
const IdentityPasswordToolReason = "It carries a password, which an agent does not hold."

// UserSessionsConfig is @userSessions on an operation set: the routes a user
// signs in, out and about themselves with.
type UserSessionsConfig struct {
	// Path prefixes every route of the set. Empty means
	// DefaultUserSessionsPath.
	Path string `json:"path,omitempty" yaml:"path,omitempty"`

	// NoLogin is @userSessions({ login: false }): the set has me and
	// capabilities alone, for an API whose users sign in through another
	// API of the same authDb.
	NoLogin bool `json:"noLogin,omitempty" yaml:"noLogin,omitempty"`

	// Register is @userSessions({ register: true }): the set also has
	// register, which anyone may call. A set with NoLogin takes no Register.
	Register bool `json:"register,omitempty" yaml:"register,omitempty"`
}

// UserAdministrationConfig is @userAdministration on an operation set: the
// routes that manage users, roles and grants.
type UserAdministrationConfig struct {
	// Path prefixes every route of the set. Empty means
	// DefaultUserAdministrationPath.
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
}

// The default route prefixes.
const (
	DefaultUserSessionsPath       = "auth"
	DefaultUserAdministrationPath = "auth/admin"
)

// UserSessionsPath returns the set's route prefix: Path, or
// DefaultUserSessionsPath when it is empty.
func (c *UserSessionsConfig) UserSessionsPath() string {
	if c.Path != "" {
		return c.Path
	}
	return DefaultUserSessionsPath
}

// UserAdministrationPath returns the set's route prefix: Path, or
// DefaultUserAdministrationPath when it is empty.
func (c *UserAdministrationConfig) UserAdministrationPath() string {
	if c.Path != "" {
		return c.Path
	}
	return DefaultUserAdministrationPath
}

// The operations of @userSessions. Each is the operation's name in its set
// and the value of its FieldDef.IdentityOperation. The route follows the
// set's path; {id} and {roleId} are path parameters.
const (
	// IdentityOpLogin is POST <path>/login, LoginInput to LoginResult:
	// @publicRoute and rate-limited. With session "cookie" it sets the
	// session cookie and returns no token.
	IdentityOpLogin = "login"
	// IdentityOpLogout is POST <path>/logout, to true: @auth; revokes the
	// caller's session and clears the cookie.
	IdentityOpLogout = "logout"
	// IdentityOpMe is GET <path>/me, to CurrentUser: @auth.
	IdentityOpMe = "me"
	// IdentityOpCapabilities is GET <path>/capabilities, to Capabilities:
	// @auth; for each operation of the API an end user may call, whether
	// the route admits the caller.
	IdentityOpCapabilities = "capabilities"
	// IdentityOpChangePassword is POST <path>/password,
	// ChangePasswordInput to true: @auth and rate-limited; revokes the
	// user's other sessions.
	IdentityOpChangePassword = "changePassword"
	// IdentityOpRegister is POST <path>/register, RegisterInput to
	// LoginResult: @publicRoute and rate-limited, only with Register.
	IdentityOpRegister = "register"
)

// A set with NoLogin has IdentityOpMe and IdentityOpCapabilities alone;
// IdentityOpLogin, IdentityOpLogout and IdentityOpChangePassword need the
// login.

// The operations of @userAdministration, under the set's path. Each needs
// a permission under the naming key identity_permission_prefix
// ("identity" by default): .users.read or .users.write, .roles.read or
// .roles.write.
const (
	// IdentityOpCreateUser is POST <path>/users, CreateUserInput to
	// IdentityUser (users.write).
	IdentityOpCreateUser = "createUser"
	// IdentityOpListUsers is GET <path>/users, to IdentityUser[]
	// (users.read).
	IdentityOpListUsers = "listUsers"
	// IdentityOpGetUser is GET <path>/users/{id}, to IdentityUser
	// (users.read).
	IdentityOpGetUser = "getUser"
	// IdentityOpDisableUser is POST <path>/users/{id}/disable, to
	// IdentityUser (users.write); revokes the user's sessions.
	IdentityOpDisableUser = "disableUser"
	// IdentityOpEnableUser is POST <path>/users/{id}/enable, to
	// IdentityUser (users.write).
	IdentityOpEnableUser = "enableUser"
	// IdentityOpSetUserPassword is PUT <path>/users/{id}/password,
	// SetPasswordInput to true (users.write); revokes the user's sessions.
	IdentityOpSetUserPassword = "setUserPassword"
	// IdentityOpListRoles is GET <path>/roles, to IdentityRole[]
	// (roles.read).
	IdentityOpListRoles = "listRoles"
	// IdentityOpCreateRole is POST <path>/roles, RoleInput to IdentityRole
	// (roles.write).
	IdentityOpCreateRole = "createRole"
	// IdentityOpUpdateRole is PUT <path>/roles/{id}, RoleInput to
	// IdentityRole (roles.write).
	IdentityOpUpdateRole = "updateRole"
	// IdentityOpDeleteRole is DELETE <path>/roles/{id}, to true
	// (roles.write).
	IdentityOpDeleteRole = "deleteRole"
	// IdentityOpGrantRole is PUT <path>/users/{id}/roles/{roleId}, to
	// IdentityUser (roles.write).
	IdentityOpGrantRole = "grantRole"
	// IdentityOpRevokeRole is DELETE <path>/users/{id}/roles/{roleId}, to
	// IdentityUser (roles.write).
	IdentityOpRevokeRole = "revokeRole"
)

// DefaultIdentityPermissionPrefix is the naming key
// identity_permission_prefix's default: the administration routes need
// identity.users.read, identity.users.write, identity.roles.read and
// identity.roles.write.
const DefaultIdentityPermissionPrefix = "identity"

// The permissions of the administration routes, after the prefix and a dot.
const (
	IdentityPermissionUsersRead  = "users.read"
	IdentityPermissionUsersWrite = "users.write"
	IdentityPermissionRolesRead  = "roles.read"
	IdentityPermissionRolesWrite = "roles.write"
)

// IdentityPermission returns the permission an administration route needs:
// prefix, a dot and permission, one of the IdentityPermission constants.
func IdentityPermission(prefix, permission string) string {
	return prefix + "." + permission
}

// The types the loader adds to an API with @userSessions or
// @userAdministration, each with Origin OriginIdentity: the ones its
// operations take and return. A type the author names one of these in
// such an API is refused, whether or not its sets use it. The login and
// key fields take the User table's login and key scalars, and a name field
// the type of the table's name field (the login's without one), read from
// the API's authDb schema. A role's id and name take the UserRole table's
// key and name types, or string when the authDb has no UserRole table.
const (
	// IdentitySessionTransportEnum is "bearer" or "cookie".
	IdentitySessionTransportEnum = "SessionTransport"
	// IdentityLoginInputType: login, password (Auth.Password), session
	// (SessionTransport, optional; bearer when absent).
	IdentityLoginInputType = "LoginInput"
	// IdentityRegisterInputType: login, name (optional; the login when
	// absent), password, session (optional). name is there only when the
	// User trait names a name field.
	IdentityRegisterInputType = "RegisterInput"
	// IdentityLoginResultType: user (SessionUser), expiresAt
	// (Temporal.DateTime), token (a Secret string; absent for a cookie
	// session).
	IdentityLoginResultType = "LoginResult"
	// IdentitySessionUserType: id, login, name.
	IdentitySessionUserType = "SessionUser"
	// IdentityRoleRefType: id, name.
	IdentityRoleRefType = "RoleRef"
	// IdentityCurrentUserType: user (SessionUser), roles (RoleRef[]),
	// permissions (string[]), the roles' own without repeats.
	IdentityCurrentUserType = "CurrentUser"
	// IdentityCapabilitiesType: operations, a map from each operation's
	// OpenAPI operation id to whether the route admits the caller.
	IdentityCapabilitiesType = "Capabilities"
	// IdentityChangePasswordInputType: current, password (both
	// Auth.Password).
	IdentityChangePasswordInputType = "ChangePasswordInput"
	// IdentityCreateUserInputType: login, name (optional, and only when the
	// User trait names a name field), password.
	IdentityCreateUserInputType = "CreateUserInput"
	// IdentitySetPasswordInputType: password.
	IdentitySetPasswordInputType = "SetPasswordInput"
	// IdentityUserType: id, login, name, disabled (bool), roles (RoleRef[]).
	IdentityUserType = "IdentityUser"
	// IdentityRoleInputType: name, permissions (string[]).
	IdentityRoleInputType = "RoleInput"
	// IdentityRoleType: id, name, permissions (string[]).
	IdentityRoleType = "IdentityRole"
)

// The values of IdentitySessionTransportEnum.
const (
	IdentitySessionBearer = "bearer"
	IdentitySessionCookie = "cookie"
)

// The RFC 9457 codes the identity runtime answers the routes' refusals
// with, beside bad_request, the 400 with field errors any refused input
// draws.
const (
	// IdentityCodeInvalidCredentials (401): a login, or changePassword's
	// current password, that does not verify, for whatever reason.
	IdentityCodeInvalidCredentials = "invalid_credentials"
	// IdentityCodeUnauthorized (401): a route that needs a caller, without a
	// usable session.
	IdentityCodeUnauthorized = "unauthorized"
	// IdentityCodeForbidden (403): the caller lacks the route's permission,
	// or grants a permission they do not hold.
	IdentityCodeForbidden = "forbidden"
	// IdentityCodeCrossOrigin (403): a cookie request other than GET, or a
	// cookie login, that the cross-origin check refuses.
	IdentityCodeCrossOrigin = "cross_origin"
	// IdentityCodeNotFound (404): no user or role has the id.
	IdentityCodeNotFound = "not_found"
	// IdentityCodeConflict (409): the login or the role name is taken.
	IdentityCodeConflict = "conflict"
	// IdentityCodeInvalidPermission (422): a role's permission is not
	// dotted segments of letters, digits, _ and -; details.permissions
	// lists them.
	IdentityCodeInvalidPermission = "invalid_permission"
)

// IdentityRouteError is an error response a route of the user model
// answers: its status, its code and when.
type IdentityRouteError struct {
	Status  int
	Code    string
	Meaning string
}

// IdentityOperationErrors returns the error responses the identity runtime
// answers the operation op with, beside the 400 bad_request any refused
// input draws: in status order, and in the order below within a status.
// An operation that is none of the IdentityOp constants has none.
func IdentityOperationErrors(op string) []IdentityRouteError {
	return append([]IdentityRouteError(nil), identityOperationErrors[op]...)
}

var identityOperationErrors = func() map[string][]IdentityRouteError {
	unauthorized := IdentityRouteError{401, IdentityCodeUnauthorized, "The request carries no usable session."}
	permission := IdentityRouteError{403, IdentityCodeForbidden, "The caller lacks the route's permission."}
	granting := IdentityRouteError{403, IdentityCodeForbidden, "The caller lacks the route's permission, or grants a permission they do not hold; details.permissions lists those."}
	crossOrigin := IdentityRouteError{403, IdentityCodeCrossOrigin, "The cross-origin check refused a cookie request."}
	cookieLogin := IdentityRouteError{403, IdentityCodeCrossOrigin, "The cross-origin check refused a cookie session's login."}
	loginTaken := IdentityRouteError{409, IdentityCodeConflict, "The login is taken."}
	nameTaken := IdentityRouteError{409, IdentityCodeConflict, "The role name is taken."}
	noUser := IdentityRouteError{404, IdentityCodeNotFound, "No user has the id."}
	noRole := IdentityRouteError{404, IdentityCodeNotFound, "No role has the id."}
	noUserOrRole := IdentityRouteError{404, IdentityCodeNotFound, "No user has the id, or no role the roleId."}
	invalidPermission := IdentityRouteError{422, IdentityCodeInvalidPermission, "A permission is not dotted segments of letters, digits, _ and -; details.permissions lists them."}
	return map[string][]IdentityRouteError{
		IdentityOpLogin: {
			{401, IdentityCodeInvalidCredentials, "The login or the password does not verify, or the user is disabled."},
			cookieLogin,
		},
		IdentityOpRegister:     {cookieLogin, loginTaken},
		IdentityOpLogout:       {unauthorized, crossOrigin},
		IdentityOpMe:           {unauthorized},
		IdentityOpCapabilities: {unauthorized},
		IdentityOpChangePassword: {
			unauthorized,
			{401, IdentityCodeInvalidCredentials, "The current password does not verify."},
			crossOrigin,
		},
		IdentityOpCreateUser:      {unauthorized, permission, crossOrigin, loginTaken},
		IdentityOpListUsers:       {unauthorized, permission},
		IdentityOpGetUser:         {unauthorized, permission, noUser},
		IdentityOpDisableUser:     {unauthorized, permission, crossOrigin, noUser},
		IdentityOpEnableUser:      {unauthorized, permission, crossOrigin, noUser},
		IdentityOpSetUserPassword: {unauthorized, permission, crossOrigin, noUser},
		IdentityOpListRoles:       {unauthorized, permission},
		IdentityOpCreateRole:      {unauthorized, granting, crossOrigin, nameTaken, invalidPermission},
		IdentityOpUpdateRole:      {unauthorized, granting, crossOrigin, noRole, nameTaken, invalidPermission},
		IdentityOpDeleteRole:      {unauthorized, permission, crossOrigin, noRole},
		IdentityOpGrantRole:       {unauthorized, granting, crossOrigin, noUserOrRole},
		IdentityOpRevokeRole:      {unauthorized, permission, crossOrigin, noUserOrRole},
	}
}()
