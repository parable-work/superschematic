// Package shopreviews implements the shop-reviews API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.
package shopreviews

import (
	"context"

	api "example.com/schemas/api/shop-reviews"
	types "example.com/schemas/types/go/shop-reviews"
)

// New builds the implementation of shop-reviews from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{
		Review: &Review{deps: deps},
	}, nil
}

var _ api.Constructor = New

// Review implements api.ReviewImplementation.
type Review struct {
	deps api.Deps
}

// ListReviews handles GET /api/products/{productId}/reviews.
func (impl *Review) ListReviews(ctx context.Context, productId string) ([]types.ReviewView, error) {
	return nil, api.NotImplementedError("Review.ListReviews")
}
