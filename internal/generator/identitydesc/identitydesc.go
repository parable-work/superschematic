// Package identitydesc builds the identity descriptor of a schema with the
// core user model (D50): the JSON that tells each server runtime's identity
// store which tables hold the users, their sessions, credentials, roles and
// role grants, and which column of each plays which part. README.md in this
// directory is its contract.
//
// The Go types generator writes the descriptor as identity/<schema>.json
// beside the types and as the constant IdentityDescriptor, the TypeScript
// types export it as identityDescriptor, and the Rust types hold it as
// IDENTITY_DESCRIPTOR. All three read it from here.
package identitydesc

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// Version is the descriptor format this package writes and the identity
// runtimes read.
const Version = 1

// Descriptor is an identity descriptor as the identity runtimes read it.
// Every table and column is named as the sql generator names it.
type Descriptor struct {
	Version    int        `json:"version"`
	User       User       `json:"user"`
	Session    Session    `json:"session"`
	Credential Credential `json:"credential"`
	// APIKey is the UserApiKey table (D114). A descriptor written before it
	// has none, and a store built from one has no API keys.
	APIKey *APIKey `json:"apiKey,omitempty"`
	// OAuth is the tables of the API's OAuth authorization server (#695).
	// A descriptor written before them has none, and a store built from
	// one serves no OAuth.
	OAuth *OAuth `json:"oauth,omitempty"`
	// Role and RoleGrant are nil when the schema has no UserRole table.
	Role      *Role      `json:"role,omitempty"`
	RoleGrant *RoleGrant `json:"roleGrant,omitempty"`
}

// User is the table with the User trait.
type User struct {
	// Type is the table's type name, the one the generated types carry.
	Type    string      `json:"type"`
	Table   string      `json:"table"`
	Columns UserColumns `json:"columns"`
	// KeyScalar and LoginScalar are the types of the key and login fields
	// as the schema names them ("Identity.UUID", "Contact.Email"), which
	// the runtime parses a key and a login with.
	KeyScalar   string `json:"keyScalar"`
	LoginScalar string `json:"loginScalar"`
	// NameScalar is the type of the name field ("Identity.Name", or a
	// builtin such as "string"), which the runtime checks a display name
	// with before it writes one. It is the login's when the name is the
	// login.
	NameScalar string `json:"nameScalar"`
}

// UserColumns are the user table's columns the runtime reads. Name is the
// login's column when the trait names no display name.
type UserColumns struct {
	Key   string `json:"key"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

// Session is the Session table the loader adds.
type Session struct {
	Table   string         `json:"table"`
	Columns SessionColumns `json:"columns"`
}

// SessionColumns are every column of the Session table.
type SessionColumns struct {
	ID         string `json:"id"`
	User       string `json:"user"`
	TokenHash  string `json:"tokenHash"`
	CreatedAt  string `json:"createdAt"`
	ExpiresAt  string `json:"expiresAt"`
	LastSeenAt string `json:"lastSeenAt"`
	RevokedAt  string `json:"revokedAt"`
}

// Credential is the UserCredential table the loader adds.
type Credential struct {
	Table   string            `json:"table"`
	Columns CredentialColumns `json:"columns"`
}

// CredentialColumns are every column of the UserCredential table.
type CredentialColumns struct {
	ID                string `json:"id"`
	User              string `json:"user"`
	PasswordHash      string `json:"passwordHash"`
	PasswordChangedAt string `json:"passwordChangedAt"`
	DisabledAt        string `json:"disabledAt"`
	PendingAt         string `json:"pendingAt"`
}

// Role is the table with the UserRole trait.
type Role struct {
	Type    string      `json:"type"`
	Table   string      `json:"table"`
	Columns RoleColumns `json:"columns"`
	// KeyScalar is the type of the role table's key field, which the
	// runtime parses a role id with.
	KeyScalar string `json:"keyScalar"`
}

// RoleColumns are the role table's columns the runtime reads and writes.
type RoleColumns struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Permissions string `json:"permissions"`
}

// RoleGrant is the UserRoleGrant table the loader adds beside a UserRole
// table.
type RoleGrant struct {
	Table   string           `json:"table"`
	Columns RoleGrantColumns `json:"columns"`
}

// RoleGrantColumns are every column of the UserRoleGrant table.
type RoleGrantColumns struct {
	ID        string `json:"id"`
	User      string `json:"user"`
	Role      string `json:"role"`
	GrantedAt string `json:"grantedAt"`
}

// APIKey is the UserApiKey table the loader adds (D114).
type APIKey struct {
	Table   string        `json:"table"`
	Columns APIKeyColumns `json:"columns"`
}

// APIKeyColumns are every column of the UserApiKey table.
type APIKeyColumns struct {
	ID          string `json:"id"`
	User        string `json:"user"`
	Name        string `json:"name"`
	TokenHash   string `json:"tokenHash"`
	Permissions string `json:"permissions"`
	CreatedAt   string `json:"createdAt"`
	ExpiresAt   string `json:"expiresAt"`
	LastUsedAt  string `json:"lastUsedAt"`
	RevokedAt   string `json:"revokedAt"`
}

// OAuth is the four tables the loader adds for an OAuth authorization
// server (#695).
type OAuth struct {
	Client OAuthClient `json:"client"`
	Grant  OAuthGrant  `json:"grant"`
	Code   OAuthCode   `json:"code"`
	Token  OAuthToken  `json:"token"`
}

// OAuthClient is the OAuthClient table.
type OAuthClient struct {
	Table   string             `json:"table"`
	Columns OAuthClientColumns `json:"columns"`
}

// OAuthClientColumns are every column of the OAuthClient table.
type OAuthClientColumns struct {
	ID           string `json:"id"`
	ClientID     string `json:"clientId"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	URI          string `json:"uri"`
	RedirectURIs string `json:"redirectUris"`
	CreatedAt    string `json:"createdAt"`
	RefreshedAt  string `json:"refreshedAt"`
	FreshUntil   string `json:"freshUntil"`
	LastUsedAt   string `json:"lastUsedAt"`
}

