// Package identity expands the core user model's traits (D50) of a
// verified schema into ordinary tables, as the version graph expansion does
// for a version graph. A schema whose User table holds the project's users
// gets the Session, UserCredential and UserApiKey (D114) tables beside it,
// the four tables of an OAuth authorization server (#695), and, when it
// also has a UserRole table, UserRoleGrant. ir/identity.go names every
// table and field.
//
// The OAuth tables are added beside every User table, as UserApiKey is,
// whether or not an API turns @userSessions({ oauth }) on: a database
// schema loads apart from the APIs whose authDb it is, and an empty table
// costs nothing.
//
// The sql, orm and types generators emit the result with no identity
// specific code. Every type, field and index the expansion adds carries
// ir.OriginIdentity, so the schema writer can skip it and write the traits
// instead.
package identity

import (
	ir "github.com/parable-work/superschematic/ir"
)

// Tables are the tables the expansion can add, in the order it adds them.
// A schema with a User table reserves every name, UserRoleGrant included.
var Tables = []string{
	ir.IdentitySessionTable, ir.IdentityCredentialTable, ir.IdentityAPIKeyTable,
	ir.IdentityOAuthClientTable, ir.IdentityOAuthGrantTable, ir.IdentityOAuthCodeTable, ir.IdentityOAuthTokenTable,
	ir.IdentityRoleGrantTable,
}

// Scalars the added fields use.
const (
	uuidScalar     = "Identity.UUID"
	dateTimeScalar = "Temporal.DateTime"
	sha256Scalar   = "Crypto.SHA256"
)

// keyField is the key of every added table.
const keyField = "id"

const cascade = "CASCADE"

// grantIndex is UserRoleGrant's unique (user, role) index: a user holds a
// role once. Each call returns a fresh key slice, so no schema shares it.
func grantIndex() ir.IndexDef {
	return ir.IndexDef{
		Keys:   []string{ir.IdentityRoleGrantUserField, ir.IdentityRoleGrantRoleField},
		Unique: true,
		Name:   "user_role",
		Origin: ir.OriginIdentity,
	}
}

// sessionUserIndex indexes Session by its user, so revoking every session
// of a user, and the cascade from a deleted user, find them without a scan.
// The other tables need none: UserCredential's user is unique, and
// UserRoleGrant's (user, role) index leads with the user.
func sessionUserIndex() ir.IndexDef {
	return ir.IndexDef{
		Keys:   []string{ir.IdentitySessionUserField},
		Name:   "user",
		Origin: ir.OriginIdentity,
	}
}

// apiKeyUserIndex indexes UserApiKey by its user, as Session is: a user's
// keys are listed and revoked by it.
func apiKeyUserIndex() ir.IndexDef {
	return ir.IndexDef{
		Keys:   []string{ir.IdentityAPIKeyUserField},
		Name:   "user",
		Origin: ir.OriginIdentity,
	}
}

// oauthGrantUserIndex indexes OAuthGrant by its user, whose grants are
// listed and revoked by it (#695).
func oauthGrantUserIndex() ir.IndexDef {
	return ir.IndexDef{
		Keys:   []string{ir.IdentityOAuthGrantUserField},
		Name:   "user",
		Origin: ir.OriginIdentity,
	}
}

// oauthTokenGrantIndex indexes OAuthToken by its grant, whose tokens a
// revocation ends together (#695).
func oauthTokenGrantIndex() ir.IndexDef {
	return ir.IndexDef{
		Keys:   []string{ir.IdentityOAuthTokenGrantField},
		Name:   "grant",
		Origin: ir.OriginIdentity,
	}
}

// GeneratedIndex is an index the expansion adds: the type whose table
// holds it, and the index.
type GeneratedIndex struct {
	Type  string
	Index ir.IndexDef
}

// Indexes returns the indexes the expansion of schema adds, so
// verification can refuse an authored index of the same name.
func Indexes(schema *ir.Schema) []GeneratedIndex {
	if schema.UserTable() == nil {
		return nil
	}
	indexes := []GeneratedIndex{
		{ir.IdentitySessionTable, sessionUserIndex()},
		{ir.IdentityAPIKeyTable, apiKeyUserIndex()},
		{ir.IdentityOAuthGrantTable, oauthGrantUserIndex()},
		{ir.IdentityOAuthTokenTable, oauthTokenGrantIndex()},
	}
	if schema.UserRoleTable() != nil {
		indexes = append(indexes, GeneratedIndex{ir.IdentityRoleGrantTable, grantIndex()})
	}
	return indexes
}

