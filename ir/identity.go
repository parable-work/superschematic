package ir

import (
	"maps"
	"slices"
)

// The core user model (D50). A project turns it on by giving one DB table
// the User trait and, for roles, one the UserRole trait, both core traits
// from @superschematic/db:
//
//	export abstract class Account extends Auditable implements User<{ login: "email" }> { ... }
//	export abstract class Role extends Auditable implements UserRole { ... }
//
// The reader records them as TypeDef.User and TypeDef.UserRole, not as
// TraitRefs: the traits are the core's, not types of the schema. The loader
// then adds the tables the model owns (IdentitySessionTable,
// IdentityCredentialTable and, with a UserRole table,
// IdentityRoleGrantTable), each with Origin OriginIdentity.

// OriginIdentity marks a type, field or index the loader added for the user
// model. Such definitions are loader output, never authored: the data forms
// have no key for Origin, and the schema writer skips them and writes the
// traits that produce them instead.
const OriginIdentity = "identity"

// UserTrait is the User trait's configuration on the table whose rows are
// the project's users.
type UserTrait struct {
	// Login names the table's login field: a @unique field typed by a
	// scalar superscalar declares case-insensitive (ScalarDef.CaseInsensitive),
	// such as Contact.Email or Identity.Slug. The scalar's normalize step and
	// its CITEXT column carry the case rule; the model adds none.
	Login string `json:"login" yaml:"login"`

	// Name names the field a principal's display name comes from: a string,
	// or a scalar whose values are strings. Empty means the login.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
}

// NameField returns the field a principal's display name comes from: Name,
// or Login when Name is empty.
func (t *UserTrait) NameField() string {
	if t.Name != "" {
		return t.Name
	}
	return t.Login
}

// UserRoleTrait marks the table whose rows are roles. It has no
// configuration: a role table declares a @unique text field named
// IdentityRoleNameField and a list-of-strings field named
// IdentityRolePermissionsField.
type UserRoleTrait struct{}

// The fields a UserRole table declares.
const (
	IdentityRoleNameField        = "name"
	IdentityRolePermissionsField = "permissions"
)

// The tables the loader adds beside a User table. A table an author names
// one of these, in a schema with a User table, is refused.
const (
	// IdentitySessionTable holds one row per signed-in session.
	IdentitySessionTable = "Session"

	// IdentityCredentialTable holds a user's password hash, apart from the
	// user row so that no @source view or select of every column exposes it.
	IdentityCredentialTable = "UserCredential"

	// IdentityRoleGrantTable links a user to each role they hold. The
	// loader adds it only when the schema has a UserRole table.
	IdentityRoleGrantTable = "UserRoleGrant"

	// IdentityAPIKeyTable holds one row per API key (D114): a named,
	// long-lived token a user makes for a program that calls as them.
	IdentityAPIKeyTable = "UserApiKey"

	// IdentityOAuthClientTable holds one row per OAuth client the API's
	// authorization server knows (#695): one that named itself by a Client
	// ID Metadata Document, cached, or one that registered.
	IdentityOAuthClientTable = "OAuthClient"

	// IdentityOAuthGrantTable holds one row per grant a person gave an
	// OAuth client (#695): the fourth credential beside a session, a
	// password and an API key.
	IdentityOAuthGrantTable = "OAuthGrant"

	// IdentityOAuthCodeTable holds one row per authorization request
	// (#695): pending while the person consents, then the code the client
	// redeems once. A row, not memory, so any replica redeems it.
	IdentityOAuthCodeTable = "OAuthCode"

	// IdentityOAuthTokenTable holds one row per access or refresh token a
	// grant issued (#695).
	IdentityOAuthTokenTable = "OAuthToken"
)

