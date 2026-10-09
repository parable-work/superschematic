package identity_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/parable-work/superschematic/runtime/http/go/identity"
)

// fixtureDir holds fixture-user-model-db's identity descriptor and DDL
// (runtime/http/testdata/identity).
var fixtureDir = filepath.Join("..", "..", "testdata", "identity")

// postgresURLEnv names the Postgres the store tests also run against; they
// run on SQLite alone without it.
const postgresURLEnv = "SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL"

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// descriptorJSON is the fixture's descriptor, edited by edit when it is not
// nil.
func descriptorJSON(t *testing.T, edit func(map[string]any)) []byte {
	t.Helper()
	data := readFixture(t, "fixture-user-model-db.json")
	if edit == nil {
		return data
	}
	var d map[string]any
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	edit(d)
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// testDB is a database holding the fixture's tables.
type testDB struct {
	dialect identity.Dialect
	db      *sql.DB
}

// databases are the databases a store test runs on: SQLite always, and
// Postgres when postgresURLEnv names one.
func databases(t *testing.T) []testDB {
	t.Helper()
	dbs := []testDB{{dialect: identity.SQLite, db: openSQLite(t)}}
	if url := os.Getenv(postgresURLEnv); url != "" {
		dbs = append(dbs, testDB{dialect: identity.Postgres, db: openPostgres(t, url)})
	}
	return dbs
}

// eachDatabase runs fn as a subtest on each database, with a store built
// from the fixture's descriptor.
func eachDatabase(t *testing.T, fn func(t *testing.T, db testDB, store *identity.SQLStore)) {
	t.Helper()
	if os.Getenv(postgresURLEnv) == "" {
		t.Logf("%s is unset: the store runs on SQLite alone", postgresURLEnv)
	}
	for _, db := range databases(t) {
		t.Run(string(db.dialect), func(t *testing.T) {
			store, err := identity.NewSQLStore(db.db, db.dialect, descriptorJSON(t, nil))
			if err != nil {
				t.Fatal(err)
			}
			fn(t, db, store)
		})
	}
}

// openSQLite opens a SQLite file of its own with foreign keys on and the
// fixture's sqlite/create.sql applied.
func openSQLite(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "identity.sqlite")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(string(readFixture(t, "sqlite/create.sql"))); err != nil {
		t.Fatalf("apply sqlite/create.sql: %v", err)
	}
	return db
}

// openPostgres opens a pool whose search path is a schema of its own that
// holds the fixture's create.sql, and drops the schema when the test ends.
func openPostgres(t *testing.T, url string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect to %s: %v", postgresURLEnv, err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	// An extension's name is unique in the database, so create the DDL's
	// extensions once in public, where every schema's search path finds
	// them, before test packages running at once each try to create them.
	for _, ext := range []string{"pgcrypto", "citext"} {
		if _, err := admin.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS "+ext+" SCHEMA public"); err != nil && !strings.Contains(err.Error(), "23505") {
			t.Fatalf("create %s: %v", ext, err)
		}
	}
	schema := fmt.Sprintf("identity_store_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	config, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(string(readFixture(t, "create.sql"))); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	return db
}

// at is a fixed instant plus d, to the millisecond, as both dialects keep it.
func at(d time.Duration) time.Time {
	return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC).Add(d)
}

func mustCreateUser(t *testing.T, s identity.Store, login, name string) identity.User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), identity.NewUser{Login: login, Name: name, PasswordHash: "hash-of-" + login, At: at(0)})
	if err != nil {
		t.Fatalf("create %s: %v", login, err)
	}
	return u
}

func mustSession(t *testing.T, s identity.Store, user identity.User, hash string) identity.Session {
	t.Helper()
	sess, err := s.CreateSession(context.Background(), identity.NewSession{UserID: user.ID, TokenHash: hash, CreatedAt: at(0), ExpiresAt: at(time.Hour)})
	if err != nil {
		t.Fatalf("create a session: %v", err)
	}
	return sess
}

func findSession(t *testing.T, s identity.Store, hash string) identity.SessionRecord {
	t.Helper()
	rec, err := s.FindSession(context.Background(), hash)
	if err != nil {
		t.Fatalf("find session %s: %v", hash, err)
	}
	return rec
}

