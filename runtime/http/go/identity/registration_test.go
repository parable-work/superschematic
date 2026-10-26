package identity_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/identity"
)

// The registration modes of D81: approval-gated registration, default
// roles and the first administrator.

// approval is a registration under approval with the default role member.
var approval = identity.WithRegistration(identity.Registration{Approval: true, DefaultRoles: []string{"member"}})

// roleNames are the names of the roles in an IdentityUser's or CurrentUser's
// roles member.
func roleNames(v any) []string {
	var names []string
	roles, _ := v.([]any)
	for _, role := range roles {
		if m, ok := role.(map[string]any); ok {
			names = append(names, fmt.Sprint(m["name"]))
		}
	}
	return names
}

// TestRegistrationUnderApproval is G7's acceptance: register answers 202
// with no session and no cookie, login is refused as pending with a code of
// its own, the administrator lists and approves the user, and the user then
// signs in holding the default roles.
func TestRegistrationUnderApproval(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db testDB, _ *identity.SQLStore) {
		h := newHarnessWith(t, db, testConfig, true, approval)
		const password = "new user password"

		for _, session := range []string{"cookie", "bearer"} {
			login := session + "@example.com"
			r := h.do("POST", "/auth/register", map[string]any{"login": login, "name": "New", "password": password, "session": session}, "Sec-Fetch-Site", "same-origin")
			h.expect(r, 202, "")
			if len(r.cookies) != 0 {
				t.Errorf("a %s registration under approval set %v", session, r.cookies)
			}
			data := r.data()
			if data["pending"] != true || data["user"].(map[string]any)["login"] != login {
				t.Errorf("a %s registration answered %v, want pending and the user", session, data)
			}
			for _, absent := range []string{"token", "expiresAt"} {
				if _, ok := data[absent]; ok {
					t.Errorf("a pending registration answered %s: %v", absent, data)
				}
			}
		}

		// A pending user's login is refused with its own code once the
		// password verifies, and as any failed login otherwise.
		h.expect(h.do("POST", "/auth/login", map[string]any{"login": "bearer@example.com", "password": password}), 403, identity.CodePendingApproval)
		h.expect(h.do("POST", "/auth/login", map[string]any{"login": "bearer@example.com", "password": "wrong password"}), 401, identity.CodeInvalidCredentials)

		admin := h.login("admin@example.com", adminPassword)
		pending := h.do("GET", "/auth/admin/users?pending=true", nil, bearer(admin)...)
		h.expect(pending, 200, "")
		var ids []string
		for _, u := range pending.list() {
			user := u.(map[string]any)
			if user["pending"] != true || user["disabled"] != false || !slices.Equal(roleNames(user["roles"]), []string{"member"}) {
				t.Errorf("a pending user lists as %v", user)
			}
			ids = append(ids, user["id"].(string))
		}
		if len(ids) != 2 {
			t.Fatalf("pending users = %v, want the two who registered", pending.list())
		}
		others := h.do("GET", "/auth/admin/users?pending=false", nil, bearer(admin)...)
		if len(others.list()) != 2 {
			t.Errorf("users not pending = %v, want the admin and the member", others.list())
		}
		h.expect(h.do("GET", "/auth/admin/users?pending=yes", nil, bearer(admin)...), 400, "bad_request")
		if all := h.do("GET", "/auth/admin/users", nil, bearer(admin)...); len(all.list()) != 4 {
			t.Errorf("users = %v, want all four", all.list())
		}

		// Only a caller with users.write approves.
		member := h.login("member@example.com", userPassword)
		h.expect(h.do("POST", "/auth/admin/users/"+ids[0]+"/approve", nil, bearer(member)...), 403, identity.CodeForbidden)
		h.expect(h.do("POST", "/auth/admin/users/"+ids[0]+"/approve", nil), 401, identity.CodeUnauthorized)

		approved := h.do("POST", "/auth/admin/users/"+ids[0]+"/approve", nil, bearer(admin)...)
		h.expect(approved, 200, "")
		if approved.data()["pending"] != false {
			t.Errorf("approveUser answered %v, want the user no longer pending", approved.data())
		}
		h.expect(h.do("POST", "/auth/admin/users/"+ids[0]+"/approve", nil, bearer(admin)...), 409, identity.CodeNotPending)
		h.expect(h.do("POST", "/auth/admin/users/"+h.adminID+"/approve", nil, bearer(admin)...), 409, identity.CodeNotPending)
		h.expect(h.do("POST", "/auth/admin/users/1aB2cD3eF4gH5iJ6kL7mN8/approve", nil, bearer(admin)...), 404, identity.CodeNotFound)

		approvedLogin := approved.data()["login"].(string)
		token := h.login(approvedLogin, password)
		me := h.do("GET", "/auth/me", nil, bearer(token)...)
		h.expect(me, 200, "")
		if got := roleNames(me.data()["roles"]); !slices.Equal(got, []string{"member"}) {
			t.Errorf("the approved user holds %v, want the default role member", got)
		}
		// The other is still pending.
		h.expect(h.do("POST", "/auth/login", map[string]any{"login": otherLogin(approvedLogin), "password": password}), 403, identity.CodePendingApproval)
	})
}

