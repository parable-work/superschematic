package namespaces

import (
	"context"
	"fmt"
	"net/url"

	"example.com/schemas/sdk/go/fixture-user-routes-api/runtime"
	types "example.com/schemas/types/go/fixture-user-routes-api"
)

// AccountAdminNamespace contains API methods for the account-admin namespace.
//
// Endpoints:
// - ApproveUser: Approves a user who registered to wait for approval, who can then sign in with the roles register granted them. The caller's own permissions must cover those roles'. Requires authentication.
// - CreateRole: Creates a role. The caller's own permissions must cover each permission it grants. Requires authentication.
// - CreateUser: Creates a user with the login, name and password given. Requires authentication.
// - DeleteRole: Deletes a role and every grant of it. It answers true. Requires authentication.
// - DisableUser: Disables a user, who can no longer sign in, and ends their sessions. Requires authentication.
// - EnableUser: Enables a disabled user. Requires authentication.
// - GetUser: One user and the roles they hold. Requires authentication.
// - GrantRole: Grants a user a role. The caller's own permissions must cover the role's. Requires authentication.
// - ListRoles: Lists the roles and the permissions each grants. Requires authentication.
// - ListUsers: Lists the users and the roles each holds. Requires authentication.
// - RevokeRole: Revokes a role from a user. Requires authentication.
// - SetUserPassword: Sets a user's password and ends their sessions. It answers true. Requires authentication.
// - UpdateRole: Renames a role or replaces its permissions. The caller's own permissions must cover each permission given. Requires authentication.
type AccountAdminNamespace struct {
	client jsonClient
}

// NewAccountAdminNamespace creates a AccountAdminNamespace.
func NewAccountAdminNamespace(
	client jsonClient,
) *AccountAdminNamespace {
	return &AccountAdminNamespace{
		client: client,
	}
}

// ApproveUser Approves a user who registered to wait for approval, who can then sign in with the roles register granted them. The caller's own permissions must cover those roles'.
// Requires authentication.
func (n *AccountAdminNamespace) ApproveUser(
	ctx context.Context,
	Id string,
) (types.IdentityUser, error) {
	path := fmt.Sprintf("/api/auth/admin/users/%v/approve", pathSegment(Id))

	var requestBody any
	var out types.IdentityUser
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.IdentityUser
		return zero, err
	}
	return out, nil
}

// CreateRole Creates a role. The caller's own permissions must cover each permission it grants.
// Requires authentication.
func (n *AccountAdminNamespace) CreateRole(
	ctx context.Context,
	input types.RoleInput,
) (types.IdentityRole, error) {
	path := "/api/auth/admin/roles"
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.IdentityRole
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.IdentityRole
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.IdentityRole
		return zero, err
	}
	return out, nil
}

// CreateUser Creates a user with the login, name and password given.
// Requires authentication.
func (n *AccountAdminNamespace) CreateUser(
	ctx context.Context,
	input types.CreateUserInput,
) (types.IdentityUser, error) {
	path := "/api/auth/admin/users"
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.IdentityUser
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.IdentityUser
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.IdentityUser
		return zero, err
	}
	return out, nil
}

// DeleteRole Deletes a role and every grant of it. It answers true.
// Requires authentication.
func (n *AccountAdminNamespace) DeleteRole(
	ctx context.Context,
	Id string,
) (bool, error) {
	path := fmt.Sprintf("/api/auth/admin/roles/%v", pathSegment(Id))

	var requestBody any
	var out bool
	if err := n.client.DoJSON(
		ctx,
		"DELETE",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero bool
		return zero, err
	}
	return out, nil
}

// DisableUser Disables a user, who can no longer sign in, and ends their sessions.
// Requires authentication.
func (n *AccountAdminNamespace) DisableUser(
	ctx context.Context,
	Id string,
) (types.IdentityUser, error) {
	path := fmt.Sprintf("/api/auth/admin/users/%v/disable", pathSegment(Id))

	var requestBody any
	var out types.IdentityUser
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.IdentityUser
		return zero, err
	}
	return out, nil
}

