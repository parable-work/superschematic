package sdk

import (
	"example.com/schemas/sdk/go/fixture-approvals-api/namespaces"
	"fmt"
)

// FixtureApprovalsApiSDK is the root client for fixture-approvals-api endpoints.
type FixtureApprovalsApiSDK struct {
	client *HTTPClient
	// TaskNamespace exposes task namespace operations.
	//
	// Endpoints:
	// - Approvals.ListSignOffs: Lists the instance's sign-offs, oldest first, one page at a time, the consumed ones included; with state, only those for it. Requires authentication.
	// - Approvals.SignOff: Signs off on the instance for a gated state, as the caller. Needs the gate's permission; refused for the instance's creator unless the gate allows it, while the instance is in the state, and for a caller whose sign-off for the state is not yet consumed. Requires authentication.
	// - Approvals.WithdrawSignOff: Withdraws the caller's sign-off for a gated state, one not yet consumed. Requires authentication.
	// - Status.Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
	TaskNamespace *namespaces.TaskNamespace
	// TaskAdminNamespace exposes task-admin namespace operations.
	//
	// Endpoints:
	// - ReassignTask: ReassignTask endpoint. Requires authentication.
	TaskAdminNamespace *namespaces.TaskAdminNamespace
}

// New creates a new FixtureApprovalsApiSDK.
func New(config SDKConfig) (*FixtureApprovalsApiSDK, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	client, err := NewHTTPClient(config)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	sdk := &FixtureApprovalsApiSDK{
		client:             client,
		TaskNamespace:      namespaces.NewTaskNamespace(client),
		TaskAdminNamespace: namespaces.NewTaskAdminNamespace(client),
	}
	return sdk, nil
}

// SetToken sets the static auth token used by the SDK client.
func (s *FixtureApprovalsApiSDK) SetToken(token string) {
	s.client.SetToken(token)
}

// ClearToken removes the static auth token used by the SDK client.
func (s *FixtureApprovalsApiSDK) ClearToken() {
	s.client.ClearToken()
}

// SetPublicEncryptionKey sets the default key for encrypted requests.
func (s *FixtureApprovalsApiSDK) SetPublicEncryptionKey(key *PublicEncryptionKey) {
	s.client.SetPublicEncryptionKey(key)
}
