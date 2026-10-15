// Package fixturepagedapi implements the fixture-paged-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.
package fixturepagedapi

import (
	"context"

	"github.com/parable-work/superschematic/runtime/http/go/paging"

	api "example.com/schemas/api/fixture-paged-api"
	types "example.com/schemas/types/go/fixture-paged-api"
)

// New builds the implementation of fixture-paged-api from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{
		Catalog: &Catalog{deps: deps},
	}, nil
}

var _ api.Constructor = New

// Catalog implements api.CatalogImplementation.
type Catalog struct {
	deps api.Deps
}

// PlaceBasket handles POST /api/baskets.
func (impl *Catalog) PlaceBasket(ctx context.Context, input *types.BasketInput) (*types.BasketView, error) {
	return nil, api.NotImplementedError("Catalog.PlaceBasket")
}

// ListItems handles GET /api/items.
func (impl *Catalog) ListItems(ctx context.Context, ids []types.IdentityUUID, inStock *bool, page paging.Request) (paging.Page[types.ItemView], error) {
	return paging.Page[types.ItemView]{}, api.NotImplementedError("Catalog.ListItems")
}

// GetItem handles GET /api/items/{itemId}.
func (impl *Catalog) GetItem(ctx context.Context, itemId types.IdentityUUID) (*types.ItemView, error) {
	return nil, api.NotImplementedError("Catalog.GetItem")
}

// ListTags handles GET /api/tags.
func (impl *Catalog) ListTags(ctx context.Context, page paging.Request) (paging.Page[string], error) {
	return paging.Page[string]{}, api.NotImplementedError("Catalog.ListTags")
}