// Expand adds the tables of the user model of schema, when it has a User
// table. It assumes the traits passed verification. It returns the scalar
// names the added fields use, so the caller can hydrate any the schema did
// not already reference.
func Expand(schema *ir.Schema) []string {
	user := schema.UserTable()
	if user == nil {
		return nil
	}
	m := &model{schema: schema, user: user}
	m.addSession()
	m.addCredential()
	m.addAPIKey()
	m.addOAuth()
	if role := schema.UserRoleTable(); role != nil {
		m.addRoleGrant(role)
	}
	scalars := []string{uuidScalar, dateTimeScalar, sha256Scalar}
	for _, name := range scalars {
		if schema.Scalars[name] == nil {
			schema.Scalars[name] = &ir.ScalarDef{Name: name}
		}
	}
	return scalars
}

// model holds what the expansion reads.
type model struct {
	schema *ir.Schema
	user   *ir.TypeDef
}

// addSession adds Session: one row per signed-in session. The database
// keeps the SHA-256 of the session's token, never the token. A session ends
// at expiresAt or when revokedAt is set, which is not a soft delete, so the
// ORM's soft-delete filter never hides a revoked session (D33).
func (m *model) addSession() {
	m.addType(&ir.TypeDef{
		Name:    ir.IdentitySessionTable,
		Comment: "A signed-in session: the user it signs in, the SHA-256 of its token, and when it ends.",
		Fields: []*ir.FieldDef{
			keyFieldDef(),
			relation(ir.IdentitySessionUserField, m.user.Name, "The user the session signs in."),
			{
				Name:     ir.IdentitySessionTokenHashField,
				Comment:  "The SHA-256 of the session's token, which only the client holds.",
				TypeRef:  ir.TypeRef{Name: sha256Scalar},
				Required: true,
				Unique:   true,
			},
			dateTime(ir.IdentitySessionCreatedAtField, true, ""),
			dateTime(ir.IdentitySessionExpiresAtField, true, "When the session ends."),
			dateTime(ir.IdentitySessionLastSeenAtField, false, "When the session last authenticated a request, written at most once a minute."),
			dateTime(ir.IdentitySessionRevokedAtField, false, "When the session was revoked, by logout or by a change to its user."),
		},
		Indexes: []ir.IndexDef{sessionUserIndex()},
	})
}

// addCredential adds UserCredential: a user's password hash, apart from the
// user row so that no @source view or select of every column exposes it.
func (m *model) addCredential() {
	user := relation(ir.IdentityCredentialUserField, m.user.Name, "The user the credential signs in; a user has one.")
	user.Unique = true
	m.addType(&ir.TypeDef{
		Name:    ir.IdentityCredentialTable,
		Comment: "A user's password hash, kept apart from the user row.",
		Fields: []*ir.FieldDef{
			keyFieldDef(),
			user,
			{
				Name:     ir.IdentityCredentialPasswordHashField,
				Comment:  "The password's argon2id hash, a PHC string.",
				TypeRef:  ir.TypeRef{Name: "string"},
				Required: true,
				Secret:   true,
			},
			dateTime(ir.IdentityCredentialPasswordChangedAtField, true, ""),
			dateTime(ir.IdentityCredentialDisabledAtField, false, "When the user was disabled; a disabled user cannot sign in."),
			dateTime(ir.IdentityCredentialPendingAtField, false, "When the user registered to wait for an administrator's approval; a pending user cannot sign in (D81)."),
		},
	})
}

// addAPIKey adds UserApiKey (D114): one row per API key, a named token a
// user makes for a program that calls as them. The database keeps the
// SHA-256 of the key's token, as a session's. A key ends at expiresAt or
// when revokedAt is set, and has no idle rule. permissions, when it holds a
// list, is the subset of its user's permissions the key was made with.
func (m *model) addAPIKey() {
	maxLength := ir.IdentityAPIKeyNameMaxLength
	minLength := 1
	m.addType(&ir.TypeDef{
		Name:    ir.IdentityAPIKeyTable,
		Comment: "An API key: the user it calls as, its name, the SHA-256 of its token, and when it ends.",
		Fields: []*ir.FieldDef{
			keyFieldDef(),
			relation(ir.IdentityAPIKeyUserField, m.user.Name, "The user the key calls as."),
			{
				Name:              ir.IdentityAPIKeyNameField,
				Comment:           "What its user calls the key.",
				TypeRef:           ir.TypeRef{Name: "string"},
				Required:          true,
				ValidateMinLength: &minLength,
				ValidateMaxLength: &maxLength,
			},
			{
				Name:     ir.IdentityAPIKeyTokenHashField,
				Comment:  "The SHA-256 of the key's token, which only its holder has.",
				TypeRef:  ir.TypeRef{Name: sha256Scalar},
				Required: true,
				Unique:   true,
			},
			{
				Name:    ir.IdentityAPIKeyPermissionsField,
				Comment: "The permissions the key was made with, each held while its user holds it; null for every permission its user holds.",
				TypeRef: ir.TypeRef{Name: "string", IsArray: true},
			},
			dateTime(ir.IdentityAPIKeyCreatedAtField, true, ""),
			dateTime(ir.IdentityAPIKeyExpiresAtField, true, "When the key ends."),
			dateTime(ir.IdentityAPIKeyLastUsedAtField, false, "When the key last authenticated a request, written at most once a minute."),
			dateTime(ir.IdentityAPIKeyRevokedAtField, false, "When the key was revoked, by its user, by an administrator, or when its user was disabled."),
		},
		Indexes: []ir.IndexDef{apiKeyUserIndex()},
	})
}

