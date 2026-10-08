package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/runtime/http/go/identity"
	"github.com/parable-work/superschematic/runtime/http/go/routing"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// testConfig is a config at a low argon2 cost, so a test hashes quickly.
const testConfig = `{"password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}, "trustedOrigins": ["https://app.example.com"]}`

// adminPassword and userPassword are the passwords the harness's users
// sign in with.
const (
	adminPassword = "admin password"
	userPassword  = "user password"
)

// clock is a test clock the harness moves by hand.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// harness is a service over a store on one database, mounted on a chi
// router as a generated server mounts it, with an app route behind the
// identity middleware and RequirePermissions.
type harness struct {
	t      *testing.T
	store  *identity.SQLStore
	svc    *identity.Service
	clock  *clock
	router http.Handler
	// admin holds the role "admin" (identity, orders); member holds none.
	adminID, memberID string
}

// routeTable is the route requirements the harness's capabilities answers
// for.
var routeTable = []identity.Route{
	{OperationID: "OrdersList"},
	{OperationID: "AuthMe", RequiresAuth: true},
	{OperationID: "OrdersCreate", RequiresAuth: true, Permissions: []string{"orders.write"}},
	{OperationID: "IdentityListUsers", RequiresAuth: true, Permissions: []string{"identity.users.read"}},
	{OperationID: "StockSync", ServiceOnly: true},
}

func newHarness(t *testing.T, db testDB, config string) *harness {
	t.Helper()
	store, err := identity.NewSQLStore(db.db, db.dialect, descriptorJSON(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := identity.ParseConfig([]byte(config))
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	svc, err := identity.New(store, cfg, identity.WithClock(c.Now), identity.WithRoutes(routeTable))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, store: store, svc: svc, clock: c}

	r := chi.NewRouter()
	routing.Register(r, svc.Routes(&ir.UserSessionsConfig{Register: true}, &ir.UserAdministrationConfig{}))
	r.With(svc.Middleware, session.RequirePermissions("orders.write")).Post("/orders", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, session.GetPrincipalName(r.Context()))
	})
	h.router = svc.CORS()(r)

	ctx := context.Background()
	hash := func(password string) string {
		phc, err := identity.HashPassword(password, cfg.Argon2Params())
		if err != nil {
			t.Fatal(err)
		}
		return phc
	}
	admin, err := store.CreateUser(ctx, identity.NewUser{Login: "admin@example.com", Name: "Admin", PasswordHash: hash(adminPassword), At: c.Now()})
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.CreateUser(ctx, identity.NewUser{Login: "member@example.com", Name: "Member", PasswordHash: hash(userPassword), At: c.Now()})
	if err != nil {
		t.Fatal(err)
	}
	role, err := store.CreateRole(ctx, "admin", []string{"identity", "orders"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.GrantRole(ctx, admin.ID, role.ID, c.Now()); err != nil {
		t.Fatal(err)
	}
	h.adminID, h.memberID = admin.ID, member.ID
	return h
}

// eachHarness runs fn on a harness over each database.
func eachHarness(t *testing.T, config string, fn func(t *testing.T, h *harness)) {
	t.Helper()
	for _, db := range databases(t) {
		t.Run(string(db.dialect), func(t *testing.T) { fn(t, newHarness(t, db, config)) })
	}
}

// reply is a response: its status, its decoded JSON body and its cookies.
type reply struct {
	status  int
	body    map[string]any
	header  http.Header
	cookies []*http.Cookie
}

// data is the envelope's data member.
func (r reply) data() map[string]any {
	d, _ := r.body["data"].(map[string]any)
	return d
}

func (r reply) list() []any {
	l, _ := r.body["data"].([]any)
	return l
}

func (r reply) code() string {
	c, _ := r.body["code"].(string)
	return c
}

// do sends a request to the harness's router. headers are name, value
// pairs.
func (h *harness) do(method, path string, body any, headers ...string) reply {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "http://api.example.com"+path, rd)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	out := reply{status: rec.Code, header: rec.Header(), cookies: rec.Result().Cookies()}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	}
	return out
}

func (h *harness) expect(r reply, status int, code string) {
	h.t.Helper()
	if r.status != status || (code != "" && r.code() != code) {
		h.t.Fatalf("got %d %q (%v), want %d %q", r.status, r.code(), r.body, status, code)
	}
}

