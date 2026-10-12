package sdk

import (
	"example.com/schemas/sdk/go/fixture-user-routes-api/namespaces"
	"fmt"
)

// FixtureUserRoutesApiSDK is the root client for fixture-user-routes-api endpoints.
type FixtureUserRoutesApiSDK struct {
	client *HTTPClient
	// AccountNamespace exposes account namespace operations.
	//
	// Endpoints:
	// - Capabilities: For each operation of the API an end user may call, keyed by its OpenAPI operation id, whether its route admits the caller. Requires authentication.
	// - ChangePassword: Changes the caller's password, given their current one, and ends their other sessions. It answers true. Requires authentication.
	// - Login: Signs a user in with their login and password and starts a session. A bearer session answers its token; a cookie session sets the session cookie and answers none.
	// - Logout: Ends the caller's session and clears the session cookie. It answers true. Requires authentication.
	// - Me: The caller's user, the roles they hold and the permissions those roles grant. Requires authentication.
	// - Register: Creates a user with the login, name and password given and signs them in, as login does.
	AccountNamespace *namespaces.AccountNamespace
	// AccountAdminNamespace exposes account-admin namespace operations.
	//
	// Endpoints:
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
	AccountAdminNamespace *namespaces.AccountAdminNamespace
	// GreetingNamespace exposes greeting namespace operations.
	//
	// Endpoints:
	// - Greet: Greet endpoint. Requires authentication.
	GreetingNamespace *namespaces.GreetingNamespace
}

// New creates a new FixtureUserRoutesApiSDK.
func New(config SDKConfig) (*FixtureUserRoutesApiSDK, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	client, err := NewHTTPClient(config)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	sdk := &FixtureUserRoutesApiSDK{
		client:                client,
		AccountNamespace:      namespaces.NewAccountNamespace(client),
		AccountAdminNamespace: namespaces.NewAccountAdminNamespace(client),
		GreetingNamespace:     namespaces.NewGreetingNamespace(client),
	}
	return sdk, nil
}

// SetToken sets the static auth token used by the SDK client.
func (s *FixtureUserRoutesApiSDK) SetToken(token string) {
	s.client.SetToken(token)
}

// ClearToken removes the static auth token used by the SDK client.
func (s *FixtureUserRoutesApiSDK) ClearToken() {
	s.client.ClearToken()
}

// SetPublicEncryptionKey sets the default key for encrypted requests.
func (s *FixtureUserRoutesApiSDK) SetPublicEncryptionKey(key *PublicEncryptionKey) {
	s.client.SetPublicEncryptionKey(key)
}
