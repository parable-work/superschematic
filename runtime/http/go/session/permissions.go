package session

import "strings"

// Covers reports whether a granted permission satisfies a required one:
// they are equal, or the required permission is nested under the granted
// one ("a.b" and "a.b.c" are covered by "a"; "ab" is not).
func Covers(granted, required string) bool {
	return granted == required || strings.HasPrefix(required, granted+".")
}

// HasAnyPermission reports whether any granted permission covers any
// required one. An empty required set is always satisfied.
func HasAnyPermission(granted, required []string) bool {
	if len(required) == 0 {
		return true
	}
	for _, req := range required {
		for _, have := range granted {
			if Covers(have, req) {
				return true
			}
		}
	}
	return false
}

// AnyRoleCovers reports whether any role carries a permission that covers
// required.
func AnyRoleCovers(roles []Role, required string) bool {
	for i := range roles {
		if HasAnyPermission(roles[i].Permissions, []string{required}) {
			return true
		}
	}
	return false
}

// AnyRoleCoversAny reports whether any role carries a permission that
// covers any of required. An empty required set is always satisfied.
func AnyRoleCoversAny(roles []Role, required []string) bool {
	if len(required) == 0 {
		return true
	}
	for _, req := range required {
		if AnyRoleCovers(roles, req) {
			return true
		}
	}
	return false
}