// login signs in with the bearer transport and returns the token.
func (h *harness) login(login, password string) string {
	h.t.Helper()
	r := h.do("POST", "/auth/login", map[string]any{"login": login, "password": password})
	h.expect(r, 200, "")
	token, _ := r.data()["token"].(string)
	if token == "" {
		h.t.Fatalf("a bearer login answered no token: %v", r.body)
	}
	return token
}

func bearer(token string) []string { return []string{"Authorization", "Bearer " + token} }

func cookieHeader(token string) []string {
	return []string{"Cookie", identity.HostCookieName + "=" + token}
}

func with(headers ...[]string) []string {
	var out []string
	for _, h := range headers {
		out = append(out, h...)
	}
	return out
}

// TestLoginBearer: a bearer login answers the user, the expiry and a token
// the routes that need a caller take; me and capabilities answer for the
// caller; every failed login is 401 invalid_credentials.
func TestLoginBearer(t *testing.T) {
	eachHarness(t, testConfig, func(t *testing.T, h *harness) {
		r := h.do("POST", "/auth/login", map[string]any{"login": "ADMIN@example.com", "password": adminPassword})
		h.expect(r, 200, "")
		user := r.data()["user"].(map[string]any)
		if user["id"] != h.adminID || user["login"] != "admin@example.com" || user["name"] != "Admin" {
			t.Errorf("login's user = %v", user)
		}
		if r.data()["expiresAt"] != "2026-10-22T12:00:00Z" {
			t.Errorf("expiresAt = %v, want 14 days on", r.data()["expiresAt"])
		}
		if len(r.cookies) != 0 {
			t.Errorf("a bearer login set cookies %v", r.cookies)
		}
		if _, ok := r.body["meta"].(map[string]any); !ok {
			t.Errorf("the answer is not the envelope: %v", r.body)
		}
		token := r.data()["token"].(string)

		me := h.do("GET", "/auth/me", nil, bearer(token)...)
		h.expect(me, 200, "")
		if got := me.data()["permissions"]; !equalJSON(got, []any{"identity", "orders"}) {
			t.Errorf("me's permissions = %v", got)
		}
		if roles := me.data()["roles"].([]any); len(roles) != 1 || roles[0].(map[string]any)["name"] != "admin" {
			t.Errorf("me's roles = %v", roles)
		}

		caps := h.do("GET", "/auth/capabilities", nil, bearer(token)...)
		h.expect(caps, 200, "")
		want := map[string]any{"OrdersList": true, "AuthMe": true, "OrdersCreate": true, "IdentityListUsers": true}
		if got := caps.data()["operations"]; !equalJSON(got, want) {
			t.Errorf("capabilities = %v, want %v", got, want)
		}
		memberToken := h.login("member@example.com", userPassword)
		want = map[string]any{"OrdersList": true, "AuthMe": true, "OrdersCreate": false, "IdentityListUsers": false}
		if got := h.do("GET", "/auth/capabilities", nil, bearer(memberToken)...).data()["operations"]; !equalJSON(got, want) {
			t.Errorf("a member's capabilities = %v, want %v", got, want)
		}

		// The identity middleware feeds RequirePermissions unchanged.
		if r := h.do("POST", "/orders", nil, bearer(token)...); r.status != 200 {
			t.Errorf("the admin's order: %d", r.status)
		}
		h.expect(h.do("POST", "/orders", nil, bearer(memberToken)...), 403, "")
		h.expect(h.do("POST", "/orders", nil), 401, identity.CodeUnauthorized)

		for name, body := range map[string]map[string]any{
			"a wrong password":                  {"login": "admin@example.com", "password": "wrong password"},
			"an unknown login":                  {"login": "nobody@example.com", "password": adminPassword},
			"a login the scalar refuses":        {"login": "admin", "password": adminPassword},
			"the right password for another":    {"login": "member@example.com", "password": adminPassword},
			"the password in another case":      {"login": "admin@example.com", "password": strings.ToUpper(adminPassword)},
			"a bearer session asked explicitly": {"login": "admin@example.com", "password": "wrong password", "session": "bearer"},
		} {
			r := h.do("POST", "/auth/login", body)
			if r.status != 401 || r.code() != identity.CodeInvalidCredentials {
				t.Errorf("%s: %d %q, want 401 invalid_credentials", name, r.status, r.code())
			}
		}
		for name, body := range map[string]any{
			"a short password":      map[string]any{"login": "admin@example.com", "password": "short"},
			"no login":              map[string]any{"password": adminPassword},
			"an unknown session":    map[string]any{"login": "admin@example.com", "password": adminPassword, "session": "jwt"},
			"an unknown member":     map[string]any{"login": "admin@example.com", "password": adminPassword, "remember": true},
			"a body that is a list": []any{},
		} {
			r := h.do("POST", "/auth/login", body)
			if r.status != 400 || r.code() != "bad_request" {
				t.Errorf("%s: %d %q, want 400 bad_request", name, r.status, r.code())
			}
		}
	})
}

