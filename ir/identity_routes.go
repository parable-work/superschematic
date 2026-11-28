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
// hidden with IdentityPasswordToolReason, and createApiKey answers a
// credential, so it is hidden with IdentityAPIKeyToolReason (D114); the
// rest are tools as any operation is.

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
	// IdentityPendingArgument is listUsers' optional boolean query
	// parameter (D81): true lists the users waiting for approval alone,
	// false the others.
	IdentityPendingArgument = "pending"
	// IdentityKeyIDArgument is the {keyId} of an administrator's route to
	// one of a user's API keys, users/{id}/keys/{keyId} (D114).
	IdentityKeyIDArgument = "keyId"
	// IdentityGrantIDArgument is the {grantId} of an administrator's route
	// to one of a user's OAuth grants, users/{id}/oauth/grants/{grantId}
	// (#695).
	IdentityGrantIDArgument = "grantId"
)

// The requests per minute the rate-limited operations allow, per client.
const (
	IdentityLoginRateLimit          = 10
	IdentityRegisterRateLimit       = 5
	IdentityChangePasswordRateLimit = 10
	IdentityCreateAPIKeyRateLimit   = 10
)

// IdentityPasswordToolReason is the @mcp hidden reason of the operations
// that carry a password.
const IdentityPasswordToolReason = "It carries a password, which an agent does not hold."

// IdentityAPIKeyToolReason is the @mcp hidden reason of createApiKey
// (D114), whose answer is a credential.
const IdentityAPIKeyToolReason = "It answers a credential, which an agent's context should not hold."

// IdentityOAuthToolReason is the @mcp hidden reason of the OAuth routes
// (#695): a person consents in their own browser, and a grant is theirs to
// see and revoke, not an agent's that holds one.
const IdentityOAuthToolReason = "It answers a person's consent to an OAuth client, which an agent must not give or see for them."

// The key of the UserApiKey table, which the loader adds: an
// AutoGenerate<Identity.UUID>, whatever the User table's key is.
const IdentityAPIKeyKeyScalar = "Identity.UUID"

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

	// Registration is how register admits a user (D81):
	// IdentityRegistrationOpen, the default when empty, signs them in;
	// IdentityRegistrationApproval creates them pending and starts no
	// session until an administrator approves them (approveUser). It
	// needs Register.
	Registration string `json:"registration,omitempty" yaml:"registration,omitempty"`

	// DefaultRoles names the roles register grants each new user (D81),
	// each created with no permissions when no role has the name. Under
	// approval they are granted with the pending user, who can neither sign
	// in nor hold a session, so they take effect at approval. It needs
	// Register and a UserRole table in the authDb.
	DefaultRoles []string `json:"defaultRoles,omitempty" yaml:"defaultRoles,omitempty"`

	// FirstUser makes the registration that finds no role grant, the one
	// identity bootstrap would still run for, the database's first
	// administrator (D81): the user is active even under approval, and holds
	// the role, created with its permissions when no role has the name,
	// beside DefaultRoles. Two such registrations at once make one
	// administrator. It needs Register and a UserRole table in the authDb.
	FirstUser *IdentityFirstUser `json:"firstUser,omitempty" yaml:"firstUser,omitempty"`

	// APIKeys is @userSessions({ apiKeys: true }) (D114): the set also has
	// createApiKey, listApiKeys and revokeApiKey, with which a user signed
	// in with a session makes, lists and revokes their API keys.
	APIKeys bool `json:"apiKeys,omitempty" yaml:"apiKeys,omitempty"`

	// OAuth is @userSessions({ oauth }) (#695): the API is its own OAuth
	// 2.1 authorization server, so a client such as Claude's connectors
	// signs a person in with the API's logins and holds a grant that
	// narrows their permissions as an API key does. oauth: true is the
	// empty config, every member at its default. It needs the login and an
	// API that serves MCP.
	OAuth *IdentityOAuthConfig `json:"oauth,omitempty" yaml:"oauth,omitempty"`
}

