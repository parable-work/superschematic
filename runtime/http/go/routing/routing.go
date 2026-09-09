package routing

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Middleware matches standard net/http middleware signatures.
type Middleware = func(http.Handler) http.Handler

// Route describes a generated route plus any route-specific middleware.
type Route struct {
	Method      string
	Path        string
	Handler     http.Handler
	Middlewares []Middleware
}

// Register mounts each route onto the provided router.
func Register(router chi.Router, routes []Route) {
	for _, route := range routes {
		registerRoute(router, route)
	}
}

// RegisterGroup applies shared group middleware before mounting routes.
func RegisterGroup(router chi.Router, routes []Route, middlewares ...Middleware) {
	if len(routes) == 0 {
		return
	}

	router.Group(func(group chi.Router) {
		for _, middleware := range middlewares {
			if middleware != nil {
				group.Use(middleware)
			}
		}
		Register(group, routes)
	})
}

func registerRoute(router chi.Router, route Route) {
	if len(route.Middlewares) == 0 {
		router.Method(route.Method, route.Path, route.Handler)
		return
	}

	router.With(compactMiddlewares(route.Middlewares)...).Method(route.Method, route.Path, route.Handler)
}

func compactMiddlewares(middlewares []Middleware) []func(http.Handler) http.Handler {
	compacted := make([]func(http.Handler) http.Handler, 0, len(middlewares))
	for _, middleware := range middlewares {
		if middleware != nil {
			compacted = append(compacted, middleware)
		}
	}
	return compacted
}
