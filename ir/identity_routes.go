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
	// IdentityOpLogout is POST <path>/logout: @auth; revokes the caller's
	// session and clears the cookie.
	IdentityOpLogout = "logout"
	// IdentityOpMe is GET <path>/me, to CurrentUser: @auth.
	IdentityOpMe = "me"
	// IdentityOpCapabilities is GET <path>/capabilities, to Capabilities:
	// @auth; for each operation of the API an end user may call, whether
	// the route admits the caller.
	IdentityOpCapabilities = "capabilities"
	// IdentityOpChangePassword is POST <path>/password,
	// ChangePasswordInput: @auth and rate-limited; revokes the user's other
	// sessions.
	IdentityOpChangePassword = "changePassword"
	// IdentityOpRegister is POST <path>/register, RegisterInput to
	// LoginResult: @publicRoute and rate-limited, only with Register.
	IdentityOpRegister = "register"
)

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
	// SetPasswordInput (users.write); revokes the user's sessions.
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
	// IdentityOpDeleteRole is DELETE <path>/roles/{id} (roles.write).
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

// The types the loader adds to an API with @userSessions or
// @userAdministration, each with Origin OriginIdentity. A type the author
// names one of these in such an API is refused. The login and key fields
// take the User table's login and key scalars, read from the API's authDb
// schema.
const (
	// IdentitySessionTransportEnum is "bearer" or "cookie".
	IdentitySessionTransportEnum = "SessionTransport"
	// IdentityLoginInputType: login, password (Auth.Password), session
	// (SessionTransport, optional; bearer when absent).
	IdentityLoginInputType = "LoginInput"
	// IdentityRegisterInputType: login, name (optional; the login when
	// absent), password, session (optional).
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
	// IdentityCreateUserInputType: login, name (optional), password.
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