// TestLoginCookie: a cookie login sets the session cookie and answers no
// token; the cookie authenticates, logout revokes the session and clears
// it, and a refused cookie is cleared with the 401.
func TestLoginCookie(t *testing.T) {
	eachHarness(t, testConfig, func(t *testing.T, h *harness) {
		r := h.do("POST", "/auth/login", map[string]any{"login": "admin@example.com", "password": adminPassword, "session": "cookie"}, "Sec-Fetch-Site", "same-origin")
		h.expect(r, 200, "")
		if _, ok := r.data()["token"]; ok {
			t.Errorf("a cookie login answered a token: %v", r.data())
		}
		if len(r.cookies) != 1 {
			t.Fatalf("a cookie login set %v", r.cookies)
		}
		c := r.cookies[0]
		if c.Name != identity.HostCookieName || !c.HttpOnly || !c.Secure || c.Path != "/" || c.MaxAge != 1209600 || c.SameSite != http.SameSiteLaxMode || len(c.Value) != identity.TokenLength {
			t.Errorf("the session cookie is %s", c)
		}
		token := c.Value

		h.expect(h.do("GET", "/auth/me", nil, cookieHeader(token)...), 200, "")
		// A safe method skips the cross-origin check.
		h.expect(h.do("GET", "/auth/me", nil, with(cookieHeader(token), []string{"Sec-Fetch-Site", "cross-site"})...), 200, "")

		out := h.do("POST", "/auth/logout", nil, with(cookieHeader(token), []string{"Sec-Fetch-Site", "same-origin"})...)
		h.expect(out, 204, "")
		if len(out.cookies) != 1 || out.cookies[0].Name != identity.HostCookieName || out.cookies[0].MaxAge != -1 || out.cookies[0].Value != "" {
			t.Errorf("logout's cookies = %v, want the clear", out.cookies)
		}
		after := h.do("GET", "/auth/me", nil, cookieHeader(token)...)
		h.expect(after, 401, identity.CodeUnauthorized)
		if len(after.cookies) != 1 || after.cookies[0].MaxAge != -1 {
			t.Errorf("a refused cookie was not cleared: %v", after.cookies)
		}
		bad := h.do("GET", "/auth/me", nil, cookieHeader("not-a-token")...)
		h.expect(bad, 401, identity.CodeUnauthorized)
		if len(bad.cookies) != 1 || bad.cookies[0].MaxAge != -1 {
			t.Errorf("a cookie that is not a token was not cleared: %v", bad.cookies)
		}
	})
}

