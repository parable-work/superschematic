package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const userModelFixture = "fixture-user-model-db"

// TestGeneratedUserModelORM generates the DDL, the Go types module and the
// ORM module for fixture-user-model-db, whose User and UserRole tables the
// loader adds Session, UserCredential and UserRoleGrant beside (D50), then
// builds, vets and tests the ORM. Its generated test applies the DDL to the
// Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (and skips without
// it) and shows the tables hold: a login that differs from another only in
// case is the same login and finds the same user, a session's token hash, a
// user's credential and a user's grant of a role are each unique, and
// deleting a role deletes its grants and deleting a user its sessions,
// credential and grants.
func TestGeneratedUserModelORM(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, userModelFixture)
	if err := os.WriteFile(filepath.Join(ormDir, "user_model_test.go"), []byte(userModelORMTest), 0o644); err != nil {
		t.Fatalf("write user model test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestUserModelTablesOnPostgres") {
		t.Fatalf("the generated ORM did not run TestUserModelTablesOnPostgres:\n%s", out)
	}
}

const userModelORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-user-model-db"
)

func TestUserModelTablesOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the user model tables against Postgres")
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	ctx := context.Background()

	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer base.Close()
	schema := fmt.Sprintf("user_model_orm_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("connect generated orm: %v", err)
	}
	defer db.Close()

	unique := func(what, constraint string, err error) {
		t.Helper()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != constraint {
			t.Fatalf("%s = %v, want a unique violation of %s", what, err, constraint)
		}
	}
	count := func(table string, user *types.User) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM \""+table+"\" WHERE user_id = $1", user.Id.ToUUID()).Scan(&n); err != nil {
			t.Fatalf("count %s rows: %v", table, err)
		}
		return n
	}
	now := types.TemporalDateTime(time.Now().UTC().Truncate(time.Microsecond))
	later := types.TemporalDateTime(time.Now().UTC().Add(14 * 24 * time.Hour).Truncate(time.Microsecond))

	// The login column ignores case: a second account for the same address
	// is refused, and a lookup in another case finds the user.
	alice, err := db.User.CreateOne(ctx, &types.User{Email: "alice@example.com", DisplayName: "Alice"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, err = db.User.CreateOne(ctx, &types.User{Email: "Alice@Example.COM", DisplayName: "Alice again"})
	unique("a second user whose login differs only in case", "user_email_key", err)
	upper := "ALICE@EXAMPLE.COM"
	found, err := db.User.FindOne(ctx, &UserFilter{Email: &StringFilter{Eq: &upper}}, nil)
	if err != nil || found == nil || *found.Id != *alice.Id {
		t.Fatalf("find the user by %s = %+v, %v; want %+v", upper, found, err, alice)
	}
	bob, err := db.User.CreateOne(ctx, &types.User{Email: "bob@example.com", DisplayName: "Bob"})
	if err != nil {
		t.Fatalf("create second user: %v", err)
	}

	// A session's token hash is unique, and so is a user's credential.
	hash := types.CryptoSHA256(strings.Repeat("ab", 32))
	session, err := db.Session.CreateOne(ctx, &types.Session{User: *alice, TokenHash: hash, ExpiresAt: later})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if session.RevokedAt != nil || session.LastSeenAt != nil || time.Time(session.CreatedAt).IsZero() {
		t.Fatalf("a new session = %+v, want createdAt set and lastSeenAt and revokedAt null", session)
	}
	_, err = db.Session.CreateOne(ctx, &types.Session{User: *bob, TokenHash: hash, ExpiresAt: later})
	unique("a second session with the same token hash", "session_token_hash_key", err)
	if _, err := db.Session.CreateOne(ctx, &types.Session{User: *bob, TokenHash: types.CryptoSHA256(strings.Repeat("cd", 32)), ExpiresAt: later}); err != nil {
		t.Fatalf("create bob's session: %v", err)
	}
	phc := "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"
	credential, err := db.UserCredential.CreateOne(ctx, &types.UserCredential{User: *alice, PasswordHash: phc, PasswordChangedAt: now})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	if credential.PasswordHash != phc || credential.DisabledAt != nil {
		t.Fatalf("credential = %+v, want the PHC string and disabledAt null", credential)
	}
	_, err = db.UserCredential.CreateOne(ctx, &types.UserCredential{User: *alice, PasswordHash: phc, PasswordChangedAt: now})
	unique("a second credential for one user", "user_credential_user_id_key", err)

	// A user holds a role once.
	admin, err := db.Role.CreateOne(ctx, &types.Role{Name: "admin", Permissions: []string{"identity.users.read", "identity.users.write"}})
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	editor, err := db.Role.CreateOne(ctx, &types.Role{Name: "editor", Permissions: []string{"posts.write"}})
	if err != nil {
		t.Fatalf("create second role: %v", err)
	}
	_, err = db.Role.CreateOne(ctx, &types.Role{Name: "admin", Permissions: []string{}})
	unique("a second role of the same name", "role_name_key", err)
	for _, grant := range []*types.UserRoleGrant{
		{User: *alice, Role: *admin, GrantedAt: now},
		{User: *alice, Role: *editor, GrantedAt: now},
		{User: *bob, Role: *editor, GrantedAt: now},
	} {
		if _, err := db.UserRoleGrant.CreateOne(ctx, grant); err != nil {
			t.Fatalf("grant a role: %v", err)
		}
	}
	_, err = db.UserRoleGrant.CreateOne(ctx, &types.UserRoleGrant{User: *alice, Role: *admin, GrantedAt: now})
	unique("a second grant of one role to one user", "uq_user_role_grant_user_role", err)

	// Deleting a role deletes its grants and no others.
	if err := db.Role.DeleteOne(ctx, *editor.Id); err != nil {
		t.Fatalf("delete role: %v", err)
	}
	if got := count("user_role_grant", alice); got != 1 {
		t.Fatalf("alice holds %d grants after the editor role went, want 1", got)
	}
	if got := count("user_role_grant", bob); got != 0 {
		t.Fatalf("bob holds %d grants after the editor role went, want 0", got)
	}

	// Deleting a user deletes their sessions, credential and grants, and
	// leaves another user's.
	if err := db.User.DeleteOne(ctx, *alice.Id); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	for _, table := range []string{"session", "user_credential", "user_role_grant"} {
		if got := count(table, alice); got != 0 {
			t.Fatalf("%d %s rows outlived their user", got, table)
		}
	}
	if got := count("session", bob); got != 1 {
		t.Fatalf("bob has %d sessions after alice went, want 1", got)
	}
}
`
