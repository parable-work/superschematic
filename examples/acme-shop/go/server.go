package shop

import (
	"net/http"

	shopapi "example.com/acme/api/shop-api"
	orm "example.com/acme/orm/shop-db"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

// NewHandler mounts the generated shop-api routes, backed by this package's
// implementations, on a chi router.
func NewHandler(db orm.DatabaseInterface, logger *zap.Logger, auth Auth) (http.Handler, error) {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	err := shopapi.RegisterRoutes(r, shopapi.Config{
		DB:             db,
		Logger:         logger,
		AuthMiddleware: auth.Middleware,
		Implementations: shopapi.Implementations{
			Product: &Products{DB: db},
		},
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}
