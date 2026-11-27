package sdk

import (
	"example.com/schemas/sdk/go/fixture-variant-field-api/namespaces"
	"fmt"
)

// FixtureVariantFieldApiSDK is the root client for fixture-variant-field-api endpoints.
type FixtureVariantFieldApiSDK struct {
	client *HTTPClient
	// StepsNamespace exposes steps namespace operations.
	//
	// Endpoints:
	// - CreateStep: Records a step. Requires authentication.
	// - GetStep: One step. Requires authentication.
	StepsNamespace *namespaces.StepsNamespace
}

// New creates a new FixtureVariantFieldApiSDK.
func New(config SDKConfig) (*FixtureVariantFieldApiSDK, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	client, err := NewHTTPClient(config)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	sdk := &FixtureVariantFieldApiSDK{
		client:         client,
		StepsNamespace: namespaces.NewStepsNamespace(client),
	}
	return sdk, nil
}

// SetToken sets the static auth token used by the SDK client.
func (s *FixtureVariantFieldApiSDK) SetToken(token string) {
	s.client.SetToken(token)
}

// ClearToken removes the static auth token used by the SDK client.
func (s *FixtureVariantFieldApiSDK) ClearToken() {
	s.client.ClearToken()
}

// SetPublicEncryptionKey sets the default key for encrypted requests.
func (s *FixtureVariantFieldApiSDK) SetPublicEncryptionKey(key *PublicEncryptionKey) {
	s.client.SetPublicEncryptionKey(key)
}
