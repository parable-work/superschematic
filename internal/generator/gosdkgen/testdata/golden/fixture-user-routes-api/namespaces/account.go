package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-user-routes-api"
)

// AccountNamespace contains API methods for the account namespace.
//
// Endpoints:
// - Capabilities: For each operation of the API an end user may call, keyed by its OpenAPI operation id, whether its route admits the caller. Requires authentication.
// - ChangePassword: Changes the caller's password, given their current one, and ends their other sessions. It answers true. Requires authentication.
// - CreateApiKey: Makes an API key of the caller's: a named token a program calls as them with, sent as Authorization: Bearer. It answers the key and its token, this once. The caller is signed in with a session; a key makes no key. Requires authentication.
// - ListApiKeys: Lists the caller's API keys that are not revoked, newest first, each without its token. Requires authentication.
// - Login: Signs a user in with their login and password and starts a session. A bearer session answers its token; a cookie session sets the session cookie and answers none.
// - Logout: Ends the caller's session and clears the session cookie. It answers true. Requires authentication.
// - Me: The caller's user, the roles they hold and the permissions those roles grant. Requires authentication.
// - Register: Creates a user with the login, name and password given, who waits for an administrator's approval: it answers 202 with pending true and starts no session. The first administrator, when the set names one, is signed in as login does.
// - RevokeApiKey: Revokes an API key of the caller's, which authenticates no request after. It answers true. Requires authentication.
type AccountNamespace struct {
	client jsonClient
}

// NewAccountNamespace creates a AccountNamespace.
func NewAccountNamespace(
	client jsonClient,
) *AccountNamespace {
	return &AccountNamespace{
		client: client,
	}
}

// Capabilities For each operation of the API an end user may call, keyed by its OpenAPI operation id, whether its route admits the caller.
// Requires authentication.
func (n *AccountNamespace) Capabilities(
	ctx context.Context,
) (types.Capabilities, error) {
	path := "/api/auth/capabilities"

	var requestBody any
	var out types.Capabilities
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.Capabilities
		return zero, err
	}
	return out, nil
}

// ChangePassword Changes the caller's password, given their current one, and ends their other sessions. It answers true.
// Requires authentication.
func (n *AccountNamespace) ChangePassword(
	ctx context.Context,
	input types.ChangePasswordInput,
) (bool, error) {
	path := "/api/auth/password"
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
		"POST",
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

// CreateApiKey Makes an API key of the caller's: a named token a program calls as them with, sent as Authorization: Bearer. It answers the key and its token, this once. The caller is signed in with a session; a key makes no key.
// Requires authentication.
func (n *AccountNamespace) CreateApiKey(
	ctx context.Context,
	input types.CreateApiKeyInput,
) (types.CreatedApiKey, error) {
	path := "/api/auth/keys"
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.CreatedApiKey
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.CreatedApiKey
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.CreatedApiKey
		return zero, err
	}
	return out, nil
}

// ListApiKeys Lists the caller's API keys that are not revoked, newest first, each without its token.
// Requires authentication.
func (n *AccountNamespace) ListApiKeys(
	ctx context.Context,
) ([]types.ApiKey, error) {
	path := "/api/auth/keys"

	var requestBody any
	var out []types.ApiKey
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero []types.ApiKey
		return zero, err
	}
	return out, nil
}

// Login Signs a user in with their login and password and starts a session. A bearer session answers its token; a cookie session sets the session cookie and answers none.
func (n *AccountNamespace) Login(
	ctx context.Context,
	input types.LoginInput,
) (types.LoginResult, error) {
	path := "/api/auth/login"
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.LoginResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.LoginResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.LoginResult
		return zero, err
	}
	return out, nil
}

// Logout Ends the caller's session and clears the session cookie. It answers true.
// Requires authentication.
func (n *AccountNamespace) Logout(
	ctx context.Context,
) (bool, error) {
	path := "/api/auth/logout"

	var requestBody any
	var out bool
	if err := n.client.DoJSON(
		ctx,
		"POST",
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

// Me The caller's user, the roles they hold and the permissions those roles grant.
// Requires authentication.
func (n *AccountNamespace) Me(
	ctx context.Context,
) (types.CurrentUser, error) {
	path := "/api/auth/me"

	var requestBody any
	var out types.CurrentUser
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.CurrentUser
		return zero, err
	}
	return out, nil
}

// Register Creates a user with the login, name and password given, who waits for an administrator's approval: it answers 202 with pending true and starts no session. The first administrator, when the set names one, is signed in as login does.
func (n *AccountNamespace) Register(
	ctx context.Context,
	input types.RegisterInput,
) (types.RegisterResult, error) {
	path := "/api/auth/register"
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.RegisterResult
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.RegisterResult
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.RegisterResult
		return zero, err
	}
	return out, nil
}

// RevokeApiKey Revokes an API key of the caller's, which authenticates no request after. It answers true.
// Requires authentication.
func (n *AccountNamespace) RevokeApiKey(
	ctx context.Context,
	Id string,
) (bool, error) {
	path := fmt.Sprintf("/api/auth/keys/%v", pathSegment(Id))

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
