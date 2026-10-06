package serviceauth

import (
	"context"
	"net/http"
)

// forwardedTokenKey holds the end-user bearer token of the request being
// served.
type forwardedTokenKey struct{}

// CaptureAuthorization stores the request's end-user bearer token, from
// "Authorization: Bearer <token>", on its context for ForwardedToken.
// Authenticate does the same, so a route that runs it needs no
// CaptureAuthorization.
func CaptureAuthorization(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(captureAuthorization(r.Context(), r.Header)))
	})
}

func captureAuthorization(ctx context.Context, h http.Header) context.Context {
	token, _ := parseBearer(h.Get("Authorization"))
	return WithForwardedToken(ctx, token)
}

// WithForwardedToken returns a context whose ForwardedToken is token, for a
// handler test or a task that acts for a known user.
func WithForwardedToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, forwardedTokenKey{}, token)
}

// ForwardedToken returns the end-user bearer token of the request ctx
// belongs to, or "" when it carried none. Its error is always nil; the
// signature is the Go SDK's TokenProvider, so a server's client for a
// calls edge forwards the end user per call with
//
//	Auth: &shopapi.AuthConfig{GetToken: serviceauth.ForwardedToken}
//
// and a handler forwards by passing its request's context to the call.
// The token is forwarded unchanged; the callee's end-user provider decides
// whether it is good.
func ForwardedToken(ctx context.Context) (string, error) {
	token, _ := ctx.Value(forwardedTokenKey{}).(string)
	return token, nil
}