// OAuthGrant is the OAuthGrant table.
type OAuthGrant struct {
	Table   string            `json:"table"`
	Columns OAuthGrantColumns `json:"columns"`
}

// OAuthGrantColumns are every column of the OAuthGrant table.
type OAuthGrantColumns struct {
	ID          string `json:"id"`
	User        string `json:"user"`
	Client      string `json:"client"`
	Permissions string `json:"permissions"`
	Resource    string `json:"resource"`
	CreatedAt   string `json:"createdAt"`
	ExpiresAt   string `json:"expiresAt"`
	LastUsedAt  string `json:"lastUsedAt"`
	RevokedAt   string `json:"revokedAt"`
}

// OAuthCode is the OAuthCode table.
type OAuthCode struct {
	Table   string           `json:"table"`
	Columns OAuthCodeColumns `json:"columns"`
}

// OAuthCodeColumns are every column of the OAuthCode table.
type OAuthCodeColumns struct {
	ID            string `json:"id"`
	Client        string `json:"client"`
	User          string `json:"user"`
	Grant         string `json:"grant"`
	RedirectURI   string `json:"redirectUri"`
	CodeChallenge string `json:"codeChallenge"`
	State         string `json:"state"`
	Scope         string `json:"scope"`
	Resource      string `json:"resource"`
	Nonce         string `json:"nonce"`
	CodeHash      string `json:"codeHash"`
	CreatedAt     string `json:"createdAt"`
	ExpiresAt     string `json:"expiresAt"`
	UsedAt        string `json:"usedAt"`
}

// OAuthToken is the OAuthToken table.
type OAuthToken struct {
	Table   string            `json:"table"`
	Columns OAuthTokenColumns `json:"columns"`
}