// IdentityOAuthConfig is @userSessions' oauth (#695). A member left out
// takes its default.
type IdentityOAuthConfig struct {
	// AccessTokenTTLSeconds is how long an access token lasts.
	// 0 means DefaultOAuthAccessTokenTTLSeconds (1 h).
	AccessTokenTTLSeconds int64 `json:"accessTokenTtlSeconds,omitempty" yaml:"accessTokenTtlSeconds,omitempty"`
	// RefreshTokenTTLSeconds is how long a refresh token lasts, renewed on
	// each rotation. 0 means DefaultOAuthRefreshTokenTTLSeconds (30 d).
	RefreshTokenTTLSeconds int64 `json:"refreshTokenTtlSeconds,omitempty" yaml:"refreshTokenTtlSeconds,omitempty"`
	// GrantMaxTTLSeconds is the longest a grant lasts, however often its
	// refresh token rotates. 0 means DefaultOAuthGrantMaxTTLSeconds (365 d).
	GrantMaxTTLSeconds int64 `json:"grantMaxTtlSeconds,omitempty" yaml:"grantMaxTtlSeconds,omitempty"`
	// Clients says which kinds of client the server admits. Nil admits
	// both.
	Clients *IdentityOAuthClients `json:"clients,omitempty" yaml:"clients,omitempty"`
	// ConsentPath, when set, is the path or URL of the project's own
	// consent page, which the authorize endpoint sends the browser to with
	// ?request=<id> in place of the runtime's default page. It drives the
	// consent routes as the default page does.
	ConsentPath string `json:"consentPath,omitempty" yaml:"consentPath,omitempty"`
}

// IdentityOAuthClients is oauth.clients (#695): whether a client may name
// itself by a Client ID Metadata Document's URL, and whether one may
// register through Dynamic Client Registration (RFC 7591). Nil means true.
type IdentityOAuthClients struct {
	MetadataDocuments *bool `json:"metadataDocuments,omitempty" yaml:"metadataDocuments,omitempty"`
	Registration      *bool `json:"registration,omitempty" yaml:"registration,omitempty"`
}

// The defaults of IdentityOAuthConfig's lifetimes (#695).
const (
	DefaultOAuthAccessTokenTTLSeconds  int64 = 60 * 60
	DefaultOAuthRefreshTokenTTLSeconds int64 = 30 * 24 * 60 * 60
	DefaultOAuthGrantMaxTTLSeconds     int64 = 365 * 24 * 60 * 60
)

// AccessTokenTTL returns AccessTokenTTLSeconds or its default.
func (c *IdentityOAuthConfig) AccessTokenTTL() int64 {
	if c == nil || c.AccessTokenTTLSeconds == 0 {
		return DefaultOAuthAccessTokenTTLSeconds
	}
	return c.AccessTokenTTLSeconds
}

// RefreshTokenTTL returns RefreshTokenTTLSeconds or its default.
func (c *IdentityOAuthConfig) RefreshTokenTTL() int64 {
	if c == nil || c.RefreshTokenTTLSeconds == 0 {
		return DefaultOAuthRefreshTokenTTLSeconds
	}
	return c.RefreshTokenTTLSeconds
}

// GrantMaxTTL returns GrantMaxTTLSeconds or its default.
func (c *IdentityOAuthConfig) GrantMaxTTL() int64 {
	if c == nil || c.GrantMaxTTLSeconds == 0 {
		return DefaultOAuthGrantMaxTTLSeconds
	}
	return c.GrantMaxTTLSeconds
}

// MetadataDocuments reports whether a client may name itself by a Client
// ID Metadata Document.
func (c *IdentityOAuthConfig) MetadataDocuments() bool {
	return c != nil && (c.Clients == nil || c.Clients.MetadataDocuments == nil || *c.Clients.MetadataDocuments)
}

// Registration reports whether a client may register through Dynamic
// Client Registration.
func (c *IdentityOAuthConfig) Registration() bool {
	return c != nil && (c.Clients == nil || c.Clients.Registration == nil || *c.Clients.Registration)
}

// IdentityFirstUser is @userSessions' firstUser: the role the first
// registered user holds, and the permissions it carries when register
// creates it (D81).
type IdentityFirstUser struct {
	Role        string   `json:"role" yaml:"role"`
	Permissions []string `json:"permissions" yaml:"permissions"`
}

// The values of UserSessionsConfig.Registration.
const (
	IdentityRegistrationOpen     = "open"
	IdentityRegistrationApproval = "approval"
)