// addOAuth adds the tables of the API's OAuth authorization server
// (#695): OAuthClient, OAuthGrant, OAuthCode and OAuthToken. The database
// keeps the SHA-256 of each code and token, never its text. A grant ends
// at expiresAt or when revokedAt is set, and its tokens with it.
func (m *model) addOAuth() {
	minLength := 1
	m.addType(&ir.TypeDef{
		Name:    ir.IdentityOAuthClientTable,
		Comment: "An OAuth client: a metadata document's, cached, or one that registered.",
		Fields: []*ir.FieldDef{
			keyFieldDef(),
			{
				Name:              ir.IdentityOAuthClientIDField,
				Comment:           "The client_id the client is known by: its metadata document's URL, or the id registration made.",
				TypeRef:           ir.TypeRef{Name: "string"},
				Required:          true,
				Unique:            true,
				ValidateMinLength: &minLength,
			},
			text(ir.IdentityOAuthClientKindField, true, "metadataDocument or registered."),
			text(ir.IdentityOAuthClientNameField, true, "What the consent page calls the client."),
			text(ir.IdentityOAuthClientURIField, false, "The client's home page."),
			{
				Name:     ir.IdentityOAuthClientRedirectURIsField,
				Comment:  "The redirects the client may use.",
				TypeRef:  ir.TypeRef{Name: "string", IsArray: true},
				Required: true,
			},
			dateTime(ir.IdentityOAuthClientCreatedAtField, true, ""),
			dateTime(ir.IdentityOAuthClientRefreshedAtField, false, "When the client's metadata document was last fetched."),
			dateTime(ir.IdentityOAuthClientFreshUntilField, false, "Until when the metadata document's cache headers keep it."),
			dateTime(ir.IdentityOAuthClientLastUsedAtField, false, "When an authorization last named the client."),
		},
	})
	m.addType(&ir.TypeDef{
		Name:    ir.IdentityOAuthGrantTable,
		Comment: "An OAuth grant: the user who gave it, the client that holds it, the permissions it narrows them to, and when it ends.",
		Fields: []*ir.FieldDef{
			keyFieldDef(),
			relation(ir.IdentityOAuthGrantUserField, m.user.Name, "The user the grant calls as."),
			relation(ir.IdentityOAuthGrantClientField, ir.IdentityOAuthClientTable, "The client that holds the grant."),
			{
				Name:    ir.IdentityOAuthGrantPermissionsField,
				Comment: "The permissions the grant was given, each held while its user holds it; null for every permission its user holds.",
				TypeRef: ir.TypeRef{Name: "string", IsArray: true},
			},
			text(ir.IdentityOAuthGrantResourceField, true, "The URL of the resource the grant was given for."),
			dateTime(ir.IdentityOAuthGrantCreatedAtField, true, ""),
			dateTime(ir.IdentityOAuthGrantExpiresAtField, true, "When the grant ends, however often its refresh token rotates."),
			dateTime(ir.IdentityOAuthGrantLastUsedAtField, false, "When the grant's token last authenticated a request, written at most once a minute."),
			dateTime(ir.IdentityOAuthGrantRevokedAtField, false, "When the grant was revoked, by its user, by an administrator, by a replayed code or refresh token, or when its user was disabled."),
		},
		Indexes: []ir.IndexDef{oauthGrantUserIndex()},
	})
	user := relation(ir.IdentityOAuthCodeUserField, m.user.Name, "The user who approved the request.")
	user.Required = false
	grant := relation(ir.IdentityOAuthCodeGrantField, ir.IdentityOAuthGrantTable, "The grant approval made.")
	grant.Required = false
	m.addType(&ir.TypeDef{
		Name:    ir.IdentityOAuthCodeTable,
		Comment: "An OAuth authorization request: pending while its user consents, then the code its client redeems once.",
		Fields: []*ir.FieldDef{
			keyFieldDef(),
			relation(ir.IdentityOAuthCodeClientField, ir.IdentityOAuthClientTable, "The client that asked."),
			user,
			grant,
			text(ir.IdentityOAuthCodeRedirectURIField, true, "The redirect the request named, which the code is bound to."),
			text(ir.IdentityOAuthCodeChallengeField, true, "The PKCE challenge, S256, which the code's verifier must answer."),
			text(ir.IdentityOAuthCodeStateField, false, "The state the client sent, which the redirect carries back."),
			text(ir.IdentityOAuthCodeScopeField, false, "The scope the client asked for, as it sent it."),
			text(ir.IdentityOAuthCodeResourceField, true, "The resource the request named, which the code is bound to."),
			text(ir.IdentityOAuthCodeNonceField, true, "The value approve and deny send back, which ties a decision to the request the page read."),
			{
				Name:    ir.IdentityOAuthCodeCodeHashField,
				Comment: "The SHA-256 of the code approval made, which only the client holds.",
				TypeRef: ir.TypeRef{Name: sha256Scalar},
				Unique:  true,
			},
			dateTime(ir.IdentityOAuthCodeCreatedAtField, true, ""),
			dateTime(ir.IdentityOAuthCodeExpiresAtField, true, "When the request, or once approved its code, ends."),
			dateTime(ir.IdentityOAuthCodeUsedAtField, false, "When the code was redeemed; a second redemption revokes its grant."),
		},
	})
	m.addType(&ir.TypeDef{
		Name:    ir.IdentityOAuthTokenTable,
		Comment: "An OAuth access or refresh token: the grant that issued it, the SHA-256 of its text, and when it ends.",
		Fields: []*ir.FieldDef{
			keyFieldDef(),
			relation(ir.IdentityOAuthTokenGrantField, ir.IdentityOAuthGrantTable, "The grant that issued the token."),
			text(ir.IdentityOAuthTokenKindField, true, "access or refresh."),
			{
				Name:     ir.IdentityOAuthTokenTokenHashField,
				Comment:  "The SHA-256 of the token, which only its client holds.",
				TypeRef:  ir.TypeRef{Name: sha256Scalar},
				Required: true,
				Unique:   true,
			},
			dateTime(ir.IdentityOAuthTokenCreatedAtField, true, ""),
			dateTime(ir.IdentityOAuthTokenExpiresAtField, true, "When the token ends."),
			dateTime(ir.IdentityOAuthTokenUsedAtField, false, "When a refresh token rotated; a second use revokes its grant."),
			dateTime(ir.IdentityOAuthTokenRevokedAtField, false, "When the token was revoked."),
		},
		Indexes: []ir.IndexDef{oauthTokenGrantIndex()},
	})
}

