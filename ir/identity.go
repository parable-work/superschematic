package ir

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
)

// The fields of the tables the loader adds. Every table's key is "id", an
// AutoGenerate<Identity.UUID>, and every relation to the User and UserRole
// tables cascades on delete.
const (
	// Session: id; user, the User table; tokenHash, a @unique Crypto.SHA256
	// of the session's token; createdAt and expiresAt, Temporal.DateTime;
	// lastSeenAt and revokedAt, nullable Temporal.DateTime. Revocation is
	// revokedAt, not a soft delete, so the ORM's soft-delete filter never
	// hides a revoked session (D33).
	IdentitySessionUserField       = "user"
	IdentitySessionTokenHashField  = "tokenHash"
	IdentitySessionCreatedAtField  = "createdAt"
	IdentitySessionExpiresAtField  = "expiresAt"
	IdentitySessionLastSeenAtField = "lastSeenAt"
	IdentitySessionRevokedAtField  = "revokedAt"

	// UserCredential: id; user, the User table, @unique; passwordHash, a
	// Secret string holding the PHC string; passwordChangedAt,
	// Temporal.DateTime; disabledAt, nullable Temporal.DateTime.
	IdentityCredentialUserField              = "user"
	IdentityCredentialPasswordHashField      = "passwordHash"
	IdentityCredentialPasswordChangedAtField = "passwordChangedAt"
	IdentityCredentialDisabledAtField        = "disabledAt"

	// UserRoleGrant: id; user, the User table; role, the UserRole table;
	// grantedAt, Temporal.DateTime; a unique index on (user, role).
	IdentityRoleGrantUserField      = "user"
	IdentityRoleGrantRoleField      = "role"
	IdentityRoleGrantGrantedAtField = "grantedAt"
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
	for _, name := range sortedStringMapKeys(s.Types) {
		if td := s.Types[name]; td != nil && is(td) {
			return td
		}
	}
	return nil
}