func hashOf(n int) string { return identity.HashToken(fmt.Sprintf("token-%d", n)) }

// TestStoreUsers: a user is created with their credential, keyed by the
// database, found by a login in any case, and listed by login; a taken
// login and one the scalar refuses are told apart.
func TestStoreUsers(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db testDB, s *identity.SQLStore) {
		ctx := context.Background()
		alice := mustCreateUser(t, s, " Alice@Example.COM ", "Alice")
		if alice.Login != "alice@example.com" || alice.Name != "Alice" || alice.ID == "" || alice.Disabled {
			t.Fatalf("created %+v", alice)
		}
		if strings.Contains(alice.ID, "-") {
			t.Errorf("the user's id %q is not the base62 form of its Identity.UUID", alice.ID)
		}
		bob := mustCreateUser(t, s, "bob@example.com", "")
		if bob.Name != "bob@example.com" {
			t.Errorf("a user created without a name is named %q, want the login", bob.Name)
		}

		if _, err := s.CreateUser(ctx, identity.NewUser{Login: "ALICE@example.com", Name: "Again", PasswordHash: "x", At: at(0)}); !errors.Is(err, identity.ErrLoginTaken) {
			t.Errorf("a second user whose login differs in case: %v, want ErrLoginTaken", err)
		}
		var invalid *identity.InvalidLoginError
		if _, err := s.CreateUser(ctx, identity.NewUser{Login: "not an email", PasswordHash: "x", At: at(0)}); !errors.As(err, &invalid) {
			t.Errorf("a login the scalar refuses: %v, want an InvalidLoginError", err)
		}
		// The fixture's display name is an Identity.Name, a VARCHAR(80): a
		// longer one is refused by its scalar before any write.
		var invalidName *identity.InvalidNameError
		if _, err := s.CreateUser(ctx, identity.NewUser{Login: "dave@example.com", Name: strings.Repeat("d", 81), PasswordHash: "x", At: at(0)}); !errors.As(err, &invalidName) {
			t.Errorf("a name the name scalar refuses: %v, want an InvalidNameError", err)
		}
		if _, err := s.FindLogin(ctx, "dave@example.com"); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("a refused name left a user behind: %v", err)
		}

		rec, err := s.FindLogin(ctx, "ALICE@EXAMPLE.com")
		if err != nil || rec.User.ID != alice.ID || rec.PasswordHash != "hash-of- Alice@Example.COM " {
			t.Fatalf("FindLogin in another case = %+v, %v", rec, err)
		}
		if _, err := s.FindLogin(ctx, "carol@example.com"); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("an unknown login: %v, want ErrNotFound", err)
		}
		if _, err := s.FindLogin(ctx, "carol"); !errors.As(err, &invalid) {
			t.Errorf("FindLogin of a login the scalar refuses: %v, want an InvalidLoginError", err)
		}
		if rec, err := s.FindCredential(ctx, bob.ID); err != nil || rec.User.Login != "bob@example.com" {
			t.Errorf("FindCredential = %+v, %v", rec, err)
		}

		got, err := s.GetUser(ctx, alice.ID)
		if err != nil || got.ID != alice.ID || got.Login != alice.Login || len(got.Roles) != 0 {
			t.Fatalf("GetUser = %+v, %v", got, err)
		}
		for _, id := range []string{"not a key", "00000000-0000-4000-8000-000000000000", "1"} {
			if _, err := s.GetUser(ctx, id); !errors.Is(err, identity.ErrNotFound) {
				t.Errorf("GetUser(%q) = %v, want ErrNotFound", id, err)
			}
		}

		users, err := s.ListUsers(ctx)
		if err != nil || len(users) != 2 || users[0].ID != alice.ID || users[1].ID != bob.ID {
			t.Fatalf("ListUsers = %+v, %v; want alice then bob", users, err)
		}
	})
}