// TestCrossOrigin: a cookie login and a cookie request with an unsafe
// method from another origin are 403 cross_origin, unless the origin is
// trusted; a bearer request is not checked.
func TestCrossOrigin(t *testing.T) {
	eachHarness(t, testConfig, func(t *testing.T, h *harness) {
		cookieLogin := map[string]any{"login": "admin@example.com", "password": adminPassword, "session": "cookie"}
		h.expect(h.do("POST", "/auth/login", cookieLogin, "Sec-Fetch-Site", "cross-site", "Origin", "https://evil.example"), 403, identity.CodeCrossOrigin)
		h.expect(h.do("POST", "/auth/login", cookieLogin, "Origin", "https://evil.example"), 403, identity.CodeCrossOrigin)
		h.expect(h.do("POST", "/auth/register", map[string]any{"login": "new@example.com", "password": userPassword, "session": "cookie"}, "Sec-Fetch-Site", "cross-site"), 403, identity.CodeCrossOrigin)
		// A bearer login from another origin is not checked: it sets no
		// cookie.
		h.expect(h.do("POST", "/auth/login", map[string]any{"login": "admin@example.com", "password": adminPassword}, "Sec-Fetch-Site", "cross-site"), 200, "")

		trusted := h.do("POST", "/auth/login", cookieLogin, "Sec-Fetch-Site", "cross-site", "Origin", "https://app.example.com")
		h.expect(trusted, 200, "")
		token := trusted.cookies[0].Value
		if trusted.header.Get("Access-Control-Allow-Origin") != "https://app.example.com" || trusted.header.Get("Access-Control-Allow-Credentials") != "true" {
			t.Errorf("a trusted origin's answer has no CORS headers: %v", trusted.header)
		}

		cross := with(cookieHeader(token), []string{"Sec-Fetch-Site", "cross-site", "Origin", "https://evil.example"})
		refused := h.do("POST", "/auth/logout", nil, cross...)
		h.expect(refused, 403, identity.CodeCrossOrigin)
		if len(refused.cookies) != 0 {
			t.Errorf("a cross-origin refusal cleared the cookie: %v", refused.cookies)
		}
		h.expect(h.do("POST", "/orders", nil, cross...), 403, identity.CodeCrossOrigin)
		h.expect(h.do("POST", "/orders", nil, with(cookieHeader(token), []string{"Sec-Fetch-Site", "cross-site", "Origin", "https://app.example.com"})...), 200, "")

		bearerToken := h.login("admin@example.com", adminPassword)
		h.expect(h.do("POST", "/orders", nil, with(bearer(bearerToken), []string{"Sec-Fetch-Site", "cross-site", "Origin", "https://evil.example"})...), 200, "")
	})
}

// TestCredentialPrecedence: an Authorization header that is not a usable
// bearer token is 401 and the cookie is not read.
func TestCredentialPrecedence(t *testing.T) {
	eachHarness(t, testConfig, func(t *testing.T, h *harness) {
		r := h.do("POST", "/auth/login", map[string]any{"login": "admin@example.com", "password": adminPassword, "session": "cookie"})
		token := r.cookies[0].Value
		h.expect(h.do("GET", "/auth/me", nil, with([]string{"Authorization", "Basic YWRtaW46cGFzcw=="}, cookieHeader(token))...), 401, identity.CodeUnauthorized)
		h.expect(h.do("GET", "/auth/me", nil, with(bearer(strings.Repeat("A", 43)), cookieHeader(token))...), 401, identity.CodeUnauthorized)
		h.expect(h.do("GET", "/auth/me", nil, with(bearer(token), cookieHeader("x"))...), 200, "")
	})
}