// Approval reports whether register creates users pending an
// administrator's approval (D81).
func (c *UserSessionsConfig) Approval() bool {
	return c != nil && c.Registration == IdentityRegistrationApproval
}

// UserAdministrationConfig is @userAdministration on an operation set: the
// routes that manage users, roles and grants.
type UserAdministrationConfig struct {
	// Path prefixes every route of the set. Empty means
	// DefaultUserAdministrationPath.
	Path string `json:"path,omitempty" yaml:"path,omitempty"`

	// APIKeys is @userAdministration({ apiKeys: true }) (D114): the set
	// also has listUserApiKeys and revokeUserApiKey, with which an
	// administrator lists and revokes any user's API keys.
	APIKeys bool `json:"apiKeys,omitempty" yaml:"apiKeys,omitempty"`

	// OAuth is @userAdministration({ oauth: true }) (#695): the set also
	// has listUserOAuthGrants and revokeUserOAuthGrant, with which an
	// administrator lists and revokes any user's OAuth grants.
	OAuth bool `json:"oauth,omitempty" yaml:"oauth,omitempty"`
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
	// LoginResult: @publicRoute and rate-limited, only with Register. Under
	// registration "approval" it answers RegisterResult (D81): 200 with a
	// session for an active user (the first administrator), 202 with none
	// for a user who waits for approval.
	IdentityOpRegister = "register"
	// IdentityOpCreateAPIKey is POST <path>/keys, CreateApiKeyInput to
	// CreatedApiKey: @auth and rate-limited, only with APIKeys (D114). It
	// needs a caller signed in with a session, and answers the key's token
	// this once.
	IdentityOpCreateAPIKey = "createApiKey"
	// IdentityOpListAPIKeys is GET <path>/keys, to ApiKey[]: @auth, only
	// with APIKeys; the caller's keys that are not revoked, newest first,
	// never a token.
	IdentityOpListAPIKeys = "listApiKeys"
	// IdentityOpRevokeAPIKey is DELETE <path>/keys/{id}, to true: @auth,
	// only with APIKeys; revokes a key of the caller's.
	IdentityOpRevokeAPIKey = "revokeApiKey"
	// IdentityOpGetOAuthRequest is GET <path>/oauth/requests/{id}, to
	// OAuthRequest: @auth, only with OAuth (#695). The consent page reads
	// what a client asks for with it.
	IdentityOpGetOAuthRequest = "getOAuthRequest"
	// IdentityOpApproveOAuthRequest is POST
	// <path>/oauth/requests/{id}/approve, ApproveOAuthRequestInput to
	// OAuthDecision: @auth, only with OAuth. It needs a caller signed in
	// with a session, makes the grant and its code, and answers where the
	// browser goes next.
	IdentityOpApproveOAuthRequest = "approveOAuthRequest"
	// IdentityOpDenyOAuthRequest is POST <path>/oauth/requests/{id}/deny,
	// DenyOAuthRequestInput to OAuthDecision: @auth, only with OAuth.
	IdentityOpDenyOAuthRequest = "denyOAuthRequest"
	// IdentityOpListOAuthGrants is GET <path>/oauth/grants, to
	// ConnectedApp[]: @auth, only with OAuth; the caller's grants that are
	// neither revoked nor past their end, newest first.
	IdentityOpListOAuthGrants = "listOAuthGrants"
	// IdentityOpRevokeOAuthGrant is DELETE <path>/oauth/grants/{id}, to
	// true: @auth, only with OAuth; revokes a grant of the caller's and
	// every token it issued.
	IdentityOpRevokeOAuthGrant = "revokeOAuthGrant"
)

