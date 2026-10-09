// Package identity expands the core user model's traits (D50) of a
// verified schema into ordinary tables, as the version graph expansion does
// for a version graph. A schema whose User table holds the project's users
// gets the Session and UserCredential tables beside it and, when it also
// has a UserRole table, UserRoleGrant. ir/identity.go names every table and
// field.
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
var Tables = []string{ir.IdentitySessionTable, ir.IdentityCredentialTable, ir.IdentityRoleGrantTable}

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
	indexes := []GeneratedIndex{{ir.IdentitySessionTable, sessionUserIndex()}}
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
		},
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