// TestSessionEnds: a session ends when it expires, is revoked, goes idle,
// or its user is disabled; requests within the idle timeout keep it alive.
func TestSessionEnds(t *testing.T) {
	idle := `{"sessionTtlSeconds": 3600, "idleTimeoutSeconds": 600, "touchIntervalSeconds": 60, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	eachHarness(t, idle, func(t *testing.T, h *harness) {
		me := func(token string) reply { return h.do("GET", "/auth/me", nil, bearer(token)...) }

		active := h.login("member@example.com", userPassword)
		for range 10 {
			h.clock.Advance(500 * time.Second)
			if r := me(active); r.status == 401 {
				// The session expires an hour after login, the eighth step.
				if h.clock.Now().Before(time.Date(2026, 10, 8, 13, 0, 0, 0, time.UTC)) {
					t.Fatalf("an active session ended at %v", h.clock.Now())
				}
				break
			}
		}
		h.expect(me(active), 401, identity.CodeUnauthorized)

		idleToken := h.login("member@example.com", userPassword)
		h.clock.Advance(599 * time.Second)
		h.expect(me(idleToken), 200, "")
		h.clock.Advance(599 * time.Second)
		h.expect(me(idleToken), 200, "")
		h.clock.Advance(600 * time.Second)
		h.expect(me(idleToken), 401, identity.CodeUnauthorized)

		revoked := h.login("member@example.com", userPassword)
		h.expect(h.do("POST", "/auth/logout", nil, bearer(revoked)...), 204, "")
		h.expect(me(revoked), 401, identity.CodeUnauthorized)
		h.expect(h.do("POST", "/auth/logout", nil, bearer(revoked)...), 401, identity.CodeUnauthorized)

		disabled := h.login("member@example.com", userPassword)
		admin := h.login("admin@example.com", adminPassword)
		h.expect(h.do("POST", "/auth/admin/users/"+h.memberID+"/disable", nil, bearer(admin)...), 200, "")
		h.expect(me(disabled), 401, identity.CodeUnauthorized)
		h.expect(h.do("POST", "/auth/login", map[string]any{"login": "member@example.com", "password": userPassword}), 401, identity.CodeInvalidCredentials)
		h.expect(h.do("POST", "/auth/admin/users/"+h.memberID+"/enable", nil, bearer(admin)...), 200, "")
		// Enabling does not bring a revoked session back.
		h.expect(me(disabled), 401, identity.CodeUnauthorized)
		h.login("member@example.com", userPassword)
	})
}

// TestChangePassword: the current password must verify; the change keeps
// the caller's session and revokes the others.
func TestChangePassword(t *testing.T) {
	eachHarness(t, testConfig, func(t *testing.T, h *harness) {
		mine := h.login("member@example.com", userPassword)
		other := h.login("member@example.com", userPassword)
		const newPassword = "a new password"

		h.expect(h.do("POST", "/auth/password", map[string]any{"current": "not my password", "password": newPassword}, bearer(mine)...), 401, identity.CodeInvalidCredentials)
		h.expect(h.do("POST", "/auth/password", map[string]any{"current": userPassword, "password": "short"}, bearer(mine)...), 400, "bad_request")
		h.expect(h.do("POST", "/auth/password", map[string]any{"current": userPassword, "password": newPassword}), 401, identity.CodeUnauthorized)
		h.expect(h.do("POST", "/auth/password", map[string]any{"current": userPassword, "password": newPassword}, bearer(mine)...), 204, "")

		h.expect(h.do("GET", "/auth/me", nil, bearer(mine)...), 200, "")
		h.expect(h.do("GET", "/auth/me", nil, bearer(other)...), 401, identity.CodeUnauthorized)
		h.expect(h.do("POST", "/auth/login", map[string]any{"login": "member@example.com", "password": userPassword}), 401, identity.CodeInvalidCredentials)
		h.login("member@example.com", newPassword)
	})
}

// TestRegister: register creates a user and signs them in as login does;
// a taken login is 409 conflict.
func TestRegister(t *testing.T) {
	eachHarness(t, testConfig, func(t *testing.T, h *harness) {
		r := h.do("POST", "/auth/register", map[string]any{"login": "New@Example.com", "name": "Newcomer", "password": userPassword})
		h.expect(r, 200, "")
		user := r.data()["user"].(map[string]any)
		if user["login"] != "new@example.com" || user["name"] != "Newcomer" {
			t.Errorf("register's user = %v", user)
		}
		me := h.do("GET", "/auth/me", nil, bearer(r.data()["token"].(string))...)
		h.expect(me, 200, "")
		if !equalJSON(me.data()["roles"], []any{}) || !equalJSON(me.data()["permissions"], []any{}) {
			t.Errorf("a new user's me = %v", me.data())
		}

		unnamed := h.do("POST", "/auth/register", map[string]any{"login": "plain@example.com", "password": userPassword, "session": "cookie"})
		h.expect(unnamed, 200, "")
		if unnamed.data()["user"].(map[string]any)["name"] != "plain@example.com" || len(unnamed.cookies) != 1 {
			t.Errorf("a register without a name = %v, cookies %v", unnamed.data(), unnamed.cookies)
		}

		h.expect(h.do("POST", "/auth/register", map[string]any{"login": "NEW@example.com", "password": userPassword}), 409, identity.CodeConflict)
		bad := h.do("POST", "/auth/register", map[string]any{"login": "not an email", "password": userPassword})
		h.expect(bad, 400, "bad_request")
		if _, ok := bad.body["errors"].(map[string]any)["login"]; !ok {
			t.Errorf("a refused login names no field: %v", bad.body)
		}
	})
}

// TestAdministration: the administration routes need their permissions,
// and no one grants what they do not hold.
func TestAdministration(t *testing.T) {
	eachHarness(t, testConfig, func(t *testing.T, h *harness) {
		admin := bearer(h.login("admin@example.com", adminPassword))
		member := bearer(h.login("member@example.com", userPassword))

		h.expect(h.do("GET", "/auth/admin/users", nil, member...), 403, identity.CodeForbidden)
		h.expect(h.do("GET", "/auth/admin/users", nil), 401, identity.CodeUnauthorized)

		created := h.do("POST", "/auth/admin/users", map[string]any{"login": "clerk@example.com", "name": "Clerk", "password": userPassword}, admin...)
		h.expect(created, 200, "")
		clerkID := created.data()["id"].(string)
		if created.data()["disabled"] != false || !equalJSON(created.data()["roles"], []any{}) {
			t.Errorf("createUser = %v", created.data())
		}
		h.expect(h.do("POST", "/auth/admin/users", map[string]any{"login": "CLERK@example.com", "password": userPassword}, admin...), 409, identity.CodeConflict)
		h.expect(h.do("POST", "/auth/admin/users", map[string]any{"login": "x@example.com", "password": userPassword}, member...), 403, identity.CodeForbidden)

		users := h.do("GET", "/auth/admin/users", nil, admin...)
		h.expect(users, 200, "")
		if l := users.list(); len(l) != 3 || l[0].(map[string]any)["login"] != "admin@example.com" || l[1].(map[string]any)["login"] != "clerk@example.com" {
			t.Errorf("listUsers = %v", l)
		}
		h.expect(h.do("GET", "/auth/admin/users/"+clerkID, nil, admin...), 200, "")
		h.expect(h.do("GET", "/auth/admin/users/00000000-0000-4000-8000-000000000000", nil, admin...), 404, identity.CodeNotFound)
		h.expect(h.do("GET", "/auth/admin/users/not-a-key", nil, admin...), 404, identity.CodeNotFound)

		// A manager may write roles but holds only orders.read.
		manager := h.do("POST", "/auth/admin/roles", map[string]any{"name": "manager", "permissions": []string{"identity.roles", "orders.read"}}, admin...)
		h.expect(manager, 200, "")
		h.expect(h.do("PUT", "/auth/admin/users/"+clerkID+"/roles/"+manager.data()["id"].(string), nil, admin...), 200, "")
		clerk := bearer(h.login("clerk@example.com", userPassword))

		denied := h.do("POST", "/auth/admin/roles", map[string]any{"name": "writer", "permissions": []string{"orders.read", "orders.write"}}, clerk...)
		h.expect(denied, 403, identity.CodeForbidden)
		if !equalJSON(denied.body["details"], map[string]any{"permissions": []any{"orders.write"}}) {
			t.Errorf("the refusal's details = %v", denied.body["details"])
		}
		reader := h.do("POST", "/auth/admin/roles", map[string]any{"name": "reader", "permissions": []string{"orders.read", "orders.read.archive", "orders.read"}}, clerk...)
		h.expect(reader, 200, "")
		if !equalJSON(reader.data()["permissions"], []any{"orders.read", "orders.read.archive"}) {
			t.Errorf("a role's permissions are kept without repeats, in order: %v", reader.data()["permissions"])
		}
		readerID := reader.data()["id"].(string)
		h.expect(h.do("PUT", "/auth/admin/roles/"+readerID, map[string]any{"name": "reader", "permissions": []string{"orders"}}, clerk...), 403, identity.CodeForbidden)
		h.expect(h.do("POST", "/auth/admin/roles", map[string]any{"name": "bad", "permissions": []string{"orders read", "a..b"}}, admin...), 422, identity.CodeInvalidPermission)
		h.expect(h.do("POST", "/auth/admin/roles", map[string]any{"name": "reader", "permissions": []string{}}, admin...), 409, identity.CodeConflict)
		h.expect(h.do("POST", "/auth/admin/roles", map[string]any{"name": "", "permissions": []string{}}, admin...), 400, "bad_request")

		// The admin's role carries orders, which the clerk does not hold.
		roles := h.do("GET", "/auth/admin/roles", nil, admin...)
		h.expect(roles, 200, "")
		var adminRoleID string
		for _, r := range roles.list() {
			if r.(map[string]any)["name"] == "admin" {
				adminRoleID = r.(map[string]any)["id"].(string)
			}
		}
		h.expect(h.do("PUT", "/auth/admin/users/"+clerkID+"/roles/"+adminRoleID, nil, clerk...), 403, identity.CodeForbidden)
		granted := h.do("PUT", "/auth/admin/users/"+h.memberID+"/roles/"+readerID, nil, clerk...)
		h.expect(granted, 200, "")
		if !equalJSON(granted.data()["roles"], []any{map[string]any{"id": readerID, "name": "reader"}}) {
			t.Errorf("grantRole's user = %v", granted.data())
		}
		h.expect(h.do("PUT", "/auth/admin/users/"+h.memberID+"/roles/00000000-0000-4000-8000-000000000000", nil, admin...), 404, identity.CodeNotFound)
		h.expect(h.do("DELETE", "/auth/admin/users/"+h.memberID+"/roles/"+readerID, nil, clerk...), 200, "")

		updated := h.do("PUT", "/auth/admin/roles/"+readerID, map[string]any{"name": "order reader", "permissions": []string{"orders.read"}}, clerk...)
		h.expect(updated, 200, "")
		if updated.data()["name"] != "order reader" {
			t.Errorf("updateRole = %v", updated.data())
		}
		h.expect(h.do("DELETE", "/auth/admin/roles/"+readerID, nil, clerk...), 204, "")
		h.expect(h.do("DELETE", "/auth/admin/roles/"+readerID, nil, clerk...), 404, identity.CodeNotFound)
		h.expect(h.do("GET", "/auth/admin/roles", nil, member...), 403, identity.CodeForbidden)

		// Setting a user's password revokes their sessions.
		h.expect(h.do("PUT", "/auth/admin/users/"+h.memberID+"/password", map[string]any{"password": "reset password"}, admin...), 204, "")
		h.expect(h.do("GET", "/auth/me", nil, member...), 401, identity.CodeUnauthorized)
		h.login("member@example.com", "reset password")
		h.expect(h.do("PUT", "/auth/admin/users/"+h.memberID+"/password", map[string]any{"password": "short"}, admin...), 400, "bad_request")
		h.expect(h.do("PUT", "/auth/admin/users/00000000-0000-4000-8000-000000000000/password", map[string]any{"password": "reset password"}, admin...), 404, identity.CodeNotFound)

		disabled := h.do("POST", "/auth/admin/users/"+clerkID+"/disable", nil, admin...)
		h.expect(disabled, 200, "")
		if disabled.data()["disabled"] != true {
			t.Errorf("disableUser = %v", disabled.data())
		}
		h.expect(h.do("GET", "/auth/me", nil, clerk...), 401, identity.CodeUnauthorized)
	})
}

// TestLoginRehashes: a login whose hash has another cost writes it again at
// the config's.
func TestLoginRehashes(t *testing.T) {
	eachHarness(t, testConfig, func(t *testing.T, h *harness) {
		ctx := context.Background()
		old, err := identity.HashPassword(userPassword, identity.Argon2Params{MemoryKiB: 32, Iterations: 1, Parallelism: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.SetPassword(ctx, h.memberID, old, h.clock.Now(), ""); err != nil {
			t.Fatal(err)
		}
		h.login("member@example.com", userPassword)
		rec, err := h.store.FindLogin(ctx, "member@example.com")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(rec.PasswordHash, "$argon2id$v=19$m=64,t=1,p=1$") {
			t.Errorf("after login the hash is %s, want the config's cost", rec.PasswordHash)
		}
		h.login("member@example.com", userPassword)
	})
}

// TestCORS: the trusted origin's preflight is answered with credentials;
// another origin's passes through without CORS headers.
func TestCORS(t *testing.T) {
	eachHarness(t, testConfig, func(t *testing.T, h *harness) {
		r := h.do("OPTIONS", "/auth/login", nil, "Origin", "https://app.example.com", "Access-Control-Request-Method", "POST", "Access-Control-Request-Headers", "content-type")
		if r.status != 204 || r.header.Get("Access-Control-Allow-Origin") != "https://app.example.com" || r.header.Get("Access-Control-Allow-Credentials") != "true" ||
			r.header.Get("Access-Control-Allow-Methods") != "POST" || r.header.Get("Access-Control-Allow-Headers") != "content-type" {
			t.Errorf("a trusted preflight: %d %v", r.status, r.header)
		}
		other := h.do("OPTIONS", "/auth/login", nil, "Origin", "https://evil.example", "Access-Control-Request-Method", "POST")
		if other.status == 204 || other.header.Get("Access-Control-Allow-Origin") != "" || other.header.Get("Vary") != "Origin" {
			t.Errorf("another origin's preflight: %d %v", other.status, other.header)
		}
	})
}

// TestHandlers: every operation the contract names has a handler, and the
// routes follow the sets' options.
func TestHandlers(t *testing.T) {
	h := newHarness(t, testDB{dialect: identity.SQLite, db: openSQLite(t)}, testConfig)
	for _, op := range identity.Operations {
		if _, ok := h.svc.Handler(op.Name); !ok {
			t.Errorf("no handler for %s", op.Name)
		}
	}
	if _, ok := h.svc.Handler("refresh"); ok {
		t.Error("a handler for an operation the contract does not name")
	}
	paths := func(routes []routing.Route) string {
		var out []string
		for _, r := range routes {
			out = append(out, r.Method+" "+r.Path)
		}
		return strings.Join(out, ", ")
	}
	if got := paths(h.svc.Routes(&ir.UserSessionsConfig{Path: "account", NoLogin: true}, nil)); got != "GET /account/me, GET /account/capabilities" {
		t.Errorf("login: false routes %s", got)
	}
	if got := paths(h.svc.Routes(&ir.UserSessionsConfig{}, nil)); strings.Contains(got, "register") || !strings.Contains(got, "POST /auth/login") {
		t.Errorf("register: false routes %s", got)
	}
	if got := len(h.svc.Routes(&ir.UserSessionsConfig{Register: true}, &ir.UserAdministrationConfig{})); got != len(identity.Operations) {
		t.Errorf("every set routes %d operations, want %d", got, len(identity.Operations))
	}
	for _, op := range identity.Operations {
		if op.Name == ir.IdentityOpGrantRole && op.Path != "users/{id}/roles/{roleId}" {
			t.Errorf("grantRole's path is %s", op.Path)
		}
	}
}

// TestHandlersOnServeMux: the handlers read path parameters from the
// standard library's mux as well as chi's.
func TestHandlersOnServeMux(t *testing.T) {
	h := newHarness(t, testDB{dialect: identity.SQLite, db: openSQLite(t)}, testConfig)
	mux := http.NewServeMux()
	for _, r := range h.svc.Routes(&ir.UserSessionsConfig{Register: true}, &ir.UserAdministrationConfig{}) {
		mux.Handle(r.Method+" "+r.Path, r.Handler)
	}
	h.router = mux
	admin := bearer(h.login("admin@example.com", adminPassword))
	r := h.do("GET", "/auth/admin/users/"+h.memberID, nil, admin...)
	h.expect(r, 200, "")
	if r.data()["id"] != h.memberID {
		t.Errorf("getUser on a ServeMux = %v", r.data())
	}
}

func equalJSON(got, want any) bool {
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	return bytes.Equal(g, w)
}

// TestUnknownLoginTakesAVerify: a login for an account that does not exist
// verifies a hash at the config's cost, so it takes as long as a wrong
// password.
func TestUnknownLoginTakesAVerify(t *testing.T) {
	costly := `{"password": {"argon2": {"memoryKiB": 16384, "iterations": 2, "parallelism": 1}}}`
	h := newHarness(t, testDB{dialect: identity.SQLite, db: openSQLite(t)}, costly)
	fastest := func(body map[string]any) time.Duration {
		best := time.Duration(1<<63 - 1)
		for range 3 {
			start := time.Now()
			h.expect(h.do("POST", "/auth/login", body), 401, identity.CodeInvalidCredentials)
			best = min(best, time.Since(start))
		}
		return best
	}
	wrong := fastest(map[string]any{"login": "member@example.com", "password": "wrong password"})
	unknown := fastest(map[string]any{"login": "nobody@example.com", "password": "wrong password"})
	refused := fastest(map[string]any{"login": "nobody", "password": "wrong password"})
	if unknown < wrong/3 || refused < wrong/3 {
		t.Errorf("a wrong password takes %v, an unknown login %v and a refused one %v: the unknown logins skip the verify", wrong, unknown, refused)
	}
}
