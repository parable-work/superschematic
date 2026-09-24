package sdk

import (
	"example.com/schemas/sdk/go/fixture-nested-arrays-api/namespaces"
	"fmt"
)

// FixtureNestedArraysApiSDK is the root client for fixture-nested-arrays-api endpoints.
type FixtureNestedArraysApiSDK struct {
	client *HTTPClient
	// GridNamespace exposes grid namespace operations.
	//
	// Endpoints:
	// - GetGrid: GetGrid endpoint.
	// - GridLabels: One grid's labels as a bare list of lists, at most `limit` rows.
	// - ReplaceLabels: Replace a grid's labels; the body argument is a list of lists.
	// - SaveGrid: Store a grid from a request body.
	GridNamespace *namespaces.GridNamespace
}

// New creates a new FixtureNestedArraysApiSDK.
func New(config SDKConfig) (*FixtureNestedArraysApiSDK, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	client, err := NewHTTPClient(config)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	sdk := &FixtureNestedArraysApiSDK{
		client:        client,
		GridNamespace: namespaces.NewGridNamespace(client),
	}
	return sdk, nil
}

// SetPublicEncryptionKey sets the default key for encrypted requests.
func (s *FixtureNestedArraysApiSDK) SetPublicEncryptionKey(key *PublicEncryptionKey) {
	s.client.SetPublicEncryptionKey(key)
}