// The protocol endpoints of @userSessions({ oauth }) (#695). They are no
// operations: they take form-encoded requests, answer redirects and RFC
// 6749's errors, and no SDK calls them. The identity runtime serves them at
// the API's root, beside the set's routes, and the discovery documents at
// the root of the origin the API is reached through.
const (
	// IdentityOAuthAuthorizePath is GET and POST: sign-in if the person
	// has no session, then consent, then a redirect with code, state and
	// iss.
	IdentityOAuthAuthorizePath = "oauth/authorize"
	// IdentityOAuthTokenPath is POST: authorization_code with PKCE, or
	// refresh_token.
	IdentityOAuthTokenPath = "oauth/token"
	// IdentityOAuthRegisterPath is POST: Dynamic Client Registration (RFC
	// 7591), public clients alone.
	IdentityOAuthRegisterPath = "oauth/register"
	// IdentityOAuthRevokePath is POST: token revocation (RFC 7009).
	IdentityOAuthRevokePath = "oauth/revoke"
	// IdentityOAuthConsentPath is GET: the runtime's default consent page,
	// unless the config names a ConsentPath.
	IdentityOAuthConsentPath = "oauth/consent"
	// IdentityOAuthProtectedResourcePath is the protected resource
	// metadata (RFC 9728), at the origin's root and, with the MCP
	// endpoint's path after it, at the path RFC 9728 inserts.
	IdentityOAuthProtectedResourcePath = "/.well-known/oauth-protected-resource"
	// IdentityOAuthAuthorizationServerPath is the authorization server
	// metadata (RFC 8414), with the issuer's path after it.
	IdentityOAuthAuthorizationServerPath = "/.well-known/oauth-authorization-server"
	// IdentityOpenIDConfigurationPath answers what
	// IdentityOAuthAuthorizationServerPath does, for a client that looks
	// for OpenID Connect discovery.
	IdentityOpenIDConfigurationPath = "/.well-known/openid-configuration"
	// IdentityOAuthOfflineAccessScope is the scope, beside the API's
	// permission names, a client asks for a refresh token with.
	IdentityOAuthOfflineAccessScope = "offline_access"
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
	// (users.read); ?pending=true lists the users waiting for approval
	// alone, ?pending=false the others (D81).
	IdentityOpListUsers = "listUsers"
	// IdentityOpGetUser is GET <path>/users/{id}, to IdentityUser
	// (users.read).
	IdentityOpGetUser = "getUser"
	// IdentityOpDisableUser is POST <path>/users/{id}/disable, to
	// IdentityUser (users.write, and the caller's permissions cover every
	// one the user holds: D50, amended); revokes the user's sessions.
	IdentityOpDisableUser = "disableUser"
	// IdentityOpEnableUser is POST <path>/users/{id}/enable, to
	// IdentityUser (users.write, and the caller's permissions cover every
	// one the user holds: D50, amended).
	IdentityOpEnableUser = "enableUser"
	// IdentityOpApproveUser is POST <path>/users/{id}/approve, to
	// IdentityUser (users.write, and the caller's permissions cover every
	// one the user holds, the roles register granted them included): the
	// user, who registered under registration "approval", can sign in
	// (D81).
	IdentityOpApproveUser = "approveUser"
	// IdentityOpSetUserPassword is PUT <path>/users/{id}/password,
	// SetPasswordInput to true (users.write, and the caller's permissions
	// cover every one the user holds: D50, amended); revokes the user's
	// sessions.
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
	// IdentityOpListUserAPIKeys is GET <path>/users/{id}/keys, to ApiKey[]
	// (users.read), only with APIKeys (D114).
	IdentityOpListUserAPIKeys = "listUserApiKeys"
	// IdentityOpRevokeUserAPIKey is DELETE <path>/users/{id}/keys/{keyId},
	// to true (users.write), only with APIKeys (D114).
	IdentityOpRevokeUserAPIKey = "revokeUserApiKey"
	// IdentityOpListUserOAuthGrants is GET <path>/users/{id}/oauth/grants,
	// to ConnectedApp[] (users.read), only with OAuth (#695).
	IdentityOpListUserOAuthGrants = "listUserOAuthGrants"
	// IdentityOpRevokeUserOAuthGrant is DELETE
	// <path>/users/{id}/oauth/grants/{grantId}, to true (users.write), only
	// with OAuth (#695).
	IdentityOpRevokeUserOAuthGrant = "revokeUserOAuthGrant"
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
	// IdentityRegisterResultType is register's result under registration
	// "approval" (D81): pending (bool), user (SessionUser), and expiresAt
	// and token as LoginResult has them, both absent while pending.
	IdentityRegisterResultType = "RegisterResult"
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
	// IdentityUserType: id, login, name, disabled (bool), pending (bool,
	// D81), roles (RoleRef[]).
	IdentityUserType = "IdentityUser"
	// IdentityRoleInputType: name, permissions (string[]).
	IdentityRoleInputType = "RoleInput"
	// IdentityRoleType: id, name, permissions (string[]).
	IdentityRoleType = "IdentityRole"
	// IdentityCreateAPIKeyInputType (D114): name, expiresInSeconds
	// (optional; the config's longest when absent), permissions (string[],
	// optional; every permission its user holds when absent).
	IdentityCreateAPIKeyInputType = "CreateApiKeyInput"
	// IdentityAPIKeyType (D114): id, name, permissions (string[], absent
	// for a key that holds every permission of its user), createdAt,
	// expiresAt, lastUsedAt (absent until its first request).
	IdentityAPIKeyType = "ApiKey"
	// IdentityCreatedAPIKeyType (D114): an ApiKey's id, name, permissions,
	// createdAt and expiresAt, and token, a Secret string answered once.
	IdentityCreatedAPIKeyType = "CreatedApiKey"
)

