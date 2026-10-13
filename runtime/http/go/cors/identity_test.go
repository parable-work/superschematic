package cors

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/parable-work/superschematic/runtime/http/go/identity"
)

// TestHandlerLeavesTheIdentityRuntimeItsTrustedOrigins: an API that a
// site calls and whose identity config trusts another origin answers
// both, as a generated server wires them (D55, amended): Handler around
// the server, and the identity runtime's CORS on the API's router. Handler
// answers the site's preflight with its policy, and passes on the trusted
// origin's, which the identity runtime answers with the method and headers
// it asks for. An origin neither lists gets no Access-Control-Allow-Origin
// from either: its preflight reaches the router, which allows no OPTIONS.
func TestHandlerLeavesTheIdentityRuntimeItsTrustedOrigins(t *testing.T) {
	const site, trusted, other = "https://shop-web.acme.dev", "https://admin.acme.dev", "https://evil.example"
	router := chi.NewRouter()
	router.Use(identity.CORS([]string{trusted}))
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	router.Get("/api/products", ok)
	router.Post("/api/auth/login", ok)
	h := Handler(router, []API{{
		Policy: Policy{Origins: []string{site}, Methods: []string{"GET", "POST"}},
		Match:  func(method, path string) bool { return router.Match(chi.NewRouteContext(), method, path) },
	}})

	for _, tc := range []struct {
		name, method, path, origin, ask string
		status                          int
		want                            map[string]string
	}{
		{"the site's preflight", "OPTIONS", "/api/auth/login", site, "POST", http.StatusNoContent, map[string]string{
			"Access-Control-Allow-Origin": site, "Access-Control-Allow-Credentials": "true",
			"Access-Control-Allow-Methods": "GET, POST", "Access-Control-Allow-Headers": "Accept, Authorization, Content-Type",
		}},
		{"the trusted origin's preflight", "OPTIONS", "/api/auth/login", trusted, "POST", http.StatusNoContent, map[string]string{
			"Access-Control-Allow-Origin": trusted, "Access-Control-Allow-Credentials": "true",
			"Access-Control-Allow-Methods": "POST", "Access-Control-Allow-Headers": "content-type",
		}},
		{"the site's request", "GET", "/api/products", site, "", http.StatusOK, map[string]string{
			"Access-Control-Allow-Origin": site, "Access-Control-Allow-Credentials": "true",
		}},
		{"the trusted origin's request", "GET", "/api/products", trusted, "", http.StatusOK, map[string]string{
			"Access-Control-Allow-Origin": trusted, "Access-Control-Allow-Credentials": "true",
		}},
		{"another origin's preflight", "OPTIONS", "/api/auth/login", other, "POST", http.StatusMethodNotAllowed, map[string]string{
			"Access-Control-Allow-Origin": "",
		}},
		{"another origin's request", "GET", "/api/products", other, "", http.StatusOK, map[string]string{
			"Access-Control-Allow-Origin": "", "Vary": "Origin",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest(tc.method, "http://shop-api.acme.dev"+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Origin", tc.origin)
			if tc.ask != "" {
				r.Header.Set("Access-Control-Request-Method", tc.ask)
				r.Header.Set("Access-Control-Request-Headers", "content-type")
			}
			w := &recorder{header: http.Header{}}
			h.ServeHTTP(w, r)
			if w.status != tc.status {
				t.Errorf("answered %d, want %d", w.status, tc.status)
			}
			for name, value := range tc.want {
				if got := w.header.Get(name); got != value {
					t.Errorf("%s = %q, want %q", name, got, value)
				}
			}
		})
	}
}
