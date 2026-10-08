package identity

import (
	"context"
	"errors"
	"time"
)

// The errors a Store returns, which the service turns into problems.
var (
	// ErrNotFound: no row has the id, token hash or login.
	ErrNotFound = errors.New("identity: not found")
	// ErrLoginTaken: another user has the login, in the login column's
	// case rule.
	ErrLoginTaken = errors.New("identity: the login is taken")
	// ErrRoleNameTaken: another role has the name.
	ErrRoleNameTaken = errors.New("identity: the role name is taken")
	// ErrNoRoles: the schema has no UserRole table.
	ErrNoRoles = errors.New("identity: the schema has no roles")
)

// InvalidLoginError is a login the login scalar does not parse.
type InvalidLoginError struct {
	Scalar string
	Err    error
}

func (e *InvalidLoginError) Error() string {
	return "identity: the login is not a " + e.Scalar + ": " + e.Err.Error()
}

func (e *InvalidLoginError) Unwrap() error { return e.Err }

// User is a user as the identity routes show one. Every id is in its key
// scalar's wire form.
type User struct {
	ID    string
	Login string
	Name  string
	// Disabled: the user's credential has disabledAt set.
	Disabled bool
	// Roles are the roles the user holds, by name. GetUser and ListUsers
	// fill them in; other methods leave them nil.
	Roles []Role
}

// LoginRecord is a user and the password hash they sign in with.
type LoginRecord struct {
	User User
	// PasswordHash is the credential's PHC string: empty when the user has
	// no credential, or one with no password.
	PasswordHash string
}

// Session is a session row.
type Session struct {
	ID         string
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt *time.Time
	RevokedAt  *time.Time
}

// SessionRecord is a session and its user.
type SessionRecord struct {
	Session Session
	User    User
}

// Role is a role row.
type Role struct {
	ID          string
	Name        string
	Permissions []string
}

// NewUser is a user the store creates, with their credential.
type NewUser struct {
	// Login is the login as the caller gave it; the store parses it with
	// the login scalar.
	Login string
	// Name is the display name, written when the name column is not the
	// login's. Empty means the parsed login.
	Name         string
	PasswordHash string
	At           time.Time
}

// NewSession is a session the store creates.
type NewSession struct {
	UserID    string
	TokenHash string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Store is the identity runtime's storage over the tables the core owns
// and the project's user and role tables. Mutations the user model ties
// together run in one transaction: SetPassword with its revocations,
// SetDisabled with the revocations of a disable, CreateUser with the
// credential, and DeleteRole with the role's grants.
type Store interface {
	// FindLogin finds the user whose login equals login once the login
	// scalar parses it, with their password hash. A login the scalar
	// refuses is an *InvalidLoginError; one no user has is ErrNotFound.
	FindLogin(ctx context.Context, login string) (LoginRecord, error)
	// FindCredential is FindLogin by the user's id.
	FindCredential(ctx context.Context, userID string) (LoginRecord, error)
	// GetUser returns the user with id and their roles.
	GetUser(ctx context.Context, id string) (User, error)
	// ListUsers returns every user with their roles, by login.
	ListUsers(ctx context.Context) ([]User, error)
	// CreateUser creates a user and their credential. The database
	// generates the user's key. A taken login is ErrLoginTaken.
	CreateUser(ctx context.Context, user NewUser) (User, error)

	// SetPassword writes the user's password hash and passwordChangedAt,
	// and revokes the user's sessions but keepSession (none when empty).
	SetPassword(ctx context.Context, userID, passwordHash string, at time.Time, keepSession string) error
	// RehashPassword replaces the user's password hash when it is still
	// oldHash, keeping passwordChangedAt: the same password at a new cost.
	RehashPassword(ctx context.Context, userID, oldHash, newHash string) error
	// SetDisabled sets the credential's disabledAt to at, or clears it.
	// Disabling also revokes the user's sessions.
	SetDisabled(ctx context.Context, userID string, disabled bool, at time.Time) error

	// CreateSession creates a session. The database generates its key.
	CreateSession(ctx context.Context, s NewSession) (Session, error)
	// FindSession returns the session whose token hash is tokenHash, and
	// its user.
	FindSession(ctx context.Context, tokenHash string) (SessionRecord, error)
	// TouchSession sets lastSeenAt to at when it is null or before
	// staleBefore, so concurrent requests write it once.
	TouchSession(ctx context.Context, sessionID string, at, staleBefore time.Time) error
	// RevokeSession sets the session's revokedAt when it is null.
	RevokeSession(ctx context.Context, sessionID string, at time.Time) error
	// RevokeUserSessions revokes every live session of the user but
	// exceptSession (none when empty).
	RevokeUserSessions(ctx context.Context, userID, exceptSession string, at time.Time) error

	// HasRoles reports whether the schema has a UserRole table. Without
	// one, UserRoles answers none and the other role methods ErrNoRoles.
	HasRoles() bool
	// UserRoles returns the roles the user holds, by name.
	UserRoles(ctx context.Context, userID string) ([]Role, error)
	// ListRoles returns every role, by name.
	ListRoles(ctx context.Context) ([]Role, error)
	// GetRole returns the role with id.
	GetRole(ctx context.Context, id string) (Role, error)
	// CreateRole creates a role. A taken name is ErrRoleNameTaken.
	CreateRole(ctx context.Context, name string, permissions []string) (Role, error)
	// UpdateRole rewrites a role's name and permissions.
	UpdateRole(ctx context.Context, id, name string, permissions []string) (Role, error)
	// DeleteRole deletes a role and its grants.
	DeleteRole(ctx context.Context, id string) error
	// GrantRole grants the user the role; a role already held stays
	// granted. A user or role that does not exist is ErrNotFound.
	GrantRole(ctx context.Context, userID, roleID string, at time.Time) error
	// RevokeRole revokes the role from the user; one not held stays
	// revoked. A user or role that does not exist is ErrNotFound.
	RevokeRole(ctx context.Context, userID, roleID string) error
}
