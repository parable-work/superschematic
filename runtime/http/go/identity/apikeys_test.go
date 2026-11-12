package identity_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	superscalar "github.com/parable-work/superscalar/go"

	"github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/runtime/http/go/identity"
	"github.com/parable-work/superschematic/runtime/http/go/routing"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// API keys (D114): the store's rows, authentication by a key, and the five
// operations.

// The refusals' details, the same text in every runtime.
const (
	sessionOnlyDetail = "An API key does not act on its own account: sign in with a session"
	notHeldDetail     = "An API key names only permissions its user holds"
	noAPIKeysDetail   = "API keys are not available: this database's types hold no API key table"
)

// missingKey is an id in a key's form that no row has.
const missingKey = "00000000-0000-4000-8000-000000000000"

// placeholders is query with its ? placeholders as the database's dialect
// writes them.
func (db testDB) placeholders(query string) string {
	if db.dialect != identity.Postgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, c := range query {
		if c == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// mustAPIKey creates user a key that lasts an hour from created.
func mustAPIKey(t *testing.T, s identity.Store, user identity.User, name, hash string, permissions []string, created time.Time) identity.APIKey {
	t.Helper()
	key, err := s.CreateAPIKey(context.Background(), identity.NewAPIKeyRow{
		UserID: user.ID, Name: name, TokenHash: hash, Permissions: permissions, CreatedAt: created, ExpiresAt: created.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create API key %s: %v", name, err)
	}
	return key
}

func findAPIKey(t *testing.T, s identity.Store, hash string) identity.APIKeyRecord {
	t.Helper()
	rec, err := s.FindAPIKey(context.Background(), hash)
	if err != nil {
		t.Fatalf("find API key %s: %v", hash, err)
	}
	return rec
}

func keyNames(keys []identity.APIKey) string {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = k.Name
	}
	return strings.Join(names, ",")
}

// databaseKey is a wire id's form in the database, the hyphenated UUID the
// store orders by.
func databaseKey(t *testing.T, id string) string {
	t.Helper()
	u, err := superscalar.ParseUUID(id)
	if err != nil {
		t.Fatalf("id %q: %v", id, err)
	}
	return u.ToUUID().String()
}

// TestStoreAPIKeys: an API key is created with its permissions, null for
// all of its user's, found by its token hash with its user, touched once
// per interval, listed newest first without the revoked ones, and revoked
// by its user alone; disabling a user revokes their keys, and deleting one
// deletes them.
func TestStoreAPIKeys(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db testDB, s *identity.SQLStore) {
		ctx := context.Background()
		if !s.HasAPIKeys() {
			t.Fatal("the fixture has an API key table")
		}
		alice := mustCreateUser(t, s, "alice@example.com", "Alice")
		bob := mustCreateUser(t, s, "bob@example.com", "Bob")

		all := mustAPIKey(t, s, alice, "ci", hashOf(1), nil, at(0))
		if all.ID == "" || strings.Contains(all.ID, "-") || all.UserID != alice.ID || all.Name != "ci" || all.Permissions != nil ||
			!all.CreatedAt.Equal(at(0)) || !all.ExpiresAt.Equal(at(time.Hour)) || all.LastUsedAt != nil || all.RevokedAt != nil {
			t.Fatalf("created %+v", all)
		}
		rec := findAPIKey(t, s, hashOf(1))
		if rec.APIKey.ID != all.ID || rec.APIKey.UserID != alice.ID || rec.APIKey.Name != "ci" || rec.APIKey.Permissions != nil ||
			!rec.APIKey.CreatedAt.Equal(at(0)) || !rec.APIKey.ExpiresAt.Equal(at(time.Hour)) || rec.APIKey.LastUsedAt != nil || rec.APIKey.RevokedAt != nil {
			t.Fatalf("FindAPIKey's key = %+v", rec.APIKey)
		}
		if rec.User.ID != alice.ID || rec.User.Login != "alice@example.com" || rec.User.Name != "Alice" || rec.User.Disabled || rec.User.Pending {
			t.Fatalf("FindAPIKey's user = %+v", rec.User)
		}
		if _, err := s.FindAPIKey(ctx, hashOf(99)); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("an unknown token hash: %v, want ErrNotFound", err)
		}
		if _, err := s.CreateAPIKey(ctx, identity.NewAPIKeyRow{UserID: bob.ID, Name: "copy", TokenHash: hashOf(1), CreatedAt: at(0), ExpiresAt: at(time.Hour)}); err == nil {
			t.Error("a second key with a token hash already kept was created")
		}
		if _, err := s.CreateAPIKey(ctx, identity.NewAPIKeyRow{UserID: "not a key", Name: "x", TokenHash: hashOf(98), CreatedAt: at(0), ExpiresAt: at(time.Hour)}); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("a key for an id that is no user's: %v, want ErrNotFound", err)
		}

		// Permissions are kept in order; null is all, and an empty list none.
		some := mustAPIKey(t, s, alice, "reader", hashOf(2), []string{"orders.read", "issues"}, at(time.Minute))
		if got := some.Permissions; !slices.Equal(got, []string{"orders.read", "issues"}) {
			t.Errorf("a created key's permissions = %v", got)
		}
		if got := findAPIKey(t, s, hashOf(2)).APIKey.Permissions; !slices.Equal(got, []string{"orders.read", "issues"}) {
			t.Errorf("a key's permissions read as %v", got)
		}
		mustAPIKey(t, s, alice, "nothing", hashOf(3), []string{}, at(2*time.Minute))
		if got := findAPIKey(t, s, hashOf(3)).APIKey.Permissions; got == nil || len(got) != 0 {
			t.Errorf("an empty list of permissions reads as %#v, want an empty list", got)
		}
		var null bool
		if err := db.db.QueryRow(db.placeholders(`SELECT "permissions" IS NULL FROM "user_api_key" WHERE "token_hash" = ?`), hashOf(1)).Scan(&null); err != nil || !null {
			t.Errorf("a key with every permission keeps null: %v, %v", null, err)
		}
		if db.dialect == identity.SQLite {
			var created, permissions string
			if err := db.db.QueryRow(`SELECT "created_at", "permissions" FROM "user_api_key" WHERE "token_hash" = ?`, hashOf(2)).Scan(&created, &permissions); err != nil {
				t.Fatal(err)
			}
			if created != "2026-10-08T12:01:00.000Z" || permissions != `["orders.read","issues"]` {
				t.Errorf("SQLite keeps createdAt %q and permissions %q", created, permissions)
			}
		}

		// The first touch writes lastUsedAt; one within the interval does
		// not; one after it does.
		touch := func(when time.Time) *time.Time {
			t.Helper()
			if err := s.TouchAPIKey(ctx, all.ID, when, when.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			return findAPIKey(t, s, hashOf(1)).APIKey.LastUsedAt
		}
		if used := touch(at(time.Second)); used == nil || !used.Equal(at(time.Second)) {
			t.Fatalf("after the first touch lastUsedAt = %v", used)
		}
		if used := touch(at(30 * time.Second)); !used.Equal(at(time.Second)) {
			t.Errorf("a touch within the interval moved lastUsedAt to %v", used)
		}
		if used := touch(at(2 * time.Minute)); !used.Equal(at(2 * time.Minute)) {
			t.Errorf("a touch after the interval left lastUsedAt at %v", used)
		}
		if err := s.TouchAPIKey(ctx, "not a key", at(0), at(0)); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("a touch of an id that is no key's: %v, want ErrNotFound", err)
		}

		// Newest first, then by the database's key descending; an expired
		// key is listed, and another user's is not.
		_, err := s.CreateAPIKey(ctx, identity.NewAPIKeyRow{UserID: alice.ID, Name: "expired", TokenHash: hashOf(4), CreatedAt: at(-2 * time.Hour), ExpiresAt: at(-time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		for n := range 4 {
			mustAPIKey(t, s, alice, "twin", hashOf(10+n), nil, at(3*time.Minute))
		}
		bobs := mustAPIKey(t, s, bob, "bob's", hashOf(5), nil, at(time.Hour))
		listed, err := s.ListAPIKeys(ctx, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := keyNames(listed); got != "twin,twin,twin,twin,nothing,reader,ci,expired" {
			t.Fatalf("ListAPIKeys = %s", got)
		}
		for i := 1; i < 4; i++ {
			if a, b := databaseKey(t, listed[i-1].ID), databaseKey(t, listed[i].ID); a <= b {
				t.Errorf("keys created at once list %s before %s, want the database's keys descending", a, b)
			}
		}
		if last := listed[len(listed)-1]; !last.ExpiresAt.Equal(at(-time.Hour)) || last.UserID != alice.ID {
			t.Errorf("the expired key lists as %+v", last)
		}
		if ci := listed[6]; ci.LastUsedAt == nil || !ci.LastUsedAt.Equal(at(2*time.Minute)) || ci.Permissions != nil {
			t.Errorf("the touched key lists as %+v", ci)
		}
		if got, err := s.ListAPIKeys(ctx, bob.ID); err != nil || keyNames(got) != "bob's" {
			t.Errorf("bob's keys = %+v, %v", got, err)
		}
		carol := mustCreateUser(t, s, "carol@example.com", "Carol")
		if got, err := s.ListAPIKeys(ctx, carol.ID); err != nil || got == nil || len(got) != 0 {
			t.Errorf("a user without keys lists %#v, %v", got, err)
		}
		if _, err := s.ListAPIKeys(ctx, "not a key"); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("the keys of an id that is no user's: %v, want ErrNotFound", err)
		}

		// A user revokes their own live key, once.
		if err := s.RevokeAPIKey(ctx, alice.ID, some.ID, at(10*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if revoked := findAPIKey(t, s, hashOf(2)).APIKey.RevokedAt; revoked == nil || !revoked.Equal(at(10*time.Minute)) {
			t.Errorf("a revoked key's revokedAt = %v", revoked)
		}
		for name, err := range map[string]error{
			"a key revoked already": s.RevokeAPIKey(ctx, alice.ID, some.ID, at(11*time.Minute)),
			"another user's key":    s.RevokeAPIKey(ctx, alice.ID, bobs.ID, at(11*time.Minute)),
			"an unknown key":        s.RevokeAPIKey(ctx, alice.ID, missingKey, at(11*time.Minute)),
			"an id that is no key":  s.RevokeAPIKey(ctx, alice.ID, "not a key", at(11*time.Minute)),
			"an unknown user":       s.RevokeAPIKey(ctx, missingKey, all.ID, at(11*time.Minute)),
			"an id that is no user": s.RevokeAPIKey(ctx, "not a key", all.ID, at(11*time.Minute)),
		} {
			if !errors.Is(err, identity.ErrNotFound) {
				t.Errorf("RevokeAPIKey of %s: %v, want ErrNotFound", name, err)
			}
		}
		if revoked := findAPIKey(t, s, hashOf(2)).APIKey.RevokedAt; !revoked.Equal(at(10 * time.Minute)) {
			t.Errorf("a second revocation moved revokedAt to %v", revoked)
		}
		if findAPIKey(t, s, hashOf(5)).APIKey.RevokedAt != nil || findAPIKey(t, s, hashOf(1)).APIKey.RevokedAt != nil {
			t.Error("a refused revocation revoked a key")
		}
		if listed, _ := s.ListAPIKeys(ctx, alice.ID); strings.Contains(keyNames(listed), "reader") || len(listed) != 7 {
			t.Errorf("after the revocation alice's keys are %s", keyNames(listed))
		}

		// Disabling a user revokes their live keys with their sessions, and
		// enabling brings none back.
		mustSession(t, s, alice, hashOf(20))
		if err := s.SetDisabled(ctx, alice.ID, true, at(20*time.Minute)); err != nil {
			t.Fatal(err)
		}
		rec = findAPIKey(t, s, hashOf(1))
		if !rec.User.Disabled || rec.APIKey.RevokedAt == nil || !rec.APIKey.RevokedAt.Equal(at(20*time.Minute)) {
			t.Errorf("after disabling, the key reads %+v", rec)
		}
		if findSession(t, s, hashOf(20)).Session.RevokedAt == nil {
			t.Error("disabling left the user's session")
		}
		if revoked := findAPIKey(t, s, hashOf(2)).APIKey.RevokedAt; !revoked.Equal(at(10 * time.Minute)) {
			t.Errorf("disabling moved an earlier revocation to %v", revoked)
		}
		if findAPIKey(t, s, hashOf(5)).APIKey.RevokedAt != nil {
			t.Error("disabling alice revoked bob's key")
		}
		if err := s.SetDisabled(ctx, alice.ID, false, at(21*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if rec = findAPIKey(t, s, hashOf(1)); rec.User.Disabled || rec.APIKey.RevokedAt == nil {
			t.Errorf("after enabling, the key reads %+v", rec)
		}
		if listed, err := s.ListAPIKeys(ctx, alice.ID); err != nil || len(listed) != 0 {
			t.Errorf("after disabling alice's keys are %s, %v", keyNames(listed), err)
		}

		// A key's user reads as pending while they wait for approval.
		dave, err := s.CreateUser(ctx, identity.NewUser{Login: "dave@example.com", Name: "Dave", PasswordHash: "x", At: at(0), Pending: true})
		if err != nil {
			t.Fatal(err)
		}
		mustAPIKey(t, s, dave, "early", hashOf(6), nil, at(0))
		if !findAPIKey(t, s, hashOf(6)).User.Pending {
			t.Error("a pending user's key reads its user as approved")
		}

		// A deleted user's keys go with them.
		if _, err := db.db.Exec(db.placeholders(`DELETE FROM "user" WHERE "email" = ?`), "bob@example.com"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FindAPIKey(ctx, hashOf(5)); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("a deleted user's key: %v, want ErrNotFound", err)
		}
		var left int
		if err := db.db.QueryRow(db.placeholders(`SELECT count(*) FROM "user_api_key" WHERE "token_hash" = ?`), hashOf(5)).Scan(&left); err != nil || left != 0 {
			t.Errorf("a deleted user left %d keys, %v", left, err)
		}
	})
}

// withoutAPIKeys is the fixture's descriptor as one written before D114:
// without its apiKey member.
func withoutAPIKeys(t *testing.T) []byte {
	t.Helper()
	return descriptorJSON(t, func(d map[string]any) { delete(d, "apiKey") })
}

// TestStoreWithoutAPIKeys: a descriptor without an API key table gives a
// store that finds no key and whose other key methods say there are none,
// over a database that has no such table.
func TestStoreWithoutAPIKeys(t *testing.T) {
	db := openSQLite(t)
	if _, err := db.Exec(`DROP TABLE "user_api_key"`); err != nil {
		t.Fatal(err)
	}
	s, err := identity.NewSQLStore(db, identity.SQLite, withoutAPIKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	alice := mustCreateUser(t, s, "alice@example.com", "Alice")
	if s.HasAPIKeys() || s.Descriptor().APIKey != nil {
		t.Error("HasAPIKeys without an API key table")
	}
	if _, err := s.FindAPIKey(ctx, hashOf(1)); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("FindAPIKey: %v, want ErrNotFound", err)
	}
	for name, err := range map[string]error{
		"CreateAPIKey": func() error {
			_, err := s.CreateAPIKey(ctx, identity.NewAPIKeyRow{UserID: alice.ID, Name: "ci", TokenHash: hashOf(1), CreatedAt: at(0), ExpiresAt: at(time.Hour)})
			return err
		}(),
		"ListAPIKeys":  func() error { _, err := s.ListAPIKeys(ctx, alice.ID); return err }(),
		"RevokeAPIKey": s.RevokeAPIKey(ctx, alice.ID, missingKey, at(0)),
		"TouchAPIKey":  s.TouchAPIKey(ctx, missingKey, at(0), at(0)),
	} {
		if !errors.Is(err, identity.ErrNoAPIKeys) {
			t.Errorf("%s: %v, want ErrNoAPIKeys", name, err)
		}
	}
	// Disabling revokes the sessions and reads no key table.
	mustSession(t, s, alice, hashOf(2))
	if err := s.SetDisabled(ctx, alice.ID, true, at(time.Minute)); err != nil {
		t.Fatalf("SetDisabled without an API key table: %v", err)
	}
	if findSession(t, s, hashOf(2)).Session.RevokedAt == nil {
		t.Error("disabling left the user's session")
	}
}

// TestDescriptorAPIKey: the descriptor's apiKey member is optional, and
// when present every name of it is there, without a NUL, and no member is
// unknown.
func TestDescriptorAPIKey(t *testing.T) {
	d, err := identity.ParseDescriptor(descriptorJSON(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if k := d.APIKey; k == nil || k.Table != "user_api_key" || k.Columns.ID != "id" || k.Columns.User != "user_id" || k.Columns.Name != "name" ||
		k.Columns.TokenHash != "token_hash" || k.Columns.Permissions != "permissions" || k.Columns.CreatedAt != "created_at" ||
		k.Columns.ExpiresAt != "expires_at" || k.Columns.LastUsedAt != "last_used_at" || k.Columns.RevokedAt != "revoked_at" {
		t.Errorf("the fixture's apiKey reads as %+v", k)
	}
	if d, err := identity.ParseDescriptor(withoutAPIKeys(t)); err != nil || d.APIKey != nil {
		t.Errorf("a descriptor without apiKey reads as %+v, %v", d.APIKey, err)
	}
	apiKey := func(d map[string]any) map[string]any { return d["apiKey"].(map[string]any) }
	columns := func(d map[string]any) map[string]any { return apiKey(d)["columns"].(map[string]any) }
	for name, c := range map[string]struct {
		edit func(map[string]any)
		want string
	}{
		"an empty table":             {func(d map[string]any) { apiKey(d)["table"] = "" }, "apiKey.table is empty"},
		"an empty column":            {func(d map[string]any) { columns(d)["tokenHash"] = "" }, "apiKey.columns.tokenHash is empty"},
		"a column left out":          {func(d map[string]any) { delete(columns(d), "lastUsedAt") }, "apiKey.columns.lastUsedAt is empty"},
		"a NUL in a name":            {func(d map[string]any) { columns(d)["permissions"] = "per\x00missions" }, "apiKey.columns.permissions holds a NUL"},
		"an unknown member":          {func(d map[string]any) { apiKey(d)["type"] = "UserApiKey" }, "unknown field"},
		"an unknown column":          {func(d map[string]any) { columns(d)["usedAt"] = "used_at" }, "unknown field"},
		"a column in another case":   {func(d map[string]any) { columns(d)["TokenHash"] = "token_hash" }, "unknown field"},
		"the member in another case": {func(d map[string]any) { d["apikey"] = d["apiKey"] }, "unknown field"},
	} {
		if _, err := identity.ParseDescriptor(descriptorJSON(t, c.edit)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error naming %q", name, err, c.want)
		}
	}
}

// eachKeyHarness runs fn on a harness over each database, with both sets'
// API key operations turned on.
func eachKeyHarness(t *testing.T, config string, fn func(t *testing.T, h *harness)) {
	t.Helper()
	for _, db := range databases(t) {
		t.Run(string(db.dialect), func(t *testing.T) {
			fn(t, newHarnessWith(t, db, config, true, identity.WithAPIKeys(), identity.WithAPIKeyAdministration()))
		})
	}
}

// createKey makes the caller an API key and returns createApiKey's answer.
func (h *harness) createKey(caller []string, body map[string]any) map[string]any {
	h.t.Helper()
	r := h.do("POST", "/auth/keys", body, caller...)
	h.expect(r, 200, "")
	if token, _ := r.data()["token"].(string); len(token) != identity.APIKeyLength || !strings.HasPrefix(token, identity.APIKeyPrefix) {
		h.t.Fatalf("createApiKey answered the token %q", token)
	}
	return r.data()
}

// keyOf is the bearer header of a createApiKey answer's token.
func keyOf(created map[string]any) []string { return bearer(created["token"].(string)) }

// expectDetail fails unless r is the problem with status, code and detail.
func (h *harness) expectDetail(r reply, status int, code, detail string) {
	h.t.Helper()
	h.expect(r, status, code)
	if r.body["detail"] != detail {
		h.t.Fatalf("got the detail %q, want %q", r.body["detail"], detail)
	}
}

// expectField fails unless r is 400 bad_request refusing field by
// validator.
func (h *harness) expectField(r reply, field, validator string) {
	h.t.Helper()
	h.expect(r, 400, "bad_request")
	errs, _ := r.body["errors"].(map[string]any)
	refusals, _ := errs[field].([]any)
	if len(refusals) != 1 || refusals[0].(map[string]any)["validator"] != validator {
		h.t.Fatalf("got the errors %v, want %s refused by %s", r.body["errors"], field, validator)
	}
}

// names are the names of the keys a list answers, in its order.
func (r reply) names() string {
	var out []string
	for _, k := range r.list() {
		out = append(out, fmt.Sprint(k.(map[string]any)["name"]))
	}
	return strings.Join(out, ",")
}

// TestAPIKeyCreateAndList: createApiKey answers the key's token once, the
// row keeps its hash, and the list shows the key without either; a request
// carrying the key in Authorization is its user's, and writes lastUsedAt
// once per touch interval.
func TestAPIKeyCreateAndList(t *testing.T) {
	eachKeyHarness(t, testConfig, func(t *testing.T, h *harness) {
		member := bearer(h.login("member@example.com", userPassword))
		h.expect(h.do("POST", "/auth/keys", map[string]any{"name": "ci"}), 401, identity.CodeUnauthorized)
		h.expect(h.do("GET", "/auth/keys", nil), 401, identity.CodeUnauthorized)
		if empty := h.do("GET", "/auth/keys", nil, member...); empty.status != 200 || empty.list() == nil || len(empty.list()) != 0 {
			t.Fatalf("a user without keys lists %d %v", empty.status, empty.body)
		}

		created := h.createKey(member, map[string]any{"name": "  ci runner \n"})
		token, id := created["token"].(string), created["id"].(string)
		// A key with every permission has no permissions member, and one
		// never used no lastUsedAt.
		shown := map[string]any{"id": id, "name": "ci runner", "createdAt": "2026-10-08T12:00:00Z", "expiresAt": "2027-10-08T12:00:00Z"}
		if want := map[string]any{"id": id, "name": "ci runner", "createdAt": shown["createdAt"], "expiresAt": shown["expiresAt"], "token": token}; !equalJSON(created, want) {
			t.Errorf("createApiKey = %v, want %v", created, want)
		}
		if id == "" || strings.Contains(id, "-") {
			t.Errorf("the key's id %q is not the base62 form of its Identity.UUID", id)
		}
		rec, err := h.store.FindAPIKey(context.Background(), identity.HashToken(token))
		if err != nil || rec.APIKey.ID != id || rec.User.ID != h.memberID || rec.APIKey.Name != "ci runner" {
			t.Fatalf("the row of the key = %+v, %v", rec, err)
		}

		list := h.do("GET", "/auth/keys", nil, member...)
		h.expect(list, 200, "")
		if !equalJSON(list.list(), []any{shown}) {
			t.Errorf("listApiKeys = %v, want %v alone", list.list(), shown)
		}

		// The key authenticates as its user, in Authorization alone.
		h.clock.Advance(time.Minute)
		me := h.do("GET", "/auth/me", nil, bearer(token)...)
		h.expect(me, 200, "")
		if user := me.data()["user"].(map[string]any); user["id"] != h.memberID || user["login"] != "member@example.com" || user["name"] != "Member" {
			t.Errorf("a key's me = %v", me.data())
		}
		p, err := h.svc.AuthenticateToken(context.Background(), token, identity.TransportBearer)
		if err != nil || p.ID != h.memberID || p.APIKeyID != id || p.SessionID != "" || p.Transport != identity.TransportBearer {
			t.Errorf("a key's principal = %+v, %v", p, err)
		}
		if _, err := h.svc.AuthenticateToken(context.Background(), token, identity.TransportCookie); err == nil {
			t.Error("a key authenticated as a cookie's token")
		}
		inCookie := h.do("GET", "/auth/me", nil, cookieHeader(token)...)
		h.expect(inCookie, 401, identity.CodeUnauthorized)
		if len(inCookie.cookies) != 1 || inCookie.cookies[0].MaxAge != -1 {
			t.Errorf("a cookie carrying a key was not cleared: %v", inCookie.cookies)
		}
		h.expect(h.do("GET", "/auth/me", nil, bearer(token[:len(token)-1]+"!")...), 401, identity.CodeUnauthorized)
		other, err := identity.NewAPIKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		h.expect(h.do("GET", "/auth/me", nil, bearer(other)...), 401, identity.CodeUnauthorized)

		// lastUsedAt is the first request's time, and moves once the touch
		// interval has passed.
		lastUsed := func() any {
			t.Helper()
			l := h.do("GET", "/auth/keys", nil, member...).list()
			return l[len(l)-1].(map[string]any)["lastUsedAt"]
		}
		if got := lastUsed(); got != "2026-10-08T12:01:00Z" {
			t.Errorf("after its first request lastUsedAt = %v", got)
		}
		h.clock.Advance(59 * time.Second)
		h.expect(h.do("GET", "/auth/me", nil, bearer(token)...), 200, "")
		if got := lastUsed(); got != "2026-10-08T12:01:00Z" {
			t.Errorf("a request within the touch interval moved lastUsedAt to %v", got)
		}
		h.clock.Advance(2 * time.Second)
		h.expect(h.do("GET", "/auth/me", nil, bearer(token)...), 200, "")
		if got := lastUsed(); got != "2026-10-08T12:02:01Z" {
			t.Errorf("a request after the touch interval left lastUsedAt at %v", got)
		}

		// Newest first, and each user's own.
		h.clock.Advance(time.Minute)
		h.createKey(member, map[string]any{"name": "second"})
		if got := h.do("GET", "/auth/keys", nil, member...).names(); got != "second,ci runner" {
			t.Errorf("listApiKeys = %s, want the newest first", got)
		}
		admin := bearer(h.login("admin@example.com", adminPassword))
		if got := h.do("GET", "/auth/keys", nil, admin...).list(); len(got) != 0 {
			t.Errorf("the admin lists the member's keys: %v", got)
		}
	})
}

// TestAPIKeyOutlivesTheIdleTimeout: the idle timeout and the session's
// lifetime end a session and no API key, which ends at its own expiry.
func TestAPIKeyOutlivesTheIdleTimeout(t *testing.T) {
	idle := `{"sessionTtlSeconds": 3600, "idleTimeoutSeconds": 600, "touchIntervalSeconds": 60, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	eachKeyHarness(t, idle, func(t *testing.T, h *harness) {
		me := func(credential []string) reply { return h.do("GET", "/auth/me", nil, credential...) }
		signedIn := bearer(h.login("member@example.com", userPassword))
		key := keyOf(h.createKey(signedIn, map[string]any{"name": "ci"}))

		h.clock.Advance(601 * time.Second)
		h.expect(me(signedIn), 401, identity.CodeUnauthorized)
		h.expect(me(key), 200, "")
		// Unused for longer than the idle timeout, and past a session's
		// lifetime.
		h.clock.Advance(2 * time.Hour)
		h.expect(me(key), 200, "")
		h.clock.Advance(364 * 24 * time.Hour)
		h.expect(me(key), 200, "")
		h.clock.Advance(24 * time.Hour)
		h.expect(me(key), 401, identity.CodeUnauthorized)
	})
}

// TestAPIKeyEnds: a key ends at its expiry, when it is revoked, and when
// its user is disabled, for good; a password change, the user's own or an
// administrator's, ends the sessions and not the key.
func TestAPIKeyEnds(t *testing.T) {
	eachKeyHarness(t, testConfig, func(t *testing.T, h *harness) {
		ctx := context.Background()
		me := func(credential []string) reply { return h.do("GET", "/auth/me", nil, credential...) }
		member := bearer(h.login("member@example.com", userPassword))
		admin := bearer(h.login("admin@example.com", adminPassword))

		// Its expiry: the key lives while now is before expiresAt.
		short := h.createKey(member, map[string]any{"name": "short", "expiresInSeconds": 60})
		if short["expiresAt"] != "2026-10-08T12:01:00Z" {
			t.Errorf("a key of 60 seconds expires at %v", short["expiresAt"])
		}
		h.clock.Advance(59 * time.Second)
		h.expect(me(keyOf(short)), 200, "")
		h.clock.Advance(time.Second)
		h.expect(me(keyOf(short)), 401, identity.CodeUnauthorized)
		if got := h.do("GET", "/auth/keys", nil, member...).names(); got != "short" {
			t.Errorf("an expired key is listed: got %q", got)
		}

		// A revocation, which its user makes once.
		revoked := h.createKey(member, map[string]any{"name": "revoked"})
		adminsOwn := h.createKey(admin, map[string]any{"name": "admin's"})
		h.expect(me(keyOf(revoked)), 200, "")
		path := "/auth/keys/" + revoked["id"].(string)
		h.expect(h.do("DELETE", path, nil), 401, identity.CodeUnauthorized)
		h.expectTrue(h.do("DELETE", path, nil, member...))
		h.expect(me(keyOf(revoked)), 401, identity.CodeUnauthorized)
		for name, id := range map[string]string{
			"a key revoked already": revoked["id"].(string),
			"another user's key":    adminsOwn["id"].(string),
			"an unknown key":        missingKey,
			"an id that is no key":  "not-a-key",
		} {
			r := h.do("DELETE", "/auth/keys/"+id, nil, member...)
			if r.status != 404 || r.code() != identity.CodeNotFound || r.body["detail"] != "API key not found" {
				t.Errorf("revoking %s: %d %q %v, want 404 not_found", name, r.status, r.code(), r.body["detail"])
			}
		}
		h.expect(me(keyOf(adminsOwn)), 200, "")
		if got := h.do("GET", "/auth/keys", nil, member...).names(); got != "short" {
			t.Errorf("a revoked key is listed: got %q", got)
		}

		// A password change ends the user's other sessions, not their keys.
		lasting := h.createKey(member, map[string]any{"name": "lasting"})
		other := bearer(h.login("member@example.com", userPassword))
		h.expectTrue(h.do("POST", "/auth/password", map[string]any{"current": userPassword, "password": "a new password"}, member...))
		h.expect(me(other), 401, identity.CodeUnauthorized)
		h.expect(me(keyOf(lasting)), 200, "")
		h.expectTrue(h.do("PUT", "/auth/admin/users/"+h.memberID+"/password", map[string]any{"password": "reset password"}, admin...))
		h.expect(me(member), 401, identity.CodeUnauthorized)
		h.expect(me(keyOf(lasting)), 200, "")

		// Its user disabled: the keys are revoked with the sessions, and
		// enabling brings none back.
		h.expect(h.do("POST", "/auth/admin/users/"+h.memberID+"/disable", nil, admin...), 200, "")
		h.expect(me(keyOf(lasting)), 401, identity.CodeUnauthorized)
		h.expect(h.do("POST", "/auth/admin/users/"+h.memberID+"/enable", nil, admin...), 200, "")
		h.expect(me(keyOf(lasting)), 401, identity.CodeUnauthorized)
		member = bearer(h.login("member@example.com", "reset password"))
		if got := h.do("GET", "/auth/keys", nil, member...).list(); len(got) != 0 {
			t.Errorf("after the disable the member's keys are %v, want none", got)
		}
		h.expect(me(keyOf(adminsOwn)), 200, "")

		// A user who waits for approval is no caller, by a key either.
		pending, err := h.store.CreateUser(ctx, identity.NewUser{Login: "pending@example.com", PasswordHash: "x", At: h.clock.Now(), Pending: true})
		if err != nil {
			t.Fatal(err)
		}
		token, err := identity.NewAPIKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.CreateAPIKey(ctx, identity.NewAPIKeyRow{UserID: pending.ID, Name: "early", TokenHash: identity.HashToken(token), CreatedAt: h.clock.Now(), ExpiresAt: h.clock.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		h.expect(me(bearer(token)), 401, identity.CodeUnauthorized)
		if err := h.store.ApproveUser(ctx, pending.ID); err != nil {
			t.Fatal(err)
		}
		h.expect(me(bearer(token)), 200, "")
	})
}

// TestAPIKeyInput: createApiKey's name is 1 to 100 characters once
// trimmed, its expiresInSeconds 1 to the config's apiKeyMaxTtlSeconds,
// which is the lifetime without it, and its permissions 1 to 100 valid
// names, none twice.
func TestAPIKeyInput(t *testing.T) {
	config := `{"apiKeyMaxTtlSeconds": 3600, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	eachKeyHarness(t, config, func(t *testing.T, h *harness) {
		admin := bearer(h.login("admin@example.com", adminPassword))
		post := func(body any) reply { return h.do("POST", "/auth/keys", body, admin...) }
		made := 0
		create := func(body map[string]any) map[string]any {
			t.Helper()
			made++
			return h.createKey(admin, body)
		}

		if got := create(map[string]any{"name": "default"})["expiresAt"]; got != "2026-10-08T13:00:00Z" {
			t.Errorf("a key without expiresInSeconds expires at %v, want the config's hour", got)
		}
		if got := create(map[string]any{"name": "longest", "expiresInSeconds": 3600})["expiresAt"]; got != "2026-10-08T13:00:00Z" {
			t.Errorf("a key of the longest lifetime expires at %v", got)
		}
		if got := create(map[string]any{"name": "shortest", "expiresInSeconds": 1})["expiresAt"]; got != "2026-10-08T12:00:01Z" {
			t.Errorf("a key of one second expires at %v", got)
		}
		h.expectField(post(map[string]any{"name": "x", "expiresInSeconds": 3601}), "expiresInSeconds", "max")
		h.expectField(post(map[string]any{"name": "x", "expiresInSeconds": 0}), "expiresInSeconds", "min")
		h.expectField(post(map[string]any{"name": "x", "expiresInSeconds": -5}), "expiresInSeconds", "min")

		h.expectField(post(map[string]any{}), "name", "required")
		h.expectField(post(map[string]any{"name": ""}), "name", "required")
		h.expectField(post(map[string]any{"name": " \t\n "}), "name", "required")
		h.expectField(post(map[string]any{"name": strings.Repeat("n", 101)}), "name", "maxLength")
		h.expectField(post(map[string]any{"name": strings.Repeat("\u00e9", 101)}), "name", "maxLength")
		if got := create(map[string]any{"name": strings.Repeat("n", 100)})["name"]; got != strings.Repeat("n", 100) {
			t.Errorf("a name of 100 characters is kept as %v", got)
		}
		// Characters are counted, not bytes, and after the trim.
		if got := create(map[string]any{"name": "  " + strings.Repeat("\u00e9", 100) + "  "})["name"]; got != strings.Repeat("\u00e9", 100) {
			t.Errorf("a padded name of 100 two-byte characters is kept as %v", got)
		}

		h.expectField(post(map[string]any{"name": "x", "permissions": []string{}}), "permissions", "listMin")
		many := make([]string, 101)
		for i := range many {
			many[i] = fmt.Sprintf("orders.p%d", i)
		}
		h.expectField(post(map[string]any{"name": "x", "permissions": many}), "permissions", "listMax")
		if got := create(map[string]any{"name": "hundred", "permissions": many[:100]})["permissions"]; len(got.([]any)) != 100 {
			t.Errorf("a key naming 100 permissions answered %v", got)
		}
		for name, c := range map[string]struct {
			permissions, refused []string
		}{
			"a permission of the wrong form": {[]string{"orders.read", "orders read", "a..b"}, []string{"orders read", "a..b"}},
			"a permission named twice":       {[]string{"orders.read", "orders.write", "orders.read"}, []string{"orders.read"}},
			"an empty permission":            {[]string{""}, []string{""}},
		} {
			r := post(map[string]any{"name": "x", "permissions": c.permissions})
			if r.status != 422 || r.code() != identity.CodeInvalidPermission || !equalJSON(r.body["details"], map[string]any{"permissions": c.refused}) {
				t.Errorf("%s: %d %q %v, want 422 invalid_permission naming %v", name, r.status, r.code(), r.body["details"], c.refused)
			}
		}

		// Every refused field is named at once, before any permission is
		// read.
		several := post(map[string]any{"name": " ", "expiresInSeconds": 0, "permissions": []string{}})
		h.expect(several, 400, "bad_request")
		if errs, _ := several.body["errors"].(map[string]any); len(errs) != 3 {
			t.Errorf("three refused members answered the errors %v", several.body["errors"])
		}
		h.expectField(post(map[string]any{"name": "", "permissions": []string{"orders read"}}), "name", "required")

		// A null optional member is an absent one.
		nulls := create(map[string]any{"name": "nulls", "expiresInSeconds": nil, "permissions": nil})
		if _, has := nulls["permissions"]; has || nulls["expiresAt"] != "2026-10-08T13:00:00Z" {
			t.Errorf("a key made with null members = %v", nulls)
		}

		for name, body := range map[string]any{
			"a fractional lifetime":    map[string]any{"name": "x", "expiresInSeconds": 1.5},
			"a lifetime as a string":   map[string]any{"name": "x", "expiresInSeconds": "60"},
			"a name that is no string": map[string]any{"name": 5},
			"permissions as a string":  map[string]any{"name": "x", "permissions": "orders.read"},
			"an unknown member":        map[string]any{"name": "x", "ttl": 60},
			"a member in another case": map[string]any{"Name": "x"},
			"a body that is a list":    []any{},
		} {
			if r := post(body); r.status != 400 || r.code() != "bad_request" {
				t.Errorf("%s: %d %q, want 400 bad_request", name, r.status, r.code())
			}
		}
		if got := len(h.do("GET", "/auth/keys", nil, admin...).list()); got != made {
			t.Errorf("%d keys are listed, want the %d that were made: a refused create wrote a row", got, made)
		}
	})
}

// TestAPIKeyPermissions: a key made with permissions holds those alone,
// each while a role of its user covers it; a key names only what its user
// holds; and one made with none holds all its user does.
func TestAPIKeyPermissions(t *testing.T) {
	eachKeyHarness(t, testConfig, func(t *testing.T, h *harness) {
		ctx := context.Background()
		admin := bearer(h.login("admin@example.com", adminPassword))
		created := h.createKey(admin, map[string]any{"name": "reader", "permissions": []string{"orders.read", "identity.users.read"}})
		if !equalJSON(created["permissions"], []any{"orders.read", "identity.users.read"}) {
			t.Errorf("createApiKey's permissions = %v", created["permissions"])
		}
		key := keyOf(created)
		if got := h.do("GET", "/auth/keys", nil, admin...).list()[0].(map[string]any)["permissions"]; !equalJSON(got, []any{"orders.read", "identity.users.read"}) {
			t.Errorf("listApiKeys' permissions = %v", got)
		}

		// The admin's role holds identity and orders: the key holds what it
		// names of them, and its routes follow.
		holds := func(roles []string, permissions []any) {
			t.Helper()
			me := h.do("GET", "/auth/me", nil, key...)
			h.expect(me, 200, "")
			if got := roleNames(me.data()["roles"]); !slices.Equal(got, roles) || !equalJSON(me.data()["permissions"], permissions) {
				t.Errorf("the key's me = %v, want the roles %v and the permissions %v", me.data(), roles, permissions)
			}
		}
		holds([]string{"admin"}, []any{"orders.read", "identity.users.read"})
		want := map[string]any{"OrdersList": true, "AuthMe": true, "OrdersCreate": false, "IdentityListUsers": true}
		if got := h.do("GET", "/auth/capabilities", nil, key...).data()["operations"]; !equalJSON(got, want) {
			t.Errorf("the key's capabilities = %v, want %v", got, want)
		}
		h.expect(h.do("GET", "/orders", nil, key...), 200, "")
		h.expect(h.do("POST", "/orders", nil, key...), 403, "")
		h.expect(h.do("GET", "/auth/admin/users", nil, key...), 200, "")
		h.expect(h.do("POST", "/auth/admin/users", map[string]any{"login": "x@example.com", "password": userPassword}, key...), 403, identity.CodeForbidden)
		h.expect(h.do("GET", "/auth/admin/roles", nil, key...), 403, identity.CodeForbidden)

		// The role loses orders, and the key with it; given back, the key
		// holds it again.
		roles, err := h.store.ListRoles(ctx)
		if err != nil || len(roles) != 1 {
			t.Fatalf("the harness's roles = %+v, %v", roles, err)
		}
		rewrite := func(permissions ...string) {
			t.Helper()
			if _, err := h.store.UpdateRole(ctx, roles[0].ID, "admin", permissions); err != nil {
				t.Fatal(err)
			}
		}
		rewrite("identity")
		holds([]string{"admin"}, []any{"identity.users.read"})
		h.expect(h.do("GET", "/orders", nil, key...), 403, identity.CodeForbidden)
		// A role holding less than the key names gives it no part of it.
		rewrite("identity", "orders.read.archive")
		holds([]string{"admin"}, []any{"identity.users.read"})
		rewrite("billing")
		holds(nil, []any{})
		h.expect(h.do("GET", "/auth/admin/users", nil, key...), 403, identity.CodeForbidden)
		rewrite("identity", "orders")
		holds([]string{"admin"}, []any{"orders.read", "identity.users.read"})

		// A key made without permissions holds all its user does.
		whole := h.createKey(admin, map[string]any{"name": "whole"})
		if _, has := whole["permissions"]; has {
			t.Errorf("a key with every permission answered permissions: %v", whole)
		}
		if got := h.do("GET", "/auth/me", nil, keyOf(whole)...).data()["permissions"]; !equalJSON(got, []any{"identity", "orders"}) {
			t.Errorf("a key with every permission holds %v", got)
		}
		h.expect(h.do("POST", "/orders", nil, keyOf(whole)...), 200, "")

		// A key names only what its user holds.
		notHeld := func(caller []string, permissions, uncovered []string) {
			t.Helper()
			r := h.do("POST", "/auth/keys", map[string]any{"name": "wide", "permissions": permissions}, caller...)
			h.expectDetail(r, 403, identity.CodeForbidden, notHeldDetail)
			if !equalJSON(r.body["details"], map[string]any{"permissions": uncovered}) {
				t.Errorf("the refusal's details = %v, want %v", r.body["details"], uncovered)
			}
		}
		member := bearer(h.login("member@example.com", userPassword))
		notHeld(member, []string{"orders.read"}, []string{"orders.read"})
		notHeld(admin, []string{"orders.read", "billing.read"}, []string{"billing.read"})
		reader, err := h.store.CreateRole(ctx, "reader", []string{"orders.read"})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.GrantRole(ctx, h.memberID, reader.ID, h.clock.Now()); err != nil {
			t.Fatal(err)
		}
		notHeld(member, []string{"orders"}, []string{"orders"})
		notHeld(member, []string{"orders.read", "orders.write"}, []string{"orders.write"})
		if got := h.do("GET", "/auth/keys", nil, member...).list(); len(got) != 0 {
			t.Errorf("a refused create wrote the member a key: %v", got)
		}
		narrow := h.createKey(member, map[string]any{"name": "archive", "permissions": []string{"orders.read.archive"}})
		if got := h.do("GET", "/auth/me", nil, keyOf(narrow)...).data()["permissions"]; !equalJSON(got, []any{"orders.read.archive"}) {
			t.Errorf("a key naming a part of what its user holds has %v", got)
		}
		h.expect(h.do("GET", "/orders", nil, keyOf(narrow)...), 403, identity.CodeForbidden)
	})
}

// TestAPIKeyDoesNotActOnItsOwnAccount: a caller an API key authenticated
// neither makes, lists nor revokes keys, signs out nor changes the
// password, while me and capabilities answer it.
func TestAPIKeyDoesNotActOnItsOwnAccount(t *testing.T) {
	eachKeyHarness(t, testConfig, func(t *testing.T, h *harness) {
		member := bearer(h.login("member@example.com", userPassword))
		first := h.createKey(member, map[string]any{"name": "first"})
		key := keyOf(first)
		for _, call := range []struct {
			method, path string
			body         any
		}{
			{"POST", "/auth/keys", map[string]any{"name": "minted"}},
			{"GET", "/auth/keys", nil},
			{"DELETE", "/auth/keys/" + first["id"].(string), nil},
			{"POST", "/auth/logout", nil},
			{"POST", "/auth/password", map[string]any{"current": userPassword, "password": "a new password"}},
			// The refusal comes before a member of the input is checked.
			{"POST", "/auth/keys", map[string]any{"name": ""}},
			{"POST", "/auth/password", map[string]any{"current": userPassword, "password": "short"}},
		} {
			r := h.do(call.method, call.path, call.body, key...)
			if r.status != 403 || r.code() != identity.CodeForbidden || r.body["detail"] != sessionOnlyDetail {
				t.Errorf("%s %s by a key: %d %q %v, want 403 forbidden", call.method, call.path, r.status, r.code(), r.body["detail"])
			}
		}

		// A body that is not the operation's input is refused as one first.
		h.expect(h.do("POST", "/auth/keys", map[string]any{"title": "minted"}, key...), 400, "bad_request")

		// Nothing was done, and the key is a caller still.
		h.expect(h.do("GET", "/auth/me", nil, key...), 200, "")
		caps := h.do("GET", "/auth/capabilities", nil, key...)
		h.expect(caps, 200, "")
		if want := map[string]any{"OrdersList": true, "AuthMe": true, "OrdersCreate": false, "IdentityListUsers": false}; !equalJSON(caps.data()["operations"], want) {
			t.Errorf("the key's capabilities = %v, want %v", caps.data()["operations"], want)
		}
		if got := h.do("GET", "/auth/keys", nil, member...).names(); got != "first" {
			t.Errorf("the member's keys are %q, want first alone", got)
		}
		h.login("member@example.com", userPassword)

		// The service refuses the same principal whoever calls it.
		p, err := h.svc.AuthenticateToken(context.Background(), first["token"].(string), identity.TransportBearer)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.svc.Logout(context.Background(), p); err == nil {
			t.Error("Logout took a key's principal")
		}
		if _, err := h.svc.CreateAPIKey(context.Background(), identity.Principal{}, identity.CreateAPIKeyInput{Name: "x"}); err == nil {
			t.Error("CreateAPIKey took a caller who is not authenticated")
		}
	})
}

// TestAPIKeyAdministration: a caller with users.read lists any user's
// keys and one with users.write revokes them, by a session or by a key
// that holds the permission; no one else does.
func TestAPIKeyAdministration(t *testing.T) {
	eachKeyHarness(t, testConfig, func(t *testing.T, h *harness) {
		admin := bearer(h.login("admin@example.com", adminPassword))
		member := bearer(h.login("member@example.com", userPassword))
		one := h.createKey(member, map[string]any{"name": "one"})
		h.clock.Advance(time.Minute)
		two := h.createKey(member, map[string]any{"name": "two"})
		adminsOwn := h.createKey(admin, map[string]any{"name": "admin's", "permissions": []string{"identity.users"}})
		keys := "/auth/admin/users/" + h.memberID + "/keys"

		list := h.do("GET", keys, nil, admin...)
		h.expect(list, 200, "")
		want := []any{
			map[string]any{"id": two["id"], "name": "two", "createdAt": "2026-10-08T12:01:00Z", "expiresAt": "2027-10-08T12:01:00Z"},
			map[string]any{"id": one["id"], "name": "one", "createdAt": "2026-10-08T12:00:00Z", "expiresAt": "2027-10-08T12:00:00Z"},
		}
		if !equalJSON(list.list(), want) {
			t.Errorf("listUserApiKeys = %v, want %v", list.list(), want)
		}
		h.expect(h.do("GET", keys, nil, member...), 403, identity.CodeForbidden)
		h.expect(h.do("GET", keys, nil), 401, identity.CodeUnauthorized)
		h.expectDetail(h.do("GET", "/auth/admin/users/"+missingKey+"/keys", nil, admin...), 404, identity.CodeNotFound, "User not found")
		h.expectDetail(h.do("GET", "/auth/admin/users/not-a-key/keys", nil, admin...), 404, identity.CodeNotFound, "User not found")
		if got := h.do("GET", "/auth/admin/users/"+h.adminID+"/keys", nil, admin...).names(); got != "admin's" {
			t.Errorf("the admin's keys = %q", got)
		}

		h.expect(h.do("DELETE", keys+"/"+one["id"].(string), nil, member...), 403, identity.CodeForbidden)
		h.expect(h.do("DELETE", keys+"/"+one["id"].(string), nil), 401, identity.CodeUnauthorized)
		h.expect(h.do("GET", "/auth/me", nil, keyOf(one)...), 200, "")
		h.expectTrue(h.do("DELETE", keys+"/"+one["id"].(string), nil, admin...))
		h.expect(h.do("GET", "/auth/me", nil, keyOf(one)...), 401, identity.CodeUnauthorized)
		for name, path := range map[string]string{
			"a key revoked already":       keys + "/" + one["id"].(string),
			"another user's key":          "/auth/admin/users/" + h.adminID + "/keys/" + two["id"].(string),
			"an unknown key":              keys + "/" + missingKey,
			"an id that is no key":        keys + "/not-a-key",
			"a key of an unknown user":    "/auth/admin/users/" + missingKey + "/keys/" + two["id"].(string),
			"a key of an id that is none": "/auth/admin/users/not-a-key/keys/" + two["id"].(string),
		} {
			r := h.do("DELETE", path, nil, admin...)
			if r.status != 404 || r.code() != identity.CodeNotFound || r.body["detail"] != "API key not found" {
				t.Errorf("revoking %s: %d %q %v, want 404 not_found", name, r.status, r.code(), r.body["detail"])
			}
		}
		h.expect(h.do("GET", "/auth/me", nil, keyOf(two)...), 200, "")
		if got := h.do("GET", keys, nil, admin...).names(); got != "two" {
			t.Errorf("after the revocation the member's keys are %q", got)
		}

		// users.read lists and does not revoke.
		auditor := h.do("POST", "/auth/admin/roles", map[string]any{"name": "auditor", "permissions": []string{"identity.users.read"}}, admin...)
		h.expect(auditor, 200, "")
		h.expect(h.do("PUT", "/auth/admin/users/"+h.memberID+"/roles/"+auditor.data()["id"].(string), nil, admin...), 200, "")
		adminKeys := "/auth/admin/users/" + h.adminID + "/keys"
		h.expect(h.do("GET", adminKeys, nil, member...), 200, "")
		h.expect(h.do("DELETE", adminKeys+"/"+adminsOwn["id"].(string), nil, member...), 403, identity.CodeForbidden)

		// A key that holds the permission administers as a session does:
		// only the routes of its own account are a session's alone.
		h.expect(h.do("GET", keys, nil, keyOf(adminsOwn)...), 200, "")
		h.expectTrue(h.do("DELETE", keys+"/"+two["id"].(string), nil, keyOf(adminsOwn)...))
		h.expect(h.do("GET", "/auth/me", nil, keyOf(two)...), 401, identity.CodeUnauthorized)
		// A key the member's role does not reach revokes nothing.
		h.expect(h.do("DELETE", adminKeys+"/"+adminsOwn["id"].(string), nil, keyOf(h.createKey(member, map[string]any{"name": "three"}))...), 403, identity.CodeForbidden)
	})
}

// TestAPIKeysWithoutTheTable: over a store whose descriptor names no API
// key table, on a database without one, a bearer API key is no credential
// and every key operation is 404, after its caller is admitted.
func TestAPIKeysWithoutTheTable(t *testing.T) {
	for _, db := range databases(t) {
		t.Run(string(db.dialect), func(t *testing.T) {
			if _, err := db.db.Exec(`DROP TABLE "user_api_key"`); err != nil {
				t.Fatal(err)
			}
			h := newHarnessOver(t, db, withoutAPIKeys(t), testConfig, true, identity.WithAPIKeys(), identity.WithAPIKeyAdministration())
			if h.store.HasAPIKeys() {
				t.Fatal("the store has API keys")
			}
			key, err := identity.NewAPIKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			h.expect(h.do("GET", "/auth/me", nil, bearer(key)...), 401, identity.CodeUnauthorized)

			member := bearer(h.login("member@example.com", userPassword))
			admin := bearer(h.login("admin@example.com", adminPassword))
			user := "/auth/admin/users/" + h.memberID
			for _, call := range []struct {
				method, path string
				body         any
				caller       []string
			}{
				{"POST", "/auth/keys", map[string]any{"name": "ci"}, member},
				{"POST", "/auth/keys", map[string]any{"name": ""}, member},
				{"GET", "/auth/keys", nil, member},
				{"DELETE", "/auth/keys/" + missingKey, nil, member},
				{"GET", user + "/keys", nil, admin},
				{"GET", "/auth/admin/users/" + missingKey + "/keys", nil, admin},
				{"DELETE", user + "/keys/" + missingKey, nil, admin},
			} {
				r := h.do(call.method, call.path, call.body, call.caller...)
				if r.status != 404 || r.code() != identity.CodeNotFound || r.body["detail"] != noAPIKeysDetail {
					t.Errorf("%s %s: %d %q %v, want 404 not_found", call.method, call.path, r.status, r.code(), r.body["detail"])
				}
			}
			h.expect(h.do("GET", "/auth/keys", nil), 401, identity.CodeUnauthorized)
			h.expect(h.do("GET", user+"/keys", nil, member...), 403, identity.CodeForbidden)
			h.expect(h.do("DELETE", user+"/keys/"+missingKey, nil, member...), 403, identity.CodeForbidden)

			// The user model runs as it did: a disable revokes the sessions
			// and reads no key table.
			h.expect(h.do("POST", user+"/disable", nil, admin...), 200, "")
			h.expect(h.do("GET", "/auth/me", nil, member...), 401, identity.CodeUnauthorized)
		})
	}
}

// TestAPIKeyRoutesAreOptIn: a set has its API key routes only when the
// service turns them on for it, and a key authenticates whether they are
// on or not.
func TestAPIKeyRoutesAreOptIn(t *testing.T) {
	h := newHarness(t, testDB{dialect: identity.SQLite, db: openSQLite(t)}, testConfig)
	paths := func(routes []routing.Route) []string {
		var out []string
		for _, r := range routes {
			if strings.Contains(r.Path, "/keys") {
				out = append(out, r.Method+" "+r.Path)
			}
		}
		return out
	}
	sessions, administration := &ir.UserSessionsConfig{Register: true}, &ir.UserAdministrationConfig{}
	own := []string{"POST /auth/keys", "GET /auth/keys", "DELETE /auth/keys/{id}"}
	users := []string{"GET /auth/admin/users/{id}/keys", "DELETE /auth/admin/users/{id}/keys/{keyId}"}

	if h.svc.APIKeys() || h.svc.APIKeyAdministration() {
		t.Error("the API key operations are on without their options")
	}
	if got := paths(h.svc.Routes(sessions, administration)); len(got) != 0 {
		t.Errorf("without the options the routes hold %v", got)
	}
	member := h.login("member@example.com", userPassword)
	admin := h.login("admin@example.com", adminPassword)
	for _, r := range []reply{
		h.do("POST", "/auth/keys", map[string]any{"name": "ci"}, bearer(member)...),
		h.do("GET", "/auth/keys", nil, bearer(member)...),
		h.do("DELETE", "/auth/keys/"+missingKey, nil, bearer(member)...),
		h.do("GET", "/auth/admin/users/"+h.memberID+"/keys", nil, bearer(admin)...),
		h.do("DELETE", "/auth/admin/users/"+h.memberID+"/keys/"+missingKey, nil, bearer(admin)...),
	} {
		// The router's own answer, for a route it does not have.
		if r.status != http.StatusNotFound && r.status != http.StatusMethodNotAllowed || r.code() != "" {
			t.Errorf("an API key route that is not on answered %d %q %v", r.status, r.code(), r.body)
		}
	}

	// Authentication by a key follows the table, not the routes.
	p, err := h.svc.AuthenticateToken(context.Background(), member, identity.TransportBearer)
	if err != nil {
		t.Fatal(err)
	}
	created, err := h.svc.CreateAPIKey(context.Background(), p, identity.CreateAPIKeyInput{Name: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	h.expect(h.do("GET", "/auth/me", nil, bearer(created.Token)...), 200, "")

	build := func(opts ...identity.Option) *identity.Service {
		t.Helper()
		svc, err := identity.New(h.store, h.svc.Config(), opts...)
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	onlyOwn := build(identity.WithAPIKeys())
	if got := paths(onlyOwn.Routes(sessions, administration)); !slices.Equal(got, own) || !onlyOwn.APIKeys() || onlyOwn.APIKeyAdministration() {
		t.Errorf("WithAPIKeys routes %v, want %v", got, own)
	}
	onlyUsers := build(identity.WithAPIKeyAdministration())
	if got := paths(onlyUsers.Routes(sessions, administration)); !slices.Equal(got, users) || onlyUsers.APIKeys() || !onlyUsers.APIKeyAdministration() {
		t.Errorf("WithAPIKeyAdministration routes %v, want %v", got, users)
	}
	both := build(identity.WithAPIKeys(), identity.WithAPIKeyAdministration())
	if got := paths(both.Routes(sessions, administration)); !slices.Equal(got, slices.Concat(own, users)) {
		t.Errorf("both options route %v", got)
	}
	if got := len(both.Routes(sessions, administration)); got != len(identity.Operations) {
		t.Errorf("every set with its API keys routes %d operations, want %d", got, len(identity.Operations))
	}
	// A set the API does not declare has no route, its keys' included.
	if got := paths(both.Routes(nil, administration)); !slices.Equal(got, users) {
		t.Errorf("without the session set the routes hold %v", got)
	}
	if got := paths(both.Routes(sessions, nil)); !slices.Equal(got, own) {
		t.Errorf("without the administration set the routes hold %v", got)
	}
	// The routes follow each set's path, and login: false leaves them.
	got := paths(both.Routes(&ir.UserSessionsConfig{Path: "account", NoLogin: true}, &ir.UserAdministrationConfig{Path: "/manage/"}))
	want := []string{"POST /account/keys", "GET /account/keys", "DELETE /account/keys/{id}", "GET /manage/users/{id}/keys", "DELETE /manage/users/{id}/keys/{keyId}"}
	if !slices.Equal(got, want) {
		t.Errorf("under the sets' paths the routes hold %v, want %v", got, want)
	}

	for _, op := range identity.Operations {
		isKey := slices.Contains([]string{identity.OpCreateAPIKey, identity.OpListAPIKeys, identity.OpRevokeAPIKey, identity.OpListUserAPIKeys, identity.OpRevokeUserAPIKey}, op.Name)
		if op.APIKeys != isKey {
			t.Errorf("operation %s has APIKeys %v", op.Name, op.APIKeys)
		}
		if isKey && op.Administration != (op.Name == identity.OpListUserAPIKeys || op.Name == identity.OpRevokeUserAPIKey) {
			t.Errorf("operation %s is in the wrong set", op.Name)
		}
	}
}

// TestAPIKeyHandlersWithoutTheOptions: a service has a handler for each
// API key operation whether its options are on or not, since a generated
// server mounts the operations its schema declares one by one and never
// reads Routes. The handlers read the key's id as {id} under keys, and the
// user's as {id} and the key's as {keyId} under users.
func TestAPIKeyHandlersWithoutTheOptions(t *testing.T) {
	h := newHarness(t, testDB{dialect: identity.SQLite, db: openSQLite(t)}, testConfig)
	r := chi.NewRouter()
	for _, route := range []struct{ method, path, op string }{
		{"POST", "/api/account/keys", identity.OpCreateAPIKey},
		{"GET", "/api/account/keys", identity.OpListAPIKeys},
		{"DELETE", "/api/account/keys/{id}", identity.OpRevokeAPIKey},
		{"GET", "/api/admin/users/{id}/keys", identity.OpListUserAPIKeys},
		{"DELETE", "/api/admin/users/{id}/keys/{keyId}", identity.OpRevokeUserAPIKey},
		{"POST", "/auth/login", ir.IdentityOpLogin},
	} {
		handler, ok := h.svc.Handler(route.op)
		if !ok {
			t.Fatalf("no handler for %s on a service built without the API key options", route.op)
		}
		r.Method(route.method, route.path, handler)
	}
	h.router = r
	member := bearer(h.login("member@example.com", userPassword))
	admin := bearer(h.login("admin@example.com", adminPassword))
	post := func(name string) map[string]any {
		t.Helper()
		created := h.do("POST", "/api/account/keys", map[string]any{"name": name}, member...)
		h.expect(created, 200, "")
		return created.data()
	}
	one, two := post("one"), post("two")
	if got := len(h.do("GET", "/api/account/keys", nil, member...).list()); got != 2 {
		t.Errorf("listApiKeys answered %d keys, want 2", got)
	}
	h.expectTrue(h.do("DELETE", "/api/account/keys/"+one["id"].(string), nil, member...))
	users := "/api/admin/users/" + h.memberID + "/keys"
	if got := h.do("GET", users, nil, admin...).names(); got != "two" {
		t.Errorf("listUserApiKeys = %q, want two", got)
	}
	// The user's id and the key's are not read one for the other.
	h.expect(h.do("DELETE", "/api/admin/users/"+two["id"].(string)+"/keys/"+h.memberID, nil, admin...), 404, identity.CodeNotFound)
	h.expectTrue(h.do("DELETE", users+"/"+two["id"].(string), nil, admin...))
	if got := h.do("GET", users, nil, admin...).list(); len(got) != 0 {
		t.Errorf("after both revocations the member's keys are %v", got)
	}
}

// TestAPIKeyHandlersOnServeMux: the key handlers read {id} and {keyId}
// from the standard library's mux as well as chi's.
func TestAPIKeyHandlersOnServeMux(t *testing.T) {
	h := newHarnessWith(t, testDB{dialect: identity.SQLite, db: openSQLite(t)}, testConfig, true, identity.WithAPIKeys(), identity.WithAPIKeyAdministration())
	mux := http.NewServeMux()
	for _, r := range h.svc.Routes(&ir.UserSessionsConfig{Register: true}, &ir.UserAdministrationConfig{}) {
		mux.Handle(r.Method+" "+r.Path, r.Handler)
	}
	h.router = mux
	member := bearer(h.login("member@example.com", userPassword))
	admin := bearer(h.login("admin@example.com", adminPassword))
	one, two := h.createKey(member, map[string]any{"name": "one"}), h.createKey(member, map[string]any{"name": "two"})
	h.expectTrue(h.do("DELETE", "/auth/keys/"+one["id"].(string), nil, member...))
	if got := h.do("GET", "/auth/admin/users/"+h.memberID+"/keys", nil, admin...).names(); got != "two" {
		t.Errorf("listUserApiKeys on a ServeMux = %q", got)
	}
	h.expectTrue(h.do("DELETE", "/auth/admin/users/"+h.memberID+"/keys/"+two["id"].(string), nil, admin...))
	if got := h.do("GET", "/auth/keys", nil, member...).list(); len(got) != 0 {
		t.Errorf("after both revocations the member's keys are %v", got)
	}
}

// TestNewAPIKey: an API key is its prefix and 43 characters of base64url,
// 47 in all, two differ, and its hash is the whole text's.
func TestNewAPIKey(t *testing.T) {
	a, err := identity.NewAPIKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := identity.NewAPIKey(nil)
	if identity.APIKeyPrefix != "key_" || identity.APIKeyLength != 47 {
		t.Errorf("the prefix is %q and the length %d", identity.APIKeyPrefix, identity.APIKeyLength)
	}
	if a == b || len(a) != identity.APIKeyLength || !strings.HasPrefix(a, identity.APIKeyPrefix) || strings.ContainsAny(a, "+/=") {
		t.Errorf("keys %q and %q", a, b)
	}
	if h := identity.HashToken(a); len(h) != 64 || h == identity.HashToken(strings.TrimPrefix(a, identity.APIKeyPrefix)) {
		t.Errorf("the key's hash %q is not its whole text's", h)
	}
	if _, err := identity.NewAPIKey(strings.NewReader("short")); err == nil {
		t.Error("a key from too few random bytes")
	}
}

// TestNarrowRoles: a key made with permissions holds each role narrowed to
// them, in the roles' order, and no role it holds nothing of.
func TestNarrowRoles(t *testing.T) {
	roles := []session.Role{
		{ID: "1", Name: "clerk", Permissions: []string{"orders", "stock.read"}},
		{ID: "2", Name: "auditor", Permissions: []string{"audit.read"}},
		{ID: "3", Name: "reader", Permissions: []string{"orders.read"}},
	}
	got := identity.NarrowRoles(roles, []string{"orders.read", "stock", "orders.read"})
	want := []session.Role{
		{ID: "1", Name: "clerk", Permissions: []string{"orders.read"}},
		{ID: "3", Name: "reader", Permissions: []string{"orders.read"}},
	}
	if !equalJSON(got, want) {
		t.Errorf("NarrowRoles = %+v, want %+v", got, want)
	}
	if got := identity.EffectivePermissions(got); !slices.Equal(got, []string{"orders.read"}) {
		t.Errorf("the narrowed roles' permissions = %v", got)
	}
	for name, named := range map[string][]string{"nothing held": {"billing"}, "nothing named": {}} {
		if got := identity.NarrowRoles(roles, named); got == nil || len(got) != 0 {
			t.Errorf("%s: NarrowRoles = %#v, want no role", name, got)
		}
	}
	if roles[0].Permissions[0] != "orders" {
		t.Error("NarrowRoles rewrote the roles it was given")
	}
}