// The types the route expansion adds for a set with OAuth (#695).
const (
	// IdentityOAuthRequestType: id, client (OAuthClientInfo), permissions
	// (string[], the ones the client asks for), held (string[], those the
	// caller holds, which consent can give), offlineAccess (bool),
	// resource, expiresAt, nonce (the value approve and deny send back).
	IdentityOAuthRequestType = "OAuthRequest"
	// IdentityOAuthClientInfoType: id (the client_id), name, uri
	// (optional), redirectHost and loopbackOnly (bool: the redirect is to
	// the person's own machine), of the request's redirect on the consent
	// page and of every redirect of the client's among connected apps, and
	// kind ("metadataDocument", whose id's host vouches for it, or
	// "registered").
	IdentityOAuthClientInfoType = "OAuthClientInfo"
	// IdentityApproveOAuthRequestInputType: nonce, permissions (string[],
	// optional; the ones the request asks for and the caller holds when
	// absent).
	IdentityApproveOAuthRequestInputType = "ApproveOAuthRequestInput"
	// IdentityDenyOAuthRequestInputType: nonce.
	IdentityDenyOAuthRequestInputType = "DenyOAuthRequestInput"
	// IdentityOAuthDecisionType: redirectTo, the URL the browser goes to
	// next: the client's redirect with code, state and iss, or with error.
	IdentityOAuthDecisionType = "OAuthDecision"
	// IdentityConnectedAppType: an OAuth grant as its user and an
	// administrator see it: id, client (OAuthClientInfo), permissions
	// (string[], absent for a grant that holds every permission of its
	// user), createdAt, expiresAt, lastUsedAt (absent until its first
	// request). It is named apart from the authDb's OAuthGrant table.
	IdentityConnectedAppType = "ConnectedApp"
)

// IdentityOAuthTypes are the types the route expansion adds for a set
// with OAuth (#695), which an API reserves only then.
var IdentityOAuthTypes = []string{
	IdentityOAuthRequestType, IdentityOAuthClientInfoType, IdentityApproveOAuthRequestInputType,
	IdentityDenyOAuthRequestInputType, IdentityOAuthDecisionType, IdentityConnectedAppType,
}

