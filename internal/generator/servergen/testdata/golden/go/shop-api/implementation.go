// Package shopapi implements the shop-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.
package shopapi

import (
	"context"
	"net/http"

	api "example.com/schemas/api/shop-api"
	types "example.com/schemas/types/go/shop-api"
)

// New builds the implementation of shop-api from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{
		Product: &Product{deps: deps},
	}, nil
}

var _ api.Constructor = New

// AuthMiddleware authenticates the end user on the protected routes of
// shop-api. The server's entrypoint passes it to the generated
// Config.AuthMiddleware. The scaffold's refuses every request with 401:
// verify the request's credential with your identity provider, put the
// principal on the request context as your auth provider's routes read it
// (with the session provider, api.ContextWithPrincipalID and
// api.ContextWithRoles), then call next.
func AuthMiddleware(deps api.Deps) (func(http.Handler) http.Handler, error) {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			api.RespondError(w, r, http.StatusUnauthorized, "Authentication required")
		})
	}, nil
}

// Product implements api.ProductImplementation.
type Product struct {
	deps api.Deps
}

// GetProduct handles GET /api/products/{id}.
func (impl *Product) GetProduct(ctx context.Context, id string) (*types.ProductView, error) {
	return nil, api.NotImplementedError("Product.GetProduct")
}

// RenameProduct handles PUT /api/products/{id}/name.
func (impl *Product) RenameProduct(ctx context.Context, id string, name string) (*types.ProductView, error) {
	return nil, api.NotImplementedError("Product.RenameProduct")
}
