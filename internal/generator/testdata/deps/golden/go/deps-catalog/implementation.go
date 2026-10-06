// Package depscatalog implements the deps-catalog API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.
package depscatalog

import (
	"context"

	api "example.com/schemas/api/deps-catalog"
	types "example.com/schemas/types/go/deps-catalog"
)

// New builds the implementation of deps-catalog from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{
		Product: &Product{deps: deps},
	}, nil
}

var _ api.Constructor = New

// Product implements api.ProductImplementation.
type Product struct {
	deps api.Deps
}

// ListProducts handles GET /api/products.
func (impl *Product) ListProducts(ctx context.Context) ([]types.Product, error) {
	return nil, api.NotImplementedError("Product.ListProducts")
}