// IdentityAPIKeyTypes are the types the route expansion adds for a set
// with APIKeys (D114), which an API reserves only then.
var IdentityAPIKeyTypes = []string{IdentityCreateAPIKeyInputType, IdentityAPIKeyType, IdentityCreatedAPIKeyType}

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
	// usable session or API key.
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
	// IdentityCodePendingApproval (403): a login whose password verifies
	// for a user who waits for an administrator's approval (D81). Every
	// other failed login stays invalid_credentials.
	IdentityCodePendingApproval = "pending_approval"
	// IdentityCodeNotPending (409): approveUser for a user who is not
	// waiting for approval (D81).
	IdentityCodeNotPending = "not_pending"
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
	unauthorized := IdentityRouteError{401, IdentityCodeUnauthorized, "The request carries no usable session or API key."}
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
	notPending := IdentityRouteError{409, IdentityCodeNotPending, "The user is not waiting for approval."}
	byKey := IdentityRouteError{403, IdentityCodeForbidden, "The caller is authenticated by an API key, which does not act on its own account."}
	noKey := IdentityRouteError{404, IdentityCodeNotFound, "No API key of the caller's has the id, or it is revoked."}
	noUserKey := IdentityRouteError{404, IdentityCodeNotFound, "No user has the id, or no API key of theirs the keyId, or it is revoked."}
	byToken := IdentityRouteError{403, IdentityCodeForbidden, "The caller is authenticated by an API key or an OAuth token, which does not act on its own account."}
	noRequest := IdentityRouteError{404, IdentityCodeNotFound, "No pending OAuth request has the id, or it has ended."}
	badNonce := IdentityRouteError{409, IdentityCodeConflict, "The nonce is not the request's."}
	noGrant := IdentityRouteError{404, IdentityCodeNotFound, "No OAuth grant of the caller's has the id, or it is revoked."}
	noUserGrant := IdentityRouteError{404, IdentityCodeNotFound, "No user has the id, or no OAuth grant of theirs the grantId, or it is revoked."}
	return map[string][]IdentityRouteError{
		IdentityOpLogin: {
			{401, IdentityCodeInvalidCredentials, "The login or the password does not verify, or the user is disabled."},
			cookieLogin,
			{403, IdentityCodePendingApproval, "The password verifies, and the user waits for an administrator's approval."},
		},
		IdentityOpRegister:     {cookieLogin, loginTaken},
		IdentityOpLogout:       {unauthorized, byKey, crossOrigin},
		IdentityOpMe:           {unauthorized},
		IdentityOpCapabilities: {unauthorized},
		IdentityOpChangePassword: {
			unauthorized,
			{401, IdentityCodeInvalidCredentials, "The current password does not verify."},
			byKey,
			crossOrigin,
		},
		IdentityOpCreateAPIKey: {
			unauthorized,
			{403, IdentityCodeForbidden, "The caller is authenticated by an API key, or names a permission they do not hold."},
			crossOrigin,
			invalidPermission,
		},
		IdentityOpListAPIKeys:          {unauthorized, byKey},
		IdentityOpRevokeAPIKey:         {unauthorized, byKey, crossOrigin, noKey},
		IdentityOpGetOAuthRequest:      {unauthorized, byToken, noRequest},
		IdentityOpApproveOAuthRequest:  {unauthorized, {403, IdentityCodeForbidden, "The caller is authenticated by an API key or an OAuth token, or names a permission the request does not ask for or they do not hold."}, crossOrigin, noRequest, badNonce, invalidPermission},
		IdentityOpDenyOAuthRequest:     {unauthorized, byToken, crossOrigin, noRequest, badNonce},
		IdentityOpListOAuthGrants:      {unauthorized, byToken},
		IdentityOpRevokeOAuthGrant:     {unauthorized, byToken, crossOrigin, noGrant},
		IdentityOpListUserOAuthGrants:  {unauthorized, permission, noUser},
		IdentityOpRevokeUserOAuthGrant: {unauthorized, permission, crossOrigin, noUserGrant},
		IdentityOpListUserAPIKeys:      {unauthorized, permission, noUser},
		IdentityOpRevokeUserAPIKey:     {unauthorized, permission, crossOrigin, noUserKey},
		IdentityOpCreateUser:           {unauthorized, permission, crossOrigin, loginTaken},
		IdentityOpListUsers:            {unauthorized, permission},
		IdentityOpGetUser:              {unauthorized, permission, noUser},
		IdentityOpDisableUser:          {unauthorized, permission, crossOrigin, noUser},
		IdentityOpEnableUser:           {unauthorized, permission, crossOrigin, noUser},
		IdentityOpApproveUser:          {unauthorized, permission, crossOrigin, noUser, notPending},
		IdentityOpSetUserPassword:      {unauthorized, permission, crossOrigin, noUser},
		IdentityOpListRoles:            {unauthorized, permission},
		IdentityOpCreateRole:           {unauthorized, granting, crossOrigin, nameTaken, invalidPermission},
		IdentityOpUpdateRole:           {unauthorized, granting, crossOrigin, noRole, nameTaken, invalidPermission},
		IdentityOpDeleteRole:           {unauthorized, permission, crossOrigin, noRole},
		IdentityOpGrantRole:            {unauthorized, granting, crossOrigin, noUserOrRole},
		IdentityOpRevokeRole:           {unauthorized, permission, crossOrigin, noUserOrRole},
	}
}()
