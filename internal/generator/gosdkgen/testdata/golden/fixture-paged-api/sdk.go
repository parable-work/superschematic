package sdk

import (
	"example.com/schemas/sdk/go/fixture-paged-api/namespaces"
	"fmt"
)

// FixturePagedApiSDK is the root client for fixture-paged-api endpoints.
type FixturePagedApiSDK struct {
	client *HTTPClient
	// CatalogNamespace exposes catalog namespace operations.
	//
	// Endpoints:
	// - GetItem: One item.
	// - ListItems: The items, two to a page unless the caller asks for up to five, by their ids or their stock, in order of their names unless the caller sorts them by name or sku, either way.
	// - ListTags: The tags items carry: a page of a primitive, with the default limits.
	// - PlaceBasket: Place a basket.
	CatalogNamespace *namespaces.CatalogNamespace
}

// New creates a new FixturePagedApiSDK.
func New(config SDKConfig) (*FixturePagedApiSDK, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	client, err := NewHTTPClient(config)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	sdk := &FixturePagedApiSDK{
		client:           client,
		CatalogNamespace: namespaces.NewCatalogNamespace(client),
	}
	return sdk, nil
}

// SetPublicEncryptionKey sets the default key for encrypted requests.
func (s *FixturePagedApiSDK) SetPublicEncryptionKey(key *PublicEncryptionKey) {
	s.client.SetPublicEncryptionKey(key)
}
