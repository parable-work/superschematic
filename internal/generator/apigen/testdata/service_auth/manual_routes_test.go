// This file runs inside the generated API module of
// fixture-service-auth-api generated as a public API over fixture-db, with
// reserveStock and getReservation declared @manualRouteRegistration
// (TestServiceAuthManualRoutes copies it there). RegisterRoutes mounts each
// with the handler Config.ManualRoutes holds (D51), behind the steps it
// mounts a route it implements behind: getReservation, which needs a user
// and has no service clause, in a protected group behind the
// AuthMiddleware, and reserveStock behind its service clause, then its user
// step. Without Config.ManualRoutes it mounts neither.
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

// manualDatabase stands in for the ORM; no route here reads it.
type manualDatabase struct{ orm.DatabaseInterface }

// serveManual serves the API with manual as its Config.ManualRoutes, and
// counts the AuthMiddleware's runs.
func serveManual(t *testing.T, rec *recorder, manual *api.ManualRoutes) (string, *atomic.Int32) {
	t.Helper()
	authRuns := &atomic.Int32{}
	router := chi.NewRouter()
	err := api.RegisterRoutes(router, api.Config{
		DB:     manualDatabase{},
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
		ManualRoutes:         manual,
		Implementations:      api.Implementations{Ledger: rec, Stock: rec, Sync: rec},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server.URL, authRuns
}

func TestManualRoutesRunTheirRoutesSteps(t *testing.T) {
	rec := &recorder{}
	// The manual handler answers 202, which no generated handler does, so
	// a 202 is its answer.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Context())
		w.WriteHeader(http.StatusAccepted)
	})
	base, authRuns := serveManual(t, rec, &api.ManualRoutes{StockReserveStock: handler, StockGetReservation: handler})
	orders, billing := serviceToken(t, "orders"), serviceToken(t, "billing")
	const reserver = "user:stock.reserve"

	type authCheck struct {
		check
		authRan bool
	}
	for _, c := range []authCheck{
		{check{"protected: no user is refused by the group's AuthMiddleware", "GET", "/api/stock/reservations/r1", credentials{service: orders}, 401, "unauthorized", nil}, true},
		{check{"protected: a user reaches the handler", "GET", "/api/stock/reservations/r1", credentials{service: orders, user: reserver}, 202, "",
			&seen{caller: "orders", principal: reserver, forwarded: reserver}}, true},
		{check{"protected: a credential that does not verify is refused first", "GET", "/api/stock/reservations/r1", credentials{service: orders + "x", user: reserver}, 401, serviceauth.CodeUnauthorized, nil}, true},
		{check{"require: an unlisted caller is refused before the user is read", "POST", "/api/stock/reservations", credentials{service: billing, user: reserver}, 403, serviceauth.CodeForbidden, nil}, false},
		{check{"require: a listed caller forwarding no user", "POST", "/api/stock/reservations", credentials{service: orders}, 401, "unauthorized", nil}, true},
		{check{"require: a listed caller forwarding a user who may", "POST", "/api/stock/reservations", credentials{service: orders, user: reserver}, 202, "",
			&seen{caller: "orders", principal: reserver, forwarded: reserver}}, true},
	} {
		before := authRuns.Load()
		run(t, base, rec, []check{c.check})
		if ran := authRuns.Load() > before; ran != c.authRan {
			t.Errorf("%s: AuthMiddleware ran = %v, want %v", c.name, ran, c.authRan)
		}
	}

	// A nil handler answers 501 behind the same steps.
	base, _ = serveManual(t, rec, &api.ManualRoutes{})
	run(t, base, rec, []check{
		{"a nil handler answers 501", "GET", "/api/stock/reservations/r1", credentials{service: orders, user: reserver}, 501, "", nil},
		{"a nil handler is still behind the AuthMiddleware", "GET", "/api/stock/reservations/r1", credentials{service: orders}, 401, "unauthorized", nil},
	})

	// Without Config.ManualRoutes, RegisterRoutes leaves both routes to the
	// service.
	base, _ = serveManual(t, rec, nil)
	for _, route := range [][2]string{{"GET", "/api/stock/reservations/r1"}, {"POST", "/api/stock/reservations"}} {
		rec.take()
		status, _ := do(t, base, route[0], route[1], credentials{service: orders, user: reserver}, nil)
		if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
			t.Errorf("%s %s without Config.ManualRoutes = %d, want it unmounted", route[0], route[1], status)
		}
		if got := rec.take(); got != nil {
			t.Errorf("%s %s without Config.ManualRoutes ran a handler: %+v", route[0], route[1], *got)
		}
	}
}
