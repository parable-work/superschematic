// This file runs inside the generated API module of
// fixture-service-auth-api generated as an API that is not public
// (TestServiceAuthRoutes copies it there), with a webhook verifier on
// sync/status, a rate limit on stock/reindex and a body limit on
// stock/reservations/{id}/release. Such an API has no AuthMiddleware: the
// service's own middleware puts the end user on the context, and a route's
// end-user step is its permission check.
package fixtureserviceauthapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-service-auth-api"
)

// signatureHeader stands in for a webhook provider's signature.
const signatureHeader = "X-Test-Signature"

func serve(t *testing.T) (string, *recorder) {
	t.Helper()
	rec := &recorder{}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, _ := userFrom(r)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	err := api.RegisterRoutes(router, api.Config{
		Logger:               zap.NewNop(),
		ServiceAuthenticator: authenticator(t),
		Implementations: api.Implementations{
			Ledger: rec,
			Stock:  rec,
			Sync:   rec,
			WebhookVerifiers: map[string]api.WebhookVerifier{
				"stripe": func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get(signatureHeader) != "valid" {
							http.Error(w, "bad signature", http.StatusUnauthorized)
							return
						}
						next.ServeHTTP(w, r)
					})
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server.URL, rec
}

func TestRegisterRoutesRefusesANilServiceAuthenticator(t *testing.T) {
	rec := &recorder{}
	err := api.RegisterRoutes(chi.NewRouter(), api.Config{
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Ledger: rec, Stock: rec, Sync: rec},
	})
	if err == nil || !strings.Contains(err.Error(), "Config.ServiceAuthenticator is required") {
		t.Fatalf("RegisterRoutes error = %v", err)
	}
}

func TestServiceClauses(t *testing.T) {
	base, rec := serve(t)
	orders, billing := serviceToken(t, "orders"), serviceToken(t, "billing")
	const reserver, releaser = "user:stock.reserve", "user:stock.write"
	run(t, base, rec, []check{
		// @requireService({ from }) with @requirePermission: a listed
		// service forwarding a user who may.
		{"require: no credential", "POST", "/api/stock/reservations", credentials{user: reserver}, 401, serviceauth.CodeUnauthorized, nil},
		{"require: an unlisted caller", "POST", "/api/stock/reservations", credentials{service: billing, user: reserver}, 403, serviceauth.CodeForbidden, nil},
		{"require: a listed caller forwarding no user", "POST", "/api/stock/reservations", credentials{service: orders}, 401, "", nil},
		{"require: a listed caller forwarding a user without the permission", "POST", "/api/stock/reservations", credentials{service: orders, user: releaser}, 403, "", nil},
		{"require: a listed caller forwarding a user who may", "POST", "/api/stock/reservations", credentials{service: orders, user: reserver}, 200, "",
			&seen{caller: "orders", principal: reserver, forwarded: reserver}},
		{"require: a credential that does not verify", "POST", "/api/stock/reservations", credentials{service: orders + "x", user: reserver}, 401, serviceauth.CodeUnauthorized, nil},

		// @requireService alone, from the set: no end user is looked at.
		{"set require: a listed caller", "POST", "/api/sync/stock", credentials{service: orders}, 200, "", &seen{caller: "orders"}},
		{"set require: an end user alone", "POST", "/api/sync/stock", credentials{user: reserver}, 401, serviceauth.CodeUnauthorized, nil},

		// @allowService({ from }) with @requirePermission.
		{"allow: a listed caller stands in for the user", "POST", "/api/stock/reservations/r1/release", credentials{service: orders}, 200, "", &seen{caller: "orders"}},
		{"allow: a listed caller skips the permission", "POST", "/api/stock/reservations/r1/release", credentials{service: orders, user: reserver}, 200, "",
			&seen{caller: "orders", principal: reserver, forwarded: reserver}},
		{"allow: an unlisted caller goes through the permission", "POST", "/api/stock/reservations/r1/release", credentials{service: billing}, 401, "", nil},
		{"allow: an unlisted caller forwarding a user who may", "POST", "/api/stock/reservations/r1/release", credentials{service: billing, user: releaser}, 200, "",
			&seen{caller: "billing", principal: releaser, forwarded: releaser}},
		{"allow: a user who may", "POST", "/api/stock/reservations/r1/release", credentials{user: releaser}, 200, "", &seen{principal: releaser, forwarded: releaser}},
		{"allow: a user who may not", "POST", "/api/stock/reservations/r1/release", credentials{user: reserver}, 403, "", nil},

		// @allowService() with @auth: an API that is not public checks no
		// @auth (D15, amended), so the route admits any request.
		{"allow with @auth only: anyone, on an API that is not public", "POST", "/api/sync/stock/mine", credentials{}, 200, "", &seen{}},
		{"allow with @auth only: any configured caller", "POST", "/api/sync/stock/mine", credentials{service: billing}, 200, "", &seen{caller: "billing"}},

		// No service clause: the credential is still verified.
		{"no clause: a caller is reported", "GET", "/api/stock/reservations/r1", credentials{service: billing, user: reserver}, 200, "",
			&seen{caller: "billing", principal: reserver, forwarded: reserver}},
		{"no clause: a credential that does not verify", "GET", "/api/stock/reservations/r1", credentials{service: billing + "x"}, 401, serviceauth.CodeUnauthorized, nil},
	})
}

// TestTheServiceStepRunsAfterTheVerifierAndTheLimits: the webhook verifier,
// the rate limit and the body limit each refuse before the service step
// looks at a credential.
func TestTheServiceStepRunsAfterTheVerifierAndTheLimits(t *testing.T) {
	base, rec := serve(t)
	orders := serviceToken(t, "orders")
	signed := map[string]string{signatureHeader: "valid"}
	run(t, base, rec, []check{
		{"verifier: a bad signature and a bad credential", "GET", "/api/sync/status", credentials{service: "forged"}, 401, "", nil},
		{"verifier: a good signature and a bad credential", "GET", "/api/sync/status", credentials{service: "forged", extra: signed}, 401, serviceauth.CodeUnauthorized, nil},
		{"verifier: a good signature and no credential", "GET", "/api/sync/status", credentials{extra: signed}, 200, "", &seen{}},
		// One request a minute: the first takes the token, the second is
		// refused before its good credential is read.
		{"rate limit: the first request, a bad credential", "POST", "/api/stock/reindex", credentials{service: "forged"}, 401, serviceauth.CodeUnauthorized, nil},
		{"rate limit: the second request, a good credential", "POST", "/api/stock/reindex", credentials{service: orders}, 429, "too_many_requests", nil},
	})

	status, code := do(t, base, "POST", "/api/stock/reservations/r1/release", credentials{service: "forged"}, strings.NewReader(strings.Repeat(" ", 1<<20+1)))
	if status != http.StatusRequestEntityTooLarge || code == serviceauth.CodeUnauthorized {
		t.Errorf("body limit: got %d %q, want 413", status, code)
	}
}
