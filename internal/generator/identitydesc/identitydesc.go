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
			Table: codegen.ToSnakeCase(userTable.Name),
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
		},
	}
	if credential.err != nil {
		return Descriptor{}, credential.err
	}

	if roleTable == nil {
		return d, nil
	}
	role := columnsOf(schema, roleTable)
	roleKey := role.key()
	d.Role = &Role{
		Type:  roleTable.Name,
		Table: codegen.ToSnakeCase(roleTable.Name),
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