func otherLogin(login string) string {
	if login == "cookie@example.com" {
		return "bearer@example.com"
	}
	return "cookie@example.com"
}

// TestApprovalNeedsTheUsersPermissions: approval gives a user the roles
// register granted them, so the caller's permissions must cover theirs.
func TestApprovalNeedsTheUsersPermissions(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db testDB, _ *identity.SQLStore) {
		h := newHarnessWith(t, db, testConfig, true, approval)
		ctx := context.Background()
		if _, err := h.store.CreateRole(ctx, "member", []string{"orders.read"}); err != nil {
			t.Fatal(err)
		}
		clerk, err := h.store.CreateRole(ctx, "clerk", []string{"identity.users"})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.GrantRole(ctx, h.memberID, clerk.ID, h.clock.Now()); err != nil {
			t.Fatal(err)
		}
		h.expect(h.do("POST", "/auth/register", map[string]any{"login": "new@example.com", "password": "new user password"}), 202, "")
		id := h.do("GET", "/auth/admin/users?pending=true", nil, bearer(h.login("admin@example.com", adminPassword))...).list()[0].(map[string]any)["id"].(string)

		r := h.do("POST", "/auth/admin/users/"+id+"/approve", nil, bearer(h.login("member@example.com", userPassword))...)
		h.expect(r, 403, identity.CodeForbidden)
		if details, _ := r.body["details"].(map[string]any); !equalJSON(details["permissions"], []any{"orders.read"}) {
			t.Errorf("the refusal's details = %v, want the member role's orders.read", r.body["details"])
		}
	})
}

// TestOpenRegistrationGrantsTheDefaultRoles: open registration signs the
// user in at once, holding the default roles, the one register creates
// with no permissions when no role has the name.
func TestOpenRegistrationGrantsTheDefaultRoles(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db testDB, _ *identity.SQLStore) {
		h := newHarnessWith(t, db, testConfig, true, identity.WithRegistration(identity.Registration{DefaultRoles: []string{"member", "reader"}}))
		if _, err := h.store.CreateRole(context.Background(), "reader", []string{"orders.read"}); err != nil {
			t.Fatal(err)
		}
		r := h.do("POST", "/auth/register", map[string]any{"login": "new@example.com", "password": "new user password"})
		h.expect(r, 200, "")
		if _, ok := r.data()["pending"]; ok {
			t.Errorf("open registration answered RegisterResult %v, want LoginResult", r.data())
		}
		me := h.do("GET", "/auth/me", nil, bearer(r.data()["token"].(string))...)
		if got := roleNames(me.data()["roles"]); !slices.Equal(got, []string{"member", "reader"}) {
			t.Errorf("the new user holds %v", got)
		}
		if !equalJSON(me.data()["permissions"], []any{"orders.read"}) {
			t.Errorf("the new user's permissions = %v, want reader's alone", me.data()["permissions"])
		}
	})
}

