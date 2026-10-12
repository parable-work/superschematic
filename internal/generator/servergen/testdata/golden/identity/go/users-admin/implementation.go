// Package usersadmin implements the users-admin API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.
package usersadmin

import (
	api "example.com/schemas/api/users-admin"
)

// New builds the implementation of users-admin from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{}, nil
}

var _ api.Constructor = New