// addRoleGrant adds UserRoleGrant: one row per role a user holds.
func (m *model) addRoleGrant(role *ir.TypeDef) {
	m.addType(&ir.TypeDef{
		Name:    ir.IdentityRoleGrantTable,
		Comment: "A role a user holds.",
		Fields: []*ir.FieldDef{
			keyFieldDef(),
			relation(ir.IdentityRoleGrantUserField, m.user.Name, ""),
			relation(ir.IdentityRoleGrantRoleField, role.Name, ""),
			dateTime(ir.IdentityRoleGrantGrantedAtField, true, ""),
		},
		Indexes: []ir.IndexDef{grantIndex()},
	})
}

// addType adds td as a DB table owned by the User table's file, and marks
// it and its fields as the expansion's.
func (m *model) addType(td *ir.TypeDef) {
	td.Owner = m.user.Owner
	td.Role = ir.RoleDBTable
	td.Origin = ir.OriginIdentity
	for _, fd := range td.Fields {
		fd.Origin = ir.OriginIdentity
	}
	m.schema.Types[td.Name] = td
}

func keyFieldDef() *ir.FieldDef {
	return &ir.FieldDef{Name: keyField, TypeRef: ir.TypeRef{Name: uuidScalar}, Required: true, Key: true, AutoGenerated: true}
}

// relation returns a required to-one relation field whose rows go when the
// row it names is deleted.
func relation(name, target, comment string) *ir.FieldDef {
	return &ir.FieldDef{
		Name:     name,
		Comment:  comment,
		TypeRef:  ir.TypeRef{Name: target},
		Required: true,
		Relation: &ir.RelationDef{Type: target, OnDelete: cascade},
	}
}

func dateTime(name string, required bool, comment string) *ir.FieldDef {
	return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ir.TypeRef{Name: dateTimeScalar}, Required: required}
}

func text(name string, required bool, comment string) *ir.FieldDef {
	return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ir.TypeRef{Name: "string"}, Required: required}
}