// The fields of the tables the loader adds. Every table's key is "id", an
// AutoGenerate<Identity.UUID>, and every relation to the User and UserRole
// tables cascades on delete.
const (
	// Session: id; user, the User table; tokenHash, a @unique Crypto.SHA256
	// of the session's token; createdAt and expiresAt, Temporal.DateTime;
	// lastSeenAt and revokedAt, nullable Temporal.DateTime; an index on
	// user. Revocation is revokedAt, not a soft delete, so the ORM's
	// soft-delete filter never hides a revoked session (D33).
	IdentitySessionUserField       = "user"
	IdentitySessionTokenHashField  = "tokenHash"
	IdentitySessionCreatedAtField  = "createdAt"
	IdentitySessionExpiresAtField  = "expiresAt"
	IdentitySessionLastSeenAtField = "lastSeenAt"
	IdentitySessionRevokedAtField  = "revokedAt"

	// UserCredential: id; user, the User table, @unique; passwordHash, a
	// Secret string holding the PHC string; passwordChangedAt,
	// Temporal.DateTime; disabledAt and pendingAt, nullable
	// Temporal.DateTime. pendingAt is set while a user who registered under
	// registration "approval" waits for an administrator (D81), apart from
	// disabledAt so the two read apart.
	IdentityCredentialUserField              = "user"
	IdentityCredentialPasswordHashField      = "passwordHash"
	IdentityCredentialPasswordChangedAtField = "passwordChangedAt"
	IdentityCredentialDisabledAtField        = "disabledAt"
	IdentityCredentialPendingAtField         = "pendingAt"

	// UserRoleGrant: id; user, the User table; role, the UserRole table;
	// grantedAt, Temporal.DateTime; a unique index on (user, role).
	IdentityRoleGrantUserField      = "user"
	IdentityRoleGrantRoleField      = "role"
	IdentityRoleGrantGrantedAtField = "grantedAt"

	// UserApiKey (D114): id; user, the User table; name, what its user
	// calls it; tokenHash, a @unique Crypto.SHA256 of the key's token;
	// permissions, a nullable list of strings, the subset of its user's
	// permissions the key was made with, null for all of them; createdAt
	// and expiresAt, Temporal.DateTime; lastUsedAt and revokedAt, nullable
	// Temporal.DateTime; an index on user. Revocation is revokedAt, as a
	// session's is.
	IdentityAPIKeyUserField        = "user"
	IdentityAPIKeyNameField        = "name"
	IdentityAPIKeyTokenHashField   = "tokenHash"
	IdentityAPIKeyPermissionsField = "permissions"
	IdentityAPIKeyCreatedAtField   = "createdAt"
	IdentityAPIKeyExpiresAtField   = "expiresAt"
	IdentityAPIKeyLastUsedAtField  = "lastUsedAt"
	IdentityAPIKeyRevokedAtField   = "revokedAt"

	// IdentityAPIKeyNameMaxLength is the most characters a key's name holds.
	IdentityAPIKeyNameMaxLength = 100

	// OAuthClient (#695): id; clientId, @unique, the client_id it is
	// known by, a metadata document's https URL or the id registration
	// made; kind, IdentityOAuthClientMetadataDocument or
	// IdentityOAuthClientRegistered; name, what the consent page calls it;
	// uri, nullable, its home page; redirectUris, the redirects it may
	// use; createdAt; refreshedAt and freshUntil, nullable, when a
	// metadata document was last fetched and until when its HTTP cache
	// headers keep it; lastUsedAt, nullable, when an authorization last
	// named it, by which an unused registered client is pruned.
	IdentityOAuthClientIDField           = "clientId"
	IdentityOAuthClientKindField         = "kind"
	IdentityOAuthClientNameField         = "name"
	IdentityOAuthClientURIField          = "uri"
	IdentityOAuthClientRedirectURIsField = "redirectUris"
	IdentityOAuthClientCreatedAtField    = "createdAt"
	IdentityOAuthClientRefreshedAtField  = "refreshedAt"
	IdentityOAuthClientFreshUntilField   = "freshUntil"
	IdentityOAuthClientLastUsedAtField   = "lastUsedAt"

	// OAuthGrant (#695): id; user, the User table; client, the OAuthClient
	// table; permissions, a nullable list of strings, narrowed against its
	// user's own on every request as an API key's are, null for all of
	// them; resource, the MCP endpoint's URL it was given for; createdAt
	// and expiresAt, the grant's longest life; lastUsedAt and revokedAt,
	// nullable; an index on user.
	IdentityOAuthGrantUserField        = "user"
	IdentityOAuthGrantClientField      = "client"
	IdentityOAuthGrantPermissionsField = "permissions"
	IdentityOAuthGrantResourceField    = "resource"
	IdentityOAuthGrantCreatedAtField   = "createdAt"
	IdentityOAuthGrantExpiresAtField   = "expiresAt"
	IdentityOAuthGrantLastUsedAtField  = "lastUsedAt"
	IdentityOAuthGrantRevokedAtField   = "revokedAt"

	// OAuthCode (#695): id, the request's id the consent routes take;
	// client, the OAuthClient table; user and grant, nullable, set when
	// the person approves; redirectUri, codeChallenge (S256), state
	// (nullable), scope (nullable, as the client sent it) and resource,
	// as the authorization request named them; nonce, which approve and
	// deny send back; codeHash, a nullable @unique Crypto.SHA256 of the
	// code approval makes; createdAt; expiresAt, the request's end and,
	// once approved, the code's; usedAt, nullable, when the code was
	// redeemed.
	IdentityOAuthCodeClientField      = "client"
	IdentityOAuthCodeUserField        = "user"
	IdentityOAuthCodeGrantField       = "grant"
	IdentityOAuthCodeRedirectURIField = "redirectUri"
	IdentityOAuthCodeChallengeField   = "codeChallenge"
	IdentityOAuthCodeStateField       = "state"
	IdentityOAuthCodeScopeField       = "scope"
	IdentityOAuthCodeResourceField    = "resource"
	IdentityOAuthCodeNonceField       = "nonce"
	IdentityOAuthCodeCodeHashField    = "codeHash"
	IdentityOAuthCodeCreatedAtField   = "createdAt"
	IdentityOAuthCodeExpiresAtField   = "expiresAt"
	IdentityOAuthCodeUsedAtField      = "usedAt"

	// OAuthToken (#695): id; grant, the OAuthGrant table; kind,
	// IdentityOAuthAccessToken or IdentityOAuthRefreshToken; tokenHash, a
	// @unique Crypto.SHA256 of the token's text; createdAt and expiresAt;
	// usedAt, nullable, when a refresh token rotated; revokedAt, nullable;
	// an index on grant.
	IdentityOAuthTokenGrantField     = "grant"
	IdentityOAuthTokenKindField      = "kind"
	IdentityOAuthTokenTokenHashField = "tokenHash"
	IdentityOAuthTokenCreatedAtField = "createdAt"
	IdentityOAuthTokenExpiresAtField = "expiresAt"
	IdentityOAuthTokenUsedAtField    = "usedAt"
	IdentityOAuthTokenRevokedAtField = "revokedAt"
)

