package serviceauth

import (
	"context"
	"errors"
	"net/http"
	"slices"
)

// serviceStepKey holds the outcome of the service step on a request.
type serviceStepKey struct{}

// serviceStep is what the service step established: that a service
// authenticator ran, and the caller, if any.
type serviceStep struct {
	caller *Caller
}

// WithCaller returns a context carrying c as the request's service caller,
// as Authenticate puts it there. It also marks the service step as run, so
// Require and AllowOr read it; a handler test uses it to stand in for the
// step.
func WithCaller(ctx context.Context, c *Caller) context.Context {
	return context.WithValue(ctx, serviceStepKey{}, serviceStep{caller: c})
}

// CallerFromContext returns the request's service caller, and false when
// the request has none: no service credential, or no service
// authenticator.
func CallerFromContext(ctx context.Context) (*Caller, bool) {
	step, _ := ctx.Value(serviceStepKey{}).(serviceStep)
	return step.caller, step.caller != nil
}

// stepRan reports whether a service authenticator ran on the request.
func stepRan(ctx context.Context) bool {
	_, ok := ctx.Value(serviceStepKey{}).(serviceStep)
	return ok
}

// Authenticate is the service step every route of a generated server runs,
// after the rate limit and the body limit. It stores the request's end-user
// bearer token for ForwardedToken, as CaptureAuthorization does, then runs
// a: a refusal answers at once (WriteError), and a caller is put on the
// request for CallerFromContext, Require and AllowOr. A route without a
// service clause thus still verifies a credential that is present and
// tells a delegated call from a direct one.
//
// With a nil a the server has no service authenticator: the
// Service-Authorization header is ignored, no caller is put on the request,
// and Require and AllowOr answer 401 service_unauthorized.
func Authenticate(a Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(captureAuthorization(r.Context(), r.Header))
			if a == nil {
				next.ServeHTTP(w, r)
				return
			}
			caller, err := a.Authenticate(r)
			if err != nil {
				WriteError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), caller)))
		})
	}
}

// Listed reports whether c is a caller a route's from admits: any caller
// when from is empty, otherwise one that serves an API from lists.
func Listed(c *Caller, from []string) bool {
	if c == nil {
		return false
	}
	if len(from) == 0 {
		return true
	}
	for _, api := range c.Serves {
		if slices.Contains(from, api) {
			return true
		}
	}
	return false
}

// Require is the service clause of an @requireService route, after
// Authenticate. A request with no caller is 401 service_unauthorized
// ("Service credential required"); a caller from does not list is 403
// service_forbidden. An empty from lists every caller the config admits. A
// route that also has a user clause puts its end-user middlewares after
// Require, so a listed caller must forward an end user who meets it:
//
//	serviceauth.Authenticate(cfg.ServiceAuthenticator),
//	serviceauth.Require("shop-orders"),
//	cfg.AuthMiddleware,
//	runtimesession.RequirePermissions("orders.create"),
func Require(from ...string) func(http.Handler) http.Handler {
	from = slices.Clone(from)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller, ok := CallerFromContext(r.Context())
			if !ok {
				WriteError(w, r, Missing())
				return
			}
			if !Listed(caller, from) {
				WriteError(w, r, Forbidden(errors.New("route does not list caller "+caller.Deployable)))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AllowOr is the service clause of an @allowService route, after
// Authenticate, with the route's end-user middlewares as user. A caller
// from lists (any caller when from is empty) stands in for the end user:
// the request goes straight to next, and user does not run. Any other
// request, with an unlisted caller or none, runs user and then next, so the
// end user must meet the user clause:
//
//	serviceauth.Authenticate(cfg.ServiceAuthenticator),
//	serviceauth.AllowOr([]string{"shop-orders"},
//		cfg.AuthMiddleware,
//		runtimesession.RequirePermissions("stock.write")),
//
// Nil user middlewares are skipped. Without a service authenticator
// (Authenticate(nil), or no Authenticate) the route answers 401
// service_unauthorized, as the TypeScript router does.
func AllowOr(from []string, user ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	from = slices.Clone(from)
	user = slices.Clone(user)
	return func(next http.Handler) http.Handler {
		userChain := next
		for i := len(user) - 1; i >= 0; i-- {
			if user[i] != nil {
				userChain = user[i](userChain)
			}
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if !stepRan(ctx) {
				WriteError(w, r, Missing())
				return
			}
			if caller, ok := CallerFromContext(ctx); ok && Listed(caller, from) {
				next.ServeHTTP(w, r)
				return
			}
			userChain.ServeHTTP(w, r)
		})
	}
}
