package serviceauth_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
)

// authenticatorFunc adapts a function to serviceauth.Authenticator.
type authenticatorFunc func(*http.Request) (*serviceauth.Caller, error)

func (f authenticatorFunc) Authenticate(r *http.Request) (*serviceauth.Caller, error) { return f(r) }

// problem is the RFC 9457 body a refusal writes.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
}

func serve(t *testing.T, h http.Handler, r *http.Request) (*httptest.ResponseRecorder, problem) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var p problem
	if rec.Code != http.StatusOK {
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Fatalf("refusal content type %q", ct)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("refusal body %q: %v", rec.Body.String(), err)
		}
	}
	return rec, p
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

func TestWriteErrorRendersAProblemWithItsCode(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
		detail string
	}{
		{"missing", serviceauth.Missing(), 401, "service_unauthorized", "Service credential required"},
		{"invalid", serviceauth.Invalid(errors.New("token has expired")), 401, "service_unauthorized", "Invalid service credential"},
		{"forbidden", serviceauth.Forbidden(errors.New("sub 7 is not a caller")), 403, "service_forbidden", "Service not permitted"},
		{"unavailable", serviceauth.Unavailable(errors.New("fetch failed")), 503, "service_unavailable", "Service credential could not be checked"},
		{"wrapped", fmt.Errorf("custom authenticator: %w", serviceauth.Forbidden(nil)), 403, "service_forbidden", "Service not permitted"},
		{"any other error", errors.New("database is down"), 503, "service_unavailable", "Service credential could not be checked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serviceauth.WriteError(w, r, tc.err) })
			rec, p := serve(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != tc.status || p.Status != tc.status || p.Code != tc.code || p.Detail != tc.detail || p.Title != http.StatusText(tc.status) {
				t.Fatalf("got %d %+v, want %d %s %q", rec.Code, p, tc.status, tc.code, tc.detail)
			}
			for _, secret := range []string{"expired", "sub 7", "fetch failed", "database"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatalf("the cause %q reached the client: %s", secret, rec.Body.String())
				}
			}
		})
	}
	if msg := serviceauth.Invalid(errors.New("token has expired")).Error(); msg != "serviceauth: service_unauthorized: Invalid service credential: token has expired" {
		t.Fatalf("Error() = %q", msg)
	}
}

func TestAuthenticatePutsTheCallerOnTheRequest(t *testing.T) {
	caller := &serviceauth.Caller{Deployable: "orders", Serves: []string{"shop-orders"}, Subject: "orders"}
	var seen *serviceauth.Caller
	var seenOK bool
	h := serviceauth.Authenticate(authenticatorFunc(func(r *http.Request) (*serviceauth.Caller, error) {
		if r.Header.Get(serviceauth.HeaderName) == "" {
			return nil, nil
		}
		return caller, nil
	}))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, seenOK = serviceauth.CallerFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	if rec, _ := serve(t, h, request("token")); rec.Code != 200 || !seenOK || seen != caller {
		t.Fatalf("with a credential: %d, caller %v %v", rec.Code, seen, seenOK)
	}
	if rec, _ := serve(t, h, request("")); rec.Code != 200 || seenOK || seen != nil {
		t.Fatalf("without one: %d, caller %v %v", rec.Code, seen, seenOK)
	}
}

func TestAuthenticateAnswersARefusalAtOnce(t *testing.T) {
	reached := false
	h := serviceauth.Authenticate(authenticatorFunc(func(*http.Request) (*serviceauth.Caller, error) {
		return nil, serviceauth.Invalid(errors.New("bad signature"))
	}))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	rec, p := serve(t, h, request("token"))
	if reached || rec.Code != 401 || p.Code != serviceauth.CodeUnauthorized {
		t.Fatalf("reached %v, %d %+v", reached, rec.Code, p)
	}
}