// TestStoreSessions: a session is found by its token hash with its user,
// touched once per interval, and revoked alone or with the user's others.
func TestStoreSessions(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db testDB, s *identity.SQLStore) {
		ctx := context.Background()
		alice := mustCreateUser(t, s, "alice@example.com", "Alice")
		one, two, three := mustSession(t, s, alice, hashOf(1)), mustSession(t, s, alice, hashOf(2)), mustSession(t, s, alice, hashOf(3))

		rec := findSession(t, s, hashOf(1))
		if rec.Session.ID != one.ID || rec.User.ID != alice.ID || rec.User.Name != "Alice" || rec.User.Login != "alice@example.com" {
			t.Fatalf("FindSession = %+v", rec)
		}
		if !rec.Session.CreatedAt.Equal(at(0)) || !rec.Session.ExpiresAt.Equal(at(time.Hour)) || rec.Session.LastSeenAt != nil || rec.Session.RevokedAt != nil {
			t.Fatalf("a new session reads as %+v", rec.Session)
		}
		if _, err := s.FindSession(ctx, hashOf(9)); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("an unknown token hash: %v, want ErrNotFound", err)
		}

		// The first touch writes lastSeenAt; one within the interval does
		// not; one after it does.
		touch := func(when time.Time) *time.Time {
			t.Helper()
			if err := s.TouchSession(ctx, one.ID, when, when.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			return findSession(t, s, hashOf(1)).Session.LastSeenAt
		}
		if seen := touch(at(time.Second)); seen == nil || !seen.Equal(at(time.Second)) {
			t.Fatalf("after the first touch lastSeenAt = %v", seen)
		}
		if seen := touch(at(30 * time.Second)); !seen.Equal(at(time.Second)) {
			t.Errorf("a touch within the interval moved lastSeenAt to %v", seen)
		}
		if seen := touch(at(2 * time.Minute)); !seen.Equal(at(2 * time.Minute)) {
			t.Errorf("a touch after the interval left lastSeenAt at %v", seen)
		}

		if err := s.RevokeSession(ctx, one.ID, at(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeSession(ctx, one.ID, at(4*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if revoked := findSession(t, s, hashOf(1)).Session.RevokedAt; revoked == nil || !revoked.Equal(at(3*time.Minute)) {
			t.Errorf("a revoked session's revokedAt = %v, want the first revocation's", revoked)
		}
		if err := s.RevokeUserSessions(ctx, alice.ID, three.ID, at(5*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if findSession(t, s, hashOf(2)).Session.RevokedAt == nil {
			t.Errorf("RevokeUserSessions kept session %s", two.ID)
		}
		if findSession(t, s, hashOf(3)).Session.RevokedAt != nil {
			t.Errorf("RevokeUserSessions revoked the session it keeps")
		}
		if db.dialect == identity.SQLite {
			var created string
			if err := db.db.QueryRow(`SELECT "created_at" FROM "session" WHERE "token_hash" = ?`, hashOf(1)).Scan(&created); err != nil {
				t.Fatal(err)
			}
			if created != "2026-10-08T12:00:00.000Z" {
				t.Errorf("SQLite keeps createdAt as %q, want %s", created, identity.SQLiteTimeLayout)
			}
		}
	})
}

// TestStoreCredentials: setting a password revokes the user's sessions but
// the one kept, a rehash replaces only the hash it read, and disabling a
// user revokes their sessions in the same transaction.
func TestStoreCredentials(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db testDB, s *identity.SQLStore) {
		ctx := context.Background()
		alice := mustCreateUser(t, s, "alice@example.com", "Alice")
		keep, other := mustSession(t, s, alice, hashOf(1)), mustSession(t, s, alice, hashOf(2))

		if err := s.SetPassword(ctx, alice.ID, "new-hash", at(time.Minute), keep.ID); err != nil {
			t.Fatal(err)
		}
		if rec, _ := s.FindLogin(ctx, "alice@example.com"); rec.PasswordHash != "new-hash" {
			t.Errorf("after SetPassword the hash is %q", rec.PasswordHash)
		}
		if findSession(t, s, hashOf(1)).Session.RevokedAt != nil || findSession(t, s, hashOf(2)).Session.RevokedAt == nil {
			t.Errorf("SetPassword keeping %s: sessions %s and %s read %v and %v", keep.ID, keep.ID, other.ID,
				findSession(t, s, hashOf(1)).Session.RevokedAt, findSession(t, s, hashOf(2)).Session.RevokedAt)
		}

		if err := s.RehashPassword(ctx, alice.ID, "stale-hash", "rehashed"); err != nil {
			t.Fatal(err)
		}
		if rec, _ := s.FindLogin(ctx, "alice@example.com"); rec.PasswordHash != "new-hash" {
			t.Errorf("a rehash from a hash no longer stored wrote %q", rec.PasswordHash)
		}
		if err := s.RehashPassword(ctx, alice.ID, "new-hash", "rehashed"); err != nil {
			t.Fatal(err)
		}
		if rec, _ := s.FindLogin(ctx, "alice@example.com"); rec.PasswordHash != "rehashed" {
			t.Errorf("a rehash left the hash %q", rec.PasswordHash)
		}

		if err := s.SetDisabled(ctx, alice.ID, true, at(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		rec := findSession(t, s, hashOf(1))
		if !rec.User.Disabled || rec.Session.RevokedAt == nil {
			t.Errorf("after disabling, the kept session reads %+v", rec)
		}
		if err := s.SetDisabled(ctx, alice.ID, false, at(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if u, _ := s.GetUser(ctx, alice.ID); u.Disabled {
			t.Errorf("after enabling, the user reads disabled")
		}

		// A user the project wrote without a credential is disabled all
		// the same, and has no password.
		var bareID string
		insert := `INSERT INTO "user" ("email", "display_name") VALUES (?, ?) RETURNING "id"`
		if db.dialect == identity.Postgres {
			insert = `INSERT INTO "user" ("email", "display_name") VALUES ($1, $2) RETURNING "id"`
		}
		if err := db.db.QueryRow(insert, "bare@example.com", "Bare").Scan(&bareID); err != nil {
			t.Fatal(err)
		}
		bare, err := s.FindLogin(ctx, "bare@example.com")
		if err != nil || bare.PasswordHash != "" || bare.User.Disabled {
			t.Fatalf("a user without a credential reads %+v, %v", bare, err)
		}
		if err := s.SetDisabled(ctx, bare.User.ID, true, at(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if bare, _ = s.FindLogin(ctx, "bare@example.com"); !bare.User.Disabled || bare.PasswordHash != "" {
			t.Errorf("a disabled user without a credential reads %+v", bare)
		}

		missing := "00000000-0000-4000-8000-000000000000"
		for name, err := range map[string]error{
			"SetPassword": s.SetPassword(ctx, missing, "h", at(0), ""),
			"SetDisabled": s.SetDisabled(ctx, missing, true, at(0)),
		} {
			if !errors.Is(err, identity.ErrNotFound) {
				t.Errorf("%s of a missing user: %v, want ErrNotFound", name, err)
			}
		}
	})
}

// TestStoreRoles: roles keep their permissions in order, list by name and
// refuse a taken name; grants are idempotent, a user's roles list by name,
// and deleting a role deletes its grants.
func TestStoreRoles(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db testDB, s *identity.SQLStore) {
		ctx := context.Background()
		if !s.HasRoles() {
			t.Fatal("the fixture has a UserRole table")
		}
		alice := mustCreateUser(t, s, "alice@example.com", "Alice")
		bob := mustCreateUser(t, s, "bob@example.com", "Bob")

		writer, err := s.CreateRole(ctx, "writer", []string{"orders.write", "orders.read"})
		if err != nil {
			t.Fatal(err)
		}
		admin, err := s.CreateRole(ctx, "admin", []string{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateRole(ctx, "writer", nil); !errors.Is(err, identity.ErrRoleNameTaken) {
			t.Errorf("a taken role name: %v, want ErrRoleNameTaken", err)
		}
		got, err := s.GetRole(ctx, writer.ID)
		if err != nil || got.Name != "writer" || strings.Join(got.Permissions, ",") != "orders.write,orders.read" {
			t.Fatalf("GetRole = %+v, %v", got, err)
		}
		roles, err := s.ListRoles(ctx)
		if err != nil || len(roles) != 2 || roles[0].Name != "admin" || roles[1].Name != "writer" || len(roles[0].Permissions) != 0 {
			t.Fatalf("ListRoles = %+v, %v; want admin then writer", roles, err)
		}

		if _, err := s.UpdateRole(ctx, admin.ID, "writer", nil); !errors.Is(err, identity.ErrRoleNameTaken) {
			t.Errorf("renaming to a taken name: %v, want ErrRoleNameTaken", err)
		}
		updated, err := s.UpdateRole(ctx, admin.ID, "administrator", []string{"identity", "orders"})
		if err != nil || updated.Name != "administrator" || strings.Join(updated.Permissions, ",") != "identity,orders" {
			t.Fatalf("UpdateRole = %+v, %v", updated, err)
		}
		if _, err := s.UpdateRole(ctx, writer.ID, "writer", []string{"orders.write"}); err != nil {
			t.Errorf("an update keeping the role's own name: %v", err)
		}

		for _, grant := range [][2]string{{alice.ID, writer.ID}, {alice.ID, admin.ID}, {alice.ID, admin.ID}, {bob.ID, writer.ID}} {
			if err := s.GrantRole(ctx, grant[0], grant[1], at(0)); err != nil {
				t.Fatalf("grant: %v", err)
			}
		}
		held, err := s.UserRoles(ctx, alice.ID)
		if err != nil || len(held) != 2 || held[0].Name != "administrator" || held[1].Name != "writer" {
			t.Fatalf("UserRoles = %+v, %v; want administrator then writer", held, err)
		}
		if u, _ := s.GetUser(ctx, alice.ID); len(u.Roles) != 2 {
			t.Errorf("GetUser's roles = %+v", u.Roles)
		}
		users, err := s.ListUsers(ctx)
		if err != nil || len(users[0].Roles) != 2 || len(users[1].Roles) != 1 || users[1].Roles[0].Name != "writer" {
			t.Fatalf("ListUsers' roles = %+v, %v", users, err)
		}

		if err := s.RevokeRole(ctx, alice.ID, writer.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeRole(ctx, alice.ID, writer.ID); err != nil {
			t.Errorf("revoking a role not held: %v", err)
		}
		if held, _ := s.UserRoles(ctx, alice.ID); len(held) != 1 || held[0].Name != "administrator" {
			t.Errorf("after the revoke alice holds %+v", held)
		}

		if err := s.DeleteRole(ctx, writer.ID); err != nil {
			t.Fatal(err)
		}
		if held, _ := s.UserRoles(ctx, bob.ID); len(held) != 0 {
			t.Errorf("a deleted role is still held: %+v", held)
		}
		if err := s.DeleteRole(ctx, writer.ID); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("deleting a deleted role: %v, want ErrNotFound", err)
		}

		missing := "00000000-0000-4000-8000-000000000000"
		for name, err := range map[string]error{
			"GetRole":                  func() error { _, err := s.GetRole(ctx, missing); return err }(),
			"UpdateRole":               func() error { _, err := s.UpdateRole(ctx, missing, "x", nil); return err }(),
			"GrantRole a missing role": s.GrantRole(ctx, alice.ID, missing, at(0)),
			"GrantRole a missing user": s.GrantRole(ctx, missing, admin.ID, at(0)),
			"RevokeRole":               s.RevokeRole(ctx, missing, admin.ID),
			"GrantRole a malformed id": s.GrantRole(ctx, alice.ID, "not a key", at(0)),
		} {
			if !errors.Is(err, identity.ErrNotFound) {
				t.Errorf("%s: %v, want ErrNotFound", name, err)
			}
		}
	})
}

// TestStoreWithoutRoles: a descriptor without a role table gives a store
// whose users hold no roles and whose role methods say there are none.
func TestStoreWithoutRoles(t *testing.T) {
	db := openSQLite(t)
	s, err := identity.NewSQLStore(db, identity.SQLite, descriptorJSON(t, func(d map[string]any) {
		delete(d, "role")
		delete(d, "roleGrant")
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	alice := mustCreateUser(t, s, "alice@example.com", "Alice")
	if s.HasRoles() {
		t.Error("HasRoles without a role table")
	}
	if roles, err := s.UserRoles(ctx, alice.ID); err != nil || len(roles) != 0 {
		t.Errorf("UserRoles = %v, %v", roles, err)
	}
	if u, err := s.GetUser(ctx, alice.ID); err != nil || u.Roles == nil || len(u.Roles) != 0 {
		t.Errorf("GetUser = %+v, %v", u, err)
	}
	if _, err := s.ListRoles(ctx); !errors.Is(err, identity.ErrNoRoles) {
		t.Errorf("ListRoles: %v, want ErrNoRoles", err)
	}
	if err := s.GrantRole(ctx, alice.ID, alice.ID, at(0)); !errors.Is(err, identity.ErrNoRoles) {
		t.Errorf("GrantRole: %v, want ErrNoRoles", err)
	}
	if _, _, err := s.Bootstrap(ctx, newBootstrap("admin", "bob@example.com")); !errors.Is(err, identity.ErrNoRoles) {
		t.Errorf("Bootstrap: %v, want ErrNoRoles", err)
	}
}

// newBootstrap is a first administrator named login who holds role, with
// the identity permissions.
func newBootstrap(role, login string) identity.NewBootstrap {
	return identity.NewBootstrap{
		Role:        role,
		Permissions: []string{"identity", "orders.read"},
		User:        identity.NewUser{Login: login, Name: "Admin", PasswordHash: "hash-of-" + login, At: at(0)},
	}
}

// TestStoreBootstrap: the first administrator's role, user and grant are
// written together, once. A taken role name, a taken login or a login the
// scalar refuses writes none of them, and once a grant exists a second
// bootstrap writes nothing.
func TestStoreBootstrap(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db testDB, s *identity.SQLStore) {
		ctx := context.Background()
		if _, err := s.CreateRole(ctx, "taken", nil); err != nil {
			t.Fatal(err)
		}
		bob := mustCreateUser(t, s, "bob@example.com", "Bob")

		if _, _, err := s.Bootstrap(ctx, newBootstrap("taken", "alice@example.com")); !errors.Is(err, identity.ErrRoleNameTaken) {
			t.Errorf("a taken role name: %v, want ErrRoleNameTaken", err)
		}
		if _, err := s.FindLogin(ctx, "alice@example.com"); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("a refused bootstrap left its user behind: %v", err)
		}
		if _, _, err := s.Bootstrap(ctx, newBootstrap("admin", "BOB@example.com")); !errors.Is(err, identity.ErrLoginTaken) {
			t.Errorf("a taken login: %v, want ErrLoginTaken", err)
		}
		var invalid *identity.InvalidLoginError
		if _, _, err := s.Bootstrap(ctx, newBootstrap("admin", "not an email")); !errors.As(err, &invalid) {
			t.Errorf("a login the scalar refuses: %v, want an InvalidLoginError", err)
		}
		if roles, err := s.ListRoles(ctx); err != nil || len(roles) != 1 || roles[0].Name != "taken" {
			t.Fatalf("refused bootstraps left the roles %+v, %v; want taken alone", roles, err)
		}

		role, user, err := s.Bootstrap(ctx, newBootstrap("admin", " Alice@Example.com "))
		if err != nil {
			t.Fatal(err)
		}
		if role.ID == "" || role.Name != "admin" || strings.Join(role.Permissions, ",") != "identity,orders.read" {
			t.Errorf("the bootstrap's role is %+v", role)
		}
		if user.ID == "" || user.Login != "alice@example.com" || user.Name != "Admin" || len(user.Roles) != 1 || user.Roles[0].ID != role.ID {
			t.Errorf("the bootstrap's user is %+v", user)
		}
		rec, err := s.FindLogin(ctx, "alice@example.com")
		if err != nil || rec.User.ID != user.ID || rec.PasswordHash != "hash-of- Alice@Example.com " || rec.User.Disabled {
			t.Fatalf("the bootstrap's user reads %+v, %v", rec, err)
		}
		held, err := s.UserRoles(ctx, user.ID)
		if err != nil || len(held) != 1 || held[0].ID != role.ID || strings.Join(held[0].Permissions, ",") != "identity,orders.read" {
			t.Errorf("the bootstrap's user holds %+v, %v", held, err)
		}

		if _, _, err := s.Bootstrap(ctx, newBootstrap("second", "carol@example.com")); !errors.Is(err, identity.ErrGrantExists) {
			t.Errorf("a second bootstrap: %v, want ErrGrantExists", err)
		}
		if _, err := s.FindLogin(ctx, "carol@example.com"); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("a second bootstrap created its user: %v", err)
		}
		if roles, _ := s.ListRoles(ctx); len(roles) != 2 {
			t.Errorf("after a second bootstrap the roles are %+v; want admin and taken", roles)
		}
		if held, _ := s.UserRoles(ctx, bob.ID); len(held) != 0 {
			t.Errorf("bob holds %+v", held)
		}
	})
}

// TestStoreBootstrapOnce: of bootstraps run at once, each with its own
// role and login, one creates its administrator and the others none.
func TestStoreBootstrapOnce(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db testDB, s *identity.SQLStore) {
		const runs = 4
		errs := make(chan error, runs)
		for i := range runs {
			go func() {
				_, _, err := s.Bootstrap(context.Background(), newBootstrap(fmt.Sprintf("admin-%d", i), fmt.Sprintf("admin-%d@example.com", i)))
				errs <- err
			}()
		}
		created := 0
		for range runs {
			if err := <-errs; err == nil {
				created++
			}
		}
		roles, err := s.ListRoles(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		users, err := s.ListUsers(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if created != 1 || len(roles) != 1 || len(users) != 1 {
			t.Errorf("%d bootstraps at once created %d administrators, %d roles and %d users; want one of each", runs, created, len(roles), len(users))
		}
	})
}

// TestNewSQLStoreRefuses: a store is not built over a descriptor it cannot
// read or a dialect it does not speak.
func TestNewSQLStoreRefuses(t *testing.T) {
	db := openSQLite(t)
	cases := map[string]struct {
		dialect    identity.Dialect
		descriptor []byte
	}{
		"an unknown dialect":       {"mysql", descriptorJSON(t, nil)},
		"another version":          {identity.SQLite, descriptorJSON(t, func(d map[string]any) { d["version"] = 2 })},
		"an empty name":            {identity.SQLite, descriptorJSON(t, func(d map[string]any) { d["session"].(map[string]any)["table"] = "" })},
		"an unknown member":        {identity.SQLite, descriptorJSON(t, func(d map[string]any) { d["extra"] = map[string]any{} })},
		"a role without its grant": {identity.SQLite, descriptorJSON(t, func(d map[string]any) { delete(d, "roleGrant") })},
		"an unknown login scalar":  {identity.SQLite, descriptorJSON(t, func(d map[string]any) { d["user"].(map[string]any)["loginScalar"] = "Contact.Pager" })},
		"not JSON":                 {identity.SQLite, []byte("{")},
	}
	for name, c := range cases {
		if _, err := identity.NewSQLStore(db, c.dialect, c.descriptor); err == nil {
			t.Errorf("%s: NewSQLStore accepted it", name)
		}
	}
	if _, err := identity.NewSQLStore(nil, identity.SQLite, descriptorJSON(t, nil)); err == nil {
		t.Error("NewSQLStore accepted no database")
	}
}

// TestStoreNameIsLogin: when the User trait names no name, the login is
// the name, and a user is created with the login column alone.
func TestStoreNameIsLogin(t *testing.T) {
	ddl := strings.Replace(string(readFixture(t, "sqlite/create.sql")), `"display_name" TEXT NOT NULL`, `"display_name" TEXT`, 1)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "login-name.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	s, err := identity.NewSQLStore(db, identity.SQLite, descriptorJSON(t, func(d map[string]any) {
		d["user"].(map[string]any)["columns"].(map[string]any)["name"] = "email"
	}))
	if err != nil {
		t.Fatal(err)
	}
	u := mustCreateUser(t, s, "Alice@Example.com", "Ignored")
	if u.Name != "alice@example.com" {
		t.Errorf("the user's name is %q, want the login", u.Name)
	}
	var displayName sql.NullString
	if err := db.QueryRow(`SELECT "display_name" FROM "user"`).Scan(&displayName); err != nil || displayName.Valid {
		t.Errorf("the store wrote a column the descriptor does not name: %v, %v", displayName, err)
	}
	mustSession(t, s, u, hashOf(1))
	if rec := findSession(t, s, hashOf(1)); rec.User.Name != "alice@example.com" {
		t.Errorf("a session's user is named %q", rec.User.Name)
	}
}
