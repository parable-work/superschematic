// This file runs inside the generated API module of
// fixture-service-auth-api generated as a public API over fixture-db
// (TestServiceAuthPublicRoutes copies it there). The protected group runs
// the provider's AuthMiddleware; a route with a service clause runs it
// inside its own chain, after the service step, and an @allowService route
// skips it for a listed caller.
package fixtureserviceauthapi_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/parable-work/superschematic/runtime/http/go/response"
	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-service-auth-api"
	orm "example.com/schemas/orm/fixture-db"
)

// database stands in for the ORM; no route here reads it.
type database struct{ orm.DatabaseInterface }

func servePublic(t *testing.T) (string, *recorder, *atomic.Int32) {
	t.Helper()
	rec := &recorder{}
	authRuns := &atomic.Int32{}
	err := api.RegisterRoutes(chi.NewRouter(), api.Config{})
	if err == nil {
		t.Fatal("an empty Config must not register")
	}
	router := chi.NewRouter()
	err = api.RegisterRoutes(router, api.Config{
		DB:     database{},
		Logger: zap.NewNop(),
		AuthMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authRuns.Add(1)
				ctx, ok := userFrom(r)
				if !ok {
					response.Error(w, http.StatusUnauthorized, "Authentication required", "unauthorized")
					return
				}
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		},
		ServiceAuthenticator: authenticator(t),
		Implementations:      api.Implementations{Ledger: rec, Stock: rec, Sync: rec},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server.URL, rec, authRuns
}

func TestPublicAPIServiceClauses(t *testing.T) {
	base, rec, authRuns := servePublic(t)
	orders, billing := serviceToken(t, "orders"), serviceToken(t, "billing")
	const reserver, releaser = "user:stock.reserve", "user:stock.write"

	// Each check states whether the AuthMiddleware ran.
	type authCheck struct {
		check
		authRan bool
	}
	for _, c := range []authCheck{
		{check{"require: an unlisted caller is refused before the user is read", "POST", "/api/stock/reservations", credentials{service: billing, user: reserver}, 403, serviceauth.CodeForbidden, nil}, false},
		{check{"require: no credential is refused before the user is read", "POST", "/api/stock/reservations", credentials{user: reserver}, 401, serviceauth.CodeUnauthorized, nil}, false},
		{check{"require: a listed caller forwarding no user", "POST", "/api/stock/reservations", credentials{service: orders}, 401, "unauthorized", nil}, true},
		{check{"require: a listed caller forwarding a user who may", "POST", "/api/stock/reservations", credentials{service: orders, user: reserver}, 200, "",
			&seen{caller: "orders", principal: reserver, forwarded: reserver}}, true},
		{check{"require alone: no user is read", "POST", "/api/stock/reindex", credentials{service: billing, user: reserver}, 200, "",
			&seen{caller: "billing", forwarded: reserver}}, false},
		{check{"allow: a listed caller skips the AuthMiddleware", "POST", "/api/stock/reservations/r1/release", credentials{service: orders}, 200, "", &seen{caller: "orders"}}, false},
		{check{"allow: an unlisted caller goes through it", "POST", "/api/stock/reservations/r1/release", credentials{service: billing}, 401, "unauthorized", nil}, true},
		{check{"allow: an unlisted caller forwarding a user who may", "POST", "/api/stock/reservations/r1/release", credentials{service: billing, user: releaser}, 200, "",
			&seen{caller: "billing", principal: releaser, forwarded: releaser}}, true},
		{check{"allow: a user who may", "POST", "/api/stock/reservations/r1/release", credentials{user: releaser}, 200, "", &seen{principal: releaser, forwarded: releaser}}, true},
		{check{"allow with @auth: any configured caller", "POST", "/api/sync/stock/mine", credentials{service: billing}, 200, "", &seen{caller: "billing"}}, false},
		{check{"allow with @auth: a user", "POST", "/api/sync/stock/mine", credentials{user: reserver}, 200, "", &seen{principal: reserver, forwarded: reserver}}, true},
		{check{"allow with @auth: neither", "POST", "/api/sync/stock/mine", credentials{}, 401, "unauthorized", nil}, true},
		{check{"allow from an Authenticated set: a listed caller", "GET", "/api/ledger/reservations", credentials{service: orders}, 200, "", &seen{caller: "orders"}}, false},
		{check{"no clause, protected: the group authenticates the user first", "GET", "/api/stock/reservations/r1", credentials{service: orders + "x", user: reserver}, 401, serviceauth.CodeUnauthorized, nil}, true},
		{check{"no clause, protected: a caller is reported", "GET", "/api/stock/reservations/r1", credentials{service: orders, user: reserver}, 200, "",
			&seen{caller: "orders", principal: reserver, forwarded: reserver}}, true},
		{check{"no clause, @publicRoute: a credential that does not verify", "GET", "/api/sync/status", credentials{service: "forged"}, 401, serviceauth.CodeUnauthorized, nil}, false},
		{check{"no clause, @publicRoute: no credential", "GET", "/api/sync/status", credentials{}, 200, "", &seen{}}, false},
	} {
		before := authRuns.Load()
		run(t, base, rec, []check{c.check})
		if ran := authRuns.Load() > before; ran != c.authRan {
			t.Errorf("%s: AuthMiddleware ran = %v, want %v", c.name, ran, c.authRan)
		}
	}
}
