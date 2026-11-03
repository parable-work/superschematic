package sdk

import (
	"example.com/schemas/sdk/go/fixture-workflow-api/namespaces"
	"fmt"
)

// FixtureWorkflowApiSDK is the root client for fixture-workflow-api endpoints.
type FixtureWorkflowApiSDK struct {
	client *HTTPClient
	// OrderNamespace exposes order namespace operations.
	//
	// Endpoints:
	// - GetOrder: GetOrder endpoint. Requires authentication.
	// - Status.Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
	OrderNamespace *namespaces.OrderNamespace
	// ShipmentNamespace exposes shipment namespace operations.
	//
	// Endpoints:
	// - PaymentStatus.Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
	// - Status.Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
	ShipmentNamespace *namespaces.ShipmentNamespace
}

// New creates a new FixtureWorkflowApiSDK.
func New(config SDKConfig) (*FixtureWorkflowApiSDK, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	client, err := NewHTTPClient(config)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	sdk := &FixtureWorkflowApiSDK{
		client:            client,
		OrderNamespace:    namespaces.NewOrderNamespace(client),
		ShipmentNamespace: namespaces.NewShipmentNamespace(client),
	}
	return sdk, nil
}

// SetToken sets the static auth token used by the SDK client.
func (s *FixtureWorkflowApiSDK) SetToken(token string) {
	s.client.SetToken(token)
}

// ClearToken removes the static auth token used by the SDK client.
func (s *FixtureWorkflowApiSDK) ClearToken() {
	s.client.ClearToken()
}

// SetPublicEncryptionKey sets the default key for encrypted requests.
func (s *FixtureWorkflowApiSDK) SetPublicEncryptionKey(key *PublicEncryptionKey) {
	s.client.SetPublicEncryptionKey(key)
}
