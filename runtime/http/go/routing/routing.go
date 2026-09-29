package routing

import (
	"errors"
	"net/http"
	"net/url"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

// ErrPathParamEncoding reports a path parameter that is not percent-encoded
// UTF-8.
var ErrPathParamEncoding = errors.New("path parameter is not percent-encoded UTF-8")

// PathParam returns the named path parameter of r, percent-decoded once.
//
// chi matches a route against r.URL.RawPath when net/url keeps one, that is
// when the request did not encode its path the way Go would (%41 for A, a
// %2F inside a segment, lowercase hex), and its captures are then still
// encoded. Otherwise it matches against r.URL.Path, whose captures are
// already decoded. PathParam decodes the first and not the second, so a
// value arrives the same however the client encoded it. A value that does
// not decode, or is not UTF-8 once decoded, is ErrPathParamEncoding.
func PathParam(r *http.Request, name string) (string, error) {
	value := chi.URLParam(r, name)
	if r.URL.RawPath != "" {
		decoded, err := url.PathUnescape(value)
		if err != nil {
			return "", ErrPathParamEncoding
		}
		value = decoded
	}
	if !utf8.ValidString(value) {
		return "", ErrPathParamEncoding
	}
	return value, nil
}

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