// OAuthTokenColumns are every column of the OAuthToken table.
type OAuthTokenColumns struct {
	ID        string `json:"id"`
	Grant     string `json:"grant"`
	Kind      string `json:"kind"`
	TokenHash string `json:"tokenHash"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	UsedAt    string `json:"usedAt"`
	RevokedAt string `json:"revokedAt"`
}

// JSON is the descriptor as written to identity/<schema>.json and into the
// types' constants: two-space indented, with a trailing newline.
func (d Descriptor) JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return nil, fmt.Errorf("identitydesc: encode the descriptor: %w", err)
	}
	return buf.Bytes(), nil
}

// Describe returns schema's identity descriptor, and false when the schema
// has no User table. It reads the expanded schema, in which the loader has
// added the Session, UserCredential and, beside a UserRole table,
// UserRoleGrant tables (ir/identity.go). It fails when one of them is
// missing or is not the loader's, or when a table lacks a field the
// descriptor names.
func Describe(schema *ir.Schema) (Descriptor, bool, error) {
	userTable := schema.UserTable()
	if userTable == nil {
		return Descriptor{}, false, nil
	}
	d, err := describe(schema, userTable, schema.UserRoleTable())
	if err != nil {
		return Descriptor{}, false, fmt.Errorf("identitydesc: %w", err)
	}
	return d, true, nil
}

func describe(schema *ir.Schema, userTable, roleTable *ir.TypeDef) (Descriptor, error) {
	user := columnsOf(schema, userTable)
	key := user.key()
	login := user.field(userTable.User.Login)
	name := user.field(userTable.User.NameField())
	d := Descriptor{
		Version: Version,
		User: User{
			Type:  userTable.Name,
			Table: sqlutil.TableName(userTable),
			Columns: UserColumns{
				Key:   user.column(key),
				Login: user.column(login),
				Name:  user.column(name),
			},
		},
	}
	if user.err != nil {
		return Descriptor{}, user.err
	}
	d.User.KeyScalar, d.User.LoginScalar, d.User.NameScalar = key.TypeRef.Name, login.TypeRef.Name, name.TypeRef.Name

	sessionTable, err := added(schema, ir.IdentitySessionTable)
	if err != nil {
		return Descriptor{}, err
	}
	session := columnsOf(schema, sessionTable)
	d.Session = Session{
		Table: codegen.ToSnakeCase(sessionTable.Name),
		Columns: SessionColumns{
			ID:         session.column(session.key()),
			User:       session.relation(ir.IdentitySessionUserField, userTable),
			TokenHash:  session.column(session.field(ir.IdentitySessionTokenHashField)),
			CreatedAt:  session.column(session.field(ir.IdentitySessionCreatedAtField)),
			ExpiresAt:  session.column(session.field(ir.IdentitySessionExpiresAtField)),
			LastSeenAt: session.column(session.field(ir.IdentitySessionLastSeenAtField)),
			RevokedAt:  session.column(session.field(ir.IdentitySessionRevokedAtField)),
		},
	}
	if session.err != nil {
		return Descriptor{}, session.err
	}

	credentialTable, err := added(schema, ir.IdentityCredentialTable)
	if err != nil {
		return Descriptor{}, err
	}
	credential := columnsOf(schema, credentialTable)
	d.Credential = Credential{
		Table: codegen.ToSnakeCase(credentialTable.Name),
		Columns: CredentialColumns{
			ID:                credential.column(credential.key()),
			User:              credential.relation(ir.IdentityCredentialUserField, userTable),
			PasswordHash:      credential.column(credential.field(ir.IdentityCredentialPasswordHashField)),
			PasswordChangedAt: credential.column(credential.field(ir.IdentityCredentialPasswordChangedAtField)),
			DisabledAt:        credential.column(credential.field(ir.IdentityCredentialDisabledAtField)),
			PendingAt:         credential.column(credential.field(ir.IdentityCredentialPendingAtField)),
		},
	}
	if credential.err != nil {
		return Descriptor{}, credential.err
	}

	keyTable, err := added(schema, ir.IdentityAPIKeyTable)
	if err != nil {
		return Descriptor{}, err
	}
	apiKey := columnsOf(schema, keyTable)
	d.APIKey = &APIKey{
		Table: codegen.ToSnakeCase(keyTable.Name),
		Columns: APIKeyColumns{
			ID:          apiKey.column(apiKey.key()),
			User:        apiKey.relation(ir.IdentityAPIKeyUserField, userTable),
			Name:        apiKey.column(apiKey.field(ir.IdentityAPIKeyNameField)),
			TokenHash:   apiKey.column(apiKey.field(ir.IdentityAPIKeyTokenHashField)),
			Permissions: apiKey.column(apiKey.field(ir.IdentityAPIKeyPermissionsField)),
			CreatedAt:   apiKey.column(apiKey.field(ir.IdentityAPIKeyCreatedAtField)),
			ExpiresAt:   apiKey.column(apiKey.field(ir.IdentityAPIKeyExpiresAtField)),
			LastUsedAt:  apiKey.column(apiKey.field(ir.IdentityAPIKeyLastUsedAtField)),
			RevokedAt:   apiKey.column(apiKey.field(ir.IdentityAPIKeyRevokedAtField)),
		},
	}
	if apiKey.err != nil {
		return Descriptor{}, apiKey.err
	}

	if d.OAuth, err = describeOAuth(schema, userTable); err != nil {
		return Descriptor{}, err
	}

	if roleTable == nil {
		return d, nil
	}
	role := columnsOf(schema, roleTable)
	roleKey := role.key()
	d.Role = &Role{
		Type:  roleTable.Name,
		Table: sqlutil.TableName(roleTable),
		Columns: RoleColumns{
			Key:         role.column(roleKey),
			Name:        role.column(role.field(ir.IdentityRoleNameField)),
			Permissions: role.column(role.field(ir.IdentityRolePermissionsField)),
		},
	}
	if role.err != nil {
		return Descriptor{}, role.err
	}
	d.Role.KeyScalar = roleKey.TypeRef.Name

	grantTable, err := added(schema, ir.IdentityRoleGrantTable)
	if err != nil {
		return Descriptor{}, err
	}
	grant := columnsOf(schema, grantTable)
	d.RoleGrant = &RoleGrant{
		Table: codegen.ToSnakeCase(grantTable.Name),
		Columns: RoleGrantColumns{
			ID:        grant.column(grant.key()),
			User:      grant.relation(ir.IdentityRoleGrantUserField, userTable),
			Role:      grant.relation(ir.IdentityRoleGrantRoleField, roleTable),
			GrantedAt: grant.column(grant.field(ir.IdentityRoleGrantGrantedAtField)),
		},
	}
	if grant.err != nil {
		return Descriptor{}, grant.err
	}
	return d, nil
}

// describeOAuth describes the four tables of the OAuth authorization
// server (#695).
func describeOAuth(schema *ir.Schema, userTable *ir.TypeDef) (*OAuth, error) {
	tables := map[string]*ir.TypeDef{}
	for _, name := range []string{ir.IdentityOAuthClientTable, ir.IdentityOAuthGrantTable, ir.IdentityOAuthCodeTable, ir.IdentityOAuthTokenTable} {
		td, err := added(schema, name)
		if err != nil {
			return nil, err
		}
		tables[name] = td
	}
	clientTable, grantTable := tables[ir.IdentityOAuthClientTable], tables[ir.IdentityOAuthGrantTable]
	client := columnsOf(schema, clientTable)
	grant := columnsOf(schema, grantTable)
	code := columnsOf(schema, tables[ir.IdentityOAuthCodeTable])
	token := columnsOf(schema, tables[ir.IdentityOAuthTokenTable])
	d := &OAuth{
		Client: OAuthClient{
			Table: codegen.ToSnakeCase(clientTable.Name),
			Columns: OAuthClientColumns{
				ID:           client.column(client.key()),
				ClientID:     client.column(client.field(ir.IdentityOAuthClientIDField)),
				Kind:         client.column(client.field(ir.IdentityOAuthClientKindField)),
				Name:         client.column(client.field(ir.IdentityOAuthClientNameField)),
				URI:          client.column(client.field(ir.IdentityOAuthClientURIField)),
				RedirectURIs: client.column(client.field(ir.IdentityOAuthClientRedirectURIsField)),
				CreatedAt:    client.column(client.field(ir.IdentityOAuthClientCreatedAtField)),
				RefreshedAt:  client.column(client.field(ir.IdentityOAuthClientRefreshedAtField)),
				FreshUntil:   client.column(client.field(ir.IdentityOAuthClientFreshUntilField)),
				LastUsedAt:   client.column(client.field(ir.IdentityOAuthClientLastUsedAtField)),
			},
		},
		Grant: OAuthGrant{
			Table: codegen.ToSnakeCase(grantTable.Name),
			Columns: OAuthGrantColumns{
				ID:          grant.column(grant.key()),
				User:        grant.relation(ir.IdentityOAuthGrantUserField, userTable),
				Client:      grant.relation(ir.IdentityOAuthGrantClientField, clientTable),
				Permissions: grant.column(grant.field(ir.IdentityOAuthGrantPermissionsField)),
				Resource:    grant.column(grant.field(ir.IdentityOAuthGrantResourceField)),
				CreatedAt:   grant.column(grant.field(ir.IdentityOAuthGrantCreatedAtField)),
				ExpiresAt:   grant.column(grant.field(ir.IdentityOAuthGrantExpiresAtField)),
				LastUsedAt:  grant.column(grant.field(ir.IdentityOAuthGrantLastUsedAtField)),
				RevokedAt:   grant.column(grant.field(ir.IdentityOAuthGrantRevokedAtField)),
			},
		},
		Code: OAuthCode{
			Table: codegen.ToSnakeCase(tables[ir.IdentityOAuthCodeTable].Name),
			Columns: OAuthCodeColumns{
				ID:            code.column(code.key()),
				Client:        code.relation(ir.IdentityOAuthCodeClientField, clientTable),
				User:          code.relation(ir.IdentityOAuthCodeUserField, userTable),
				Grant:         code.relation(ir.IdentityOAuthCodeGrantField, grantTable),
				RedirectURI:   code.column(code.field(ir.IdentityOAuthCodeRedirectURIField)),
				CodeChallenge: code.column(code.field(ir.IdentityOAuthCodeChallengeField)),
				State:         code.column(code.field(ir.IdentityOAuthCodeStateField)),
				Scope:         code.column(code.field(ir.IdentityOAuthCodeScopeField)),
				Resource:      code.column(code.field(ir.IdentityOAuthCodeResourceField)),
				Nonce:         code.column(code.field(ir.IdentityOAuthCodeNonceField)),
				CodeHash:      code.column(code.field(ir.IdentityOAuthCodeCodeHashField)),
				CreatedAt:     code.column(code.field(ir.IdentityOAuthCodeCreatedAtField)),
				ExpiresAt:     code.column(code.field(ir.IdentityOAuthCodeExpiresAtField)),
				UsedAt:        code.column(code.field(ir.IdentityOAuthCodeUsedAtField)),
			},
		},
		Token: OAuthToken{
			Table: codegen.ToSnakeCase(tables[ir.IdentityOAuthTokenTable].Name),
			Columns: OAuthTokenColumns{
				ID:        token.column(token.key()),
				Grant:     token.relation(ir.IdentityOAuthTokenGrantField, grantTable),
				Kind:      token.column(token.field(ir.IdentityOAuthTokenKindField)),
				TokenHash: token.column(token.field(ir.IdentityOAuthTokenTokenHashField)),
				CreatedAt: token.column(token.field(ir.IdentityOAuthTokenCreatedAtField)),
				ExpiresAt: token.column(token.field(ir.IdentityOAuthTokenExpiresAtField)),
				UsedAt:    token.column(token.field(ir.IdentityOAuthTokenUsedAtField)),
				RevokedAt: token.column(token.field(ir.IdentityOAuthTokenRevokedAtField)),
			},
		},
	}
	for _, c := range []*columns{client, grant, code, token} {
		if c.err != nil {
			return nil, c.err
		}
	}
	return d, nil
}

// added returns the table the loader adds under name beside a User table.
func added(schema *ir.Schema, name string) (*ir.TypeDef, error) {
	td := schema.Types[name]
	if td == nil {
		return nil, fmt.Errorf("the schema has a User table but no %s table, which the loader adds beside it", name)
	}
	if td.Origin != ir.OriginIdentity {
		return nil, fmt.Errorf("the schema's %s table is not the one the loader adds beside its User table: its origin is %q, not %q", name, td.Origin, ir.OriginIdentity)
	}
	return td, nil
}

// columns finds the fields of one table and names their columns, keeping
// the first field it fails to find.
type columns struct {
	schema *ir.Schema
	table  *ir.TypeDef
	err    error
}

func columnsOf(schema *ir.Schema, table *ir.TypeDef) *columns {
	return &columns{schema: schema, table: table}
}

// key is the table's @key field, else its field named id.
func (c *columns) key() *ir.FieldDef {
	for _, fd := range c.table.Fields {
		if fd.Key {
			return fd
		}
	}
	for _, fd := range c.table.Fields {
		if fd.Name == "id" {
			return fd
		}
	}
	c.fail(fmt.Errorf("table %s has no key field", c.table.Name))
	return nil
}

// field is the table's field named name.
func (c *columns) field(name string) *ir.FieldDef {
	for _, fd := range c.table.Fields {
		if fd.Name == name {
			return fd
		}
	}
	c.fail(fmt.Errorf("table %s has no field %s", c.table.Name, name))
	return nil
}

// relation is the column of the table's field named name, which must be a
// to-one relation to target.
func (c *columns) relation(name string, target *ir.TypeDef) string {
	fd := c.field(name)
	if fd == nil {
		return ""
	}
	relates := fd.TypeRef.Name == target.Name || (fd.Relation != nil && fd.Relation.Type == target.Name)
	if !relates || fd.TypeRef.IsArray || fd.TypeRef.IsMap || fd.JsonField {
		c.fail(fmt.Errorf("%s.%s is not a relation to %s", c.table.Name, name, target.Name))
		return ""
	}
	return c.column(fd)
}

// column is the column that holds fd, as the sql generator names it
// (graphdesc.Column), or "" for a field that was not found.
func (c *columns) column(fd *ir.FieldDef) string {
	if fd == nil {
		return ""
	}
	return graphdesc.Column(c.schema, fd)
}

func (c *columns) fail(err error) {
	if c.err == nil {
		c.err = err
	}
}
