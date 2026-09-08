package session

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by store methods when the requested record does
// not exist.
var ErrNotFound = errors.New("record not found")

// Record is the session row a Store returns. Pointer fields are nullable
// columns: nil means not set.
type Record struct {
	ID          string
	PrincipalID string
	ExpiresAt   time.Time
	DeletedAt   *time.Time
}

// Principal is the authenticated actor a session belongs to.
type Principal struct {
	ID   string
	Name string
}

// Role is a named set of permissions assigned to a principal.
type Role struct {
	ID          string
	Name        string
	Permissions []string
}

// Store resolves a bearer token's JWT id to its session.
type Store interface {
	FindByJTI(ctx context.Context, jti string) (Record, error)
}

// PrincipalStore loads the principal a session belongs to.
type PrincipalStore interface {
	GetByID(ctx context.Context, id string) (Principal, error)
}

// RoleStore lists the roles assigned to a principal.
type RoleStore interface {
	ListRolesForPrincipal(ctx context.Context, principalID string) ([]Role, error)
}
