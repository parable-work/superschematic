package session

import "context"

type (
	principalIDKey   struct{}
	principalNameKey struct{}
	sessionIDKey     struct{}
	rolesKey         struct{}
)

// ContextWithPrincipalID returns a new context with the authenticated
// principal's ID set. An empty ID leaves the context unauthenticated.
func ContextWithPrincipalID(ctx context.Context, principalID string) context.Context {
	return context.WithValue(ctx, principalIDKey{}, principalID)
}

// GetPrincipalID returns the authenticated principal's ID, or "" when the
// request is unauthenticated.
func GetPrincipalID(ctx context.Context) string {
	if id, ok := ctx.Value(principalIDKey{}).(string); ok {
		return id
	}
	return ""
}

// HasPrincipalID reports whether the request is authenticated.
func HasPrincipalID(ctx context.Context) bool {
	return GetPrincipalID(ctx) != ""
}

// ContextWithPrincipalName returns a new context with the principal's
// display name set.
func ContextWithPrincipalName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, principalNameKey{}, name)
}

// GetPrincipalName returns the principal's display name, or "".
func GetPrincipalName(ctx context.Context) string {
	if name, ok := ctx.Value(principalNameKey{}).(string); ok {
		return name
	}
	return ""
}

// ContextWithSessionID returns a new context with the session ID set.
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// GetSessionID returns the session ID, or "".
func GetSessionID(ctx context.Context) string {
	if id, ok := ctx.Value(sessionIDKey{}).(string); ok {
		return id
	}
	return ""
}

// ContextWithRoles returns a new context with the principal's roles set.
func ContextWithRoles(ctx context.Context, roles []Role) context.Context {
	return context.WithValue(ctx, rolesKey{}, roles)
}

// GetRoles returns the principal's roles, or nil when none are set.
func GetRoles(ctx context.Context) []Role {
	if roles, ok := ctx.Value(rolesKey{}).([]Role); ok {
		return roles
	}
	return nil
}