// EnableUser Enables a disabled user.
// Requires authentication.
func (n *AccountAdminNamespace) EnableUser(
	ctx context.Context,
	Id string,
) (types.IdentityUser, error) {
	path := fmt.Sprintf("/api/auth/admin/users/%v/enable", pathSegment(Id))

	var requestBody any
	var out types.IdentityUser
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.IdentityUser
		return zero, err
	}
	return out, nil
}

// GetUser One user and the roles they hold.
// Requires authentication.
func (n *AccountAdminNamespace) GetUser(
	ctx context.Context,
	Id string,
) (types.IdentityUser, error) {
	path := fmt.Sprintf("/api/auth/admin/users/%v", pathSegment(Id))

	var requestBody any
	var out types.IdentityUser
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.IdentityUser
		return zero, err
	}
	return out, nil
}

// GrantRole Grants a user a role. The caller's own permissions must cover the role's.
// Requires authentication.
func (n *AccountAdminNamespace) GrantRole(
	ctx context.Context,
	Id string,
	RoleId string,
) (types.IdentityUser, error) {
	path := fmt.Sprintf("/api/auth/admin/users/%v/roles/%v", pathSegment(Id), pathSegment(RoleId))

	var requestBody any
	var out types.IdentityUser
	if err := n.client.DoJSON(
		ctx,
		"PUT",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.IdentityUser
		return zero, err
	}
	return out, nil
}

// ListRoles Lists the roles and the permissions each grants.
// Requires authentication.
func (n *AccountAdminNamespace) ListRoles(
	ctx context.Context,
) ([]types.IdentityRole, error) {
	path := "/api/auth/admin/roles"

	var requestBody any
	var out []types.IdentityRole
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero []types.IdentityRole
		return zero, err
	}
	return out, nil
}

type AccountAdminListUsersInput struct {
	Pending *bool `json:"pending,omitempty"`
}

// ListUsers Lists the users and the roles each holds.
// Requires authentication.
func (n *AccountAdminNamespace) ListUsers(
	ctx context.Context,
	input AccountAdminListUsersInput,
) ([]types.IdentityUser, error) {
	path := "/api/auth/admin/users"
	validationErrors := types.NewValidationErrors()
	if input.Pending != nil {
		appendScalarValidationErrors(validationErrors, "pending", any(*input.Pending))
	}
	if validationErrors.HasErrors() {
		var zero []types.IdentityUser
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	// GET endpoints: scalar args go as query parameters, not request body.
	queryValues := url.Values{}
	if input.Pending != nil {
		runtime.AddQueryParam(queryValues, "pending", *input.Pending)
	}
	var out []types.IdentityUser
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		queryValues,
		requestBody,
		&out,
	); err != nil {
		var zero []types.IdentityUser
		return zero, err
	}
	return out, nil
}

// RevokeRole Revokes a role from a user.
// Requires authentication.
func (n *AccountAdminNamespace) RevokeRole(
	ctx context.Context,
	Id string,
	RoleId string,
) (types.IdentityUser, error) {
	path := fmt.Sprintf("/api/auth/admin/users/%v/roles/%v", pathSegment(Id), pathSegment(RoleId))

	var requestBody any
	var out types.IdentityUser
	if err := n.client.DoJSON(
		ctx,
		"DELETE",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.IdentityUser
		return zero, err
	}
	return out, nil
}

// SetUserPassword Sets a user's password and ends their sessions. It answers true.
// Requires authentication.
func (n *AccountAdminNamespace) SetUserPassword(
	ctx context.Context,
	Id string,
	input types.SetPasswordInput,
) (bool, error) {
	path := fmt.Sprintf("/api/auth/admin/users/%v/password", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero bool
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out bool
	if err := n.client.DoJSON(
		ctx,
		"PUT",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero bool
		return zero, err
	}
	return out, nil
}

// UpdateRole Renames a role or replaces its permissions. The caller's own permissions must cover each permission given.
// Requires authentication.
func (n *AccountAdminNamespace) UpdateRole(
	ctx context.Context,
	Id string,
	input types.RoleInput,
) (types.IdentityRole, error) {
	path := fmt.Sprintf("/api/auth/admin/roles/%v", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.IdentityRole
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.IdentityRole
	if err := n.client.DoJSON(
		ctx,
		"PUT",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.IdentityRole
		return zero, err
	}
	return out, nil
}