// The values of OAuthClient.kind and OAuthToken.kind (#695).
const (
	IdentityOAuthClientMetadataDocument = "metadataDocument"
	IdentityOAuthClientRegistered       = "registered"
	IdentityOAuthAccessToken            = "access"
	IdentityOAuthRefreshToken           = "refresh"
)

// UserTable returns the schema's User table, or nil when it has none. A
// verified schema has at most one.
func (s *Schema) UserTable() *TypeDef {
	return s.identityTable(func(td *TypeDef) bool { return td.User != nil })
}

// UserRoleTable returns the schema's UserRole table, or nil when it has
// none. A verified schema has at most one.
func (s *Schema) UserRoleTable() *TypeDef {
	return s.identityTable(func(td *TypeDef) bool { return td.UserRole != nil })
}

func (s *Schema) identityTable(is func(*TypeDef) bool) *TypeDef {
	if s == nil {
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(s.Types)) {
		if td := s.Types[name]; td != nil && is(td) {
			return td
		}
	}
	return nil
}

// IdentityConfigFieldSuffix is what the name of an API's identity config
// field adds to the API's name in upper snake case.
const IdentityConfigFieldSuffix = "_IDENTITY"

// IdentityConfigField returns the name of the identity config field of the
// API service named service, whose server authenticates with the identity
// runtime: its name in upper snake case and IdentityConfigFieldSuffix
// (`shop-api` is `SHOP_API_IDENTITY`), named as its callers field is
// (CallersField). Its value is the identity runtime's config as JSON, the
// Config the Go, TypeScript and Rust runtimes read alike; a server without
// one runs with the runtime's defaults.
func IdentityConfigField(service string) string {
	return EnvName(service) + IdentityConfigFieldSuffix
}