func TestAuthenticateWithoutAnAuthenticatorIgnoresTheHeader(t *testing.T) {
	var caller *serviceauth.Caller
	var token string
	h := serviceauth.Authenticate(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, _ = serviceauth.CallerFromContext(r.Context())
		token, _ = serviceauth.ForwardedToken(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	r := request("not-even-a-jwt")
	r.Header.Set("Authorization", "Bearer user-token")
	if rec, _ := serve(t, h, r); rec.Code != 200 || caller != nil || token != "user-token" {
		t.Fatalf("%d, caller %v, forwarded %q", rec.Code, caller, token)
	}
}

func TestRequire(t *testing.T) {
	orders := &serviceauth.Caller{Deployable: "orders", Serves: []string{"shop-orders", "shop-carts"}}
	cases := []struct {
		name   string
		caller *serviceauth.Caller
		from   []string
		status int
		code   string
	}{
		{"a listed caller", orders, []string{"shop-carts"}, 200, ""},
		{"an empty from lists any caller", orders, nil, 200, ""},
		{"a caller from does not list", orders, []string{"shop-billing"}, 403, serviceauth.CodeForbidden},
		{"no caller", nil, []string{"shop-orders"}, 401, serviceauth.CodeUnauthorized},
		{"no caller, empty from", nil, nil, 401, serviceauth.CodeUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := serviceauth.Authenticate(authenticatorFunc(func(*http.Request) (*serviceauth.Caller, error) { return tc.caller, nil }))(
				serviceauth.Require(tc.from...)(okHandler))
			rec, p := serve(t, h, request(""))
			if rec.Code != tc.status || p.Code != tc.code {
				t.Fatalf("got %d %q, want %d %q", rec.Code, p.Code, tc.status, tc.code)
			}
		})
	}
	t.Run("without an authenticator", func(t *testing.T) {
		rec, p := serve(t, serviceauth.Authenticate(nil)(serviceauth.Require()(okHandler)), request("token"))
		if rec.Code != 401 || p.Detail != serviceauth.DetailRequired {
			t.Fatalf("got %d %+v", rec.Code, p)
		}
	})
}

func TestAllowOr(t *testing.T) {
	orders := &serviceauth.Caller{Deployable: "orders", Serves: []string{"shop-orders"}}
	billing := &serviceauth.Caller{Deployable: "billing", Serves: []string{"shop-billing"}}
	cases := []struct {
		name     string
		caller   *serviceauth.Caller
		from     []string
		userRuns bool
	}{
		{"a listed caller skips the user step", orders, []string{"shop-orders"}, false},
		{"an empty from lists any caller", billing, nil, false},
		{"an unlisted caller goes through the user step", billing, []string{"shop-orders"}, true},
		{"no caller goes through the user step", nil, []string{"shop-orders"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var steps []string
			step := func(name string) func(http.Handler) http.Handler {
				return func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						steps = append(steps, name)
						next.ServeHTTP(w, r)
					})
				}
			}
			h := serviceauth.Authenticate(authenticatorFunc(func(*http.Request) (*serviceauth.Caller, error) { return tc.caller, nil }))(
				serviceauth.AllowOr(tc.from, step("authenticate"), nil, step("permissions"))(okHandler))
			rec, _ := serve(t, h, request(""))
			if rec.Code != 200 {
				t.Fatalf("status %d", rec.Code)
			}
			want := ""
			if tc.userRuns {
				want = "authenticate,permissions"
			}
			if got := strings.Join(steps, ","); got != want {
				t.Fatalf("user steps run = %q, want %q", got, want)
			}
		})
	}
	t.Run("the user step refuses", func(t *testing.T) {
		deny := func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
		}
		h := serviceauth.Authenticate(authenticatorFunc(func(*http.Request) (*serviceauth.Caller, error) { return billing, nil }))(
			serviceauth.AllowOr([]string{"shop-orders"}, deny)(okHandler))
		if rec := httptest.NewRecorder(); func() int { h.ServeHTTP(rec, request("")); return rec.Code }() != 401 {
			t.Fatal("an unlisted caller must meet the user step")
		}
	})
	t.Run("without an authenticator", func(t *testing.T) {
		userRan := false
		user := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { userRan = true; next.ServeHTTP(w, r) })
		}
		for _, h := range []http.Handler{
			serviceauth.Authenticate(nil)(serviceauth.AllowOr(nil, user)(okHandler)),
			serviceauth.AllowOr(nil, user)(okHandler),
		} {
			rec, p := serve(t, h, request(""))
			if rec.Code != 401 || p.Code != serviceauth.CodeUnauthorized || userRan {
				t.Fatalf("got %d %+v, user step ran %v", rec.Code, p, userRan)
			}
		}
	})
	t.Run("WithCaller stands in for the service step", func(t *testing.T) {
		r := request("")
		r = r.WithContext(serviceauth.WithCaller(r.Context(), orders))
		if rec, _ := serve(t, serviceauth.AllowOr([]string{"shop-orders"})(okHandler), r); rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
	})
}

func TestListed(t *testing.T) {
	c := &serviceauth.Caller{Serves: []string{"a", "b"}}
	cases := []struct {
		caller *serviceauth.Caller
		from   []string
		want   bool
	}{
		{c, nil, true},
		{c, []string{"b"}, true},
		{c, []string{"c"}, false},
		{&serviceauth.Caller{}, []string{"a"}, false},
		{&serviceauth.Caller{}, nil, true},
		{nil, nil, false},
	}
	for _, tc := range cases {
		if got := serviceauth.Listed(tc.caller, tc.from); got != tc.want {
			t.Errorf("Listed(%+v, %v) = %v, want %v", tc.caller, tc.from, got, tc.want)
		}
	}
}

func TestForwardedToken(t *testing.T) {
	cases := []struct {
		authorization string
		want          string
	}{
		{"Bearer user-token", "user-token"},
		{"bearer user-token", "user-token"},
		{"", ""},
		{"Basic dXNlcjpwYXNz", ""},
		{"Bearer a b", ""},
	}
	for _, tc := range cases {
		var got string
		h := serviceauth.CaptureAuthorization(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, _ = serviceauth.ForwardedToken(r.Context())
		}))
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.authorization != "" {
			r.Header.Set("Authorization", tc.authorization)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		if got != tc.want {
			t.Errorf("Authorization %q: forwarded %q, want %q", tc.authorization, got, tc.want)
		}
	}

	if token, err := serviceauth.ForwardedToken(context.Background()); token != "" || err != nil {
		t.Fatalf("a context without a request: %q, %v", token, err)
	}
	if token, _ := serviceauth.ForwardedToken(serviceauth.WithForwardedToken(context.Background(), "task-user")); token != "task-user" {
		t.Fatalf("WithForwardedToken: %q", token)
	}
}