// firstUser is approval with a first administrator.
var firstUser = identity.WithRegistration(identity.Registration{
	Approval: true, DefaultRoles: []string{"member"},
	FirstUser: &identity.FirstUser{Role: "admin", Permissions: []string{"identity", "orders"}},
})

// TestFirstRegistrantIsTheAdministrator: on a database with no role grant,
// the first registration is active and signed in, holding the first
// administrator's role with its permissions beside the default roles; the
// next waits for approval, and the first administrator approves them.
func TestFirstRegistrantIsTheAdministrator(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db testDB, _ *identity.SQLStore) {
		h := newHarnessWith(t, db, testConfig, false, firstUser)
		first := h.do("POST", "/auth/register", map[string]any{"login": "ada@example.com", "password": "ada's password"})
		h.expect(first, 200, "")
		if first.data()["pending"] != false || first.data()["token"] == nil || first.data()["expiresAt"] == nil {
			t.Fatalf("the first registration answered %v, want a session", first.data())
		}
		token := first.data()["token"].(string)
		me := h.do("GET", "/auth/me", nil, bearer(token)...)
		if got := roleNames(me.data()["roles"]); !slices.Equal(got, []string{"admin", "member"}) {
			t.Errorf("the first administrator holds %v", got)
		}
		if !equalJSON(me.data()["permissions"], []any{"identity", "orders"}) {
			t.Errorf("the first administrator's permissions = %v", me.data()["permissions"])
		}

		h.expect(h.do("POST", "/auth/register", map[string]any{"login": "bob@example.com", "password": "bob's password"}), 202, "")
		h.expect(h.do("POST", "/auth/login", map[string]any{"login": "bob@example.com", "password": "bob's password"}), 403, identity.CodePendingApproval)
		bob := h.do("GET", "/auth/admin/users?pending=true", nil, bearer(token)...).list()
		if len(bob) != 1 {
			t.Fatalf("pending users = %v, want bob", bob)
		}
		h.expect(h.do("POST", "/auth/admin/users/"+bob[0].(map[string]any)["id"].(string)+"/approve", nil, bearer(token)...), 200, "")
		h.login("bob@example.com", "bob's password")
	})
}

// TestFirstRegistrantAfterABootstrap: a database identity bootstrap set up
// has a grant, so no registrant becomes its administrator.
func TestFirstRegistrantAfterABootstrap(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db testDB, _ *identity.SQLStore) {
		h := newHarnessWith(t, db, testConfig, true, firstUser)
		h.expect(h.do("POST", "/auth/register", map[string]any{"login": "eve@example.com", "password": "eve's password"}), 202, "")
	})
}

// TestFirstRegistrantsAtOnce: registrations that race on an empty
// database make one administrator. On Postgres every one succeeds; on
// SQLite one may lose the write lock, as two bootstraps may, and none
// that loses becomes an administrator.
func TestFirstRegistrantsAtOnce(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db testDB, _ *identity.SQLStore) {
		h := newHarnessWith(t, db, testConfig, false, firstUser)
		const n = 8
		var wg sync.WaitGroup
		statuses := make([]int, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := h.do("POST", "/auth/register", map[string]any{"login": fmt.Sprintf("racer%d@example.com", i), "password": "racer's password"})
				statuses[i] = r.status
			}()
		}
		wg.Wait()
		active, pending := 0, 0
		for _, status := range statuses {
			switch status {
			case 200:
				active++
			case 202:
				pending++
			default:
				if db.dialect == identity.Postgres {
					t.Errorf("a registration answered %d", status)
				}
			}
		}
		if active != 1 {
			t.Errorf("statuses %v: %d active registrations, want one administrator", statuses, active)
		}
		users, err := h.store.ListUsers(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		admins := 0
		for _, u := range users {
			for _, role := range u.Roles {
				if role.Name == "admin" {
					admins++
				}
			}
		}
		if admins != 1 || len(users) != active+pending {
			t.Errorf("%d users hold admin of %d, want one of %d", admins, len(users), active+pending)
		}
	})
}
