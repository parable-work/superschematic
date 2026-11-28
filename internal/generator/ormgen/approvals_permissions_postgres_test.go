package ormgen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// approvalsPermissionsFixture is the DB service whose Change's gate reads
// each signer's permission again at each guard (recheckPermission, D200),
// beside the identity runtime's user and role tables.
const approvalsPermissionsFixture = "fixture-approvals-identity-db"

// TestApprovalsRecheckASignersPermissionOnPostgres runs an Approvals gate
// with recheckPermission through the generated Go ORM and the behaviors'
// core (D200): the core asks the adapter for the permissions each signer
// holds now, which it reads from the layout's identity tables, so a
// reviewer whose role grant is revoked after signing stops counting and
// the transition is vetoed approvals_missing, and counts again once the
// role is granted back. Before the adapter answered the read, every guard
// of such a gate failed the command as internal. It loads
// fixture-approvals-identity-db with the core's registry, generates its Go
// types, ORM and DDL, and runs the module's test on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL. The module links the core's
// static archive (scripts/rules-archive.sh); it skips without it, without
// the database and in -short mode.
func TestApprovalsRecheckASignersPermissionOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated ORM's Approvals recheck in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM's Approvals recheck against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}
	ormDir := generateBehaviorORMModule(t, paths, approvalsPermissionsFixture)
	if err := os.WriteFile(filepath.Join(ormDir, "approvals_recheck_test.go"), []byte(approvalsRecheckORMTest), 0o644); err != nil {
		t.Fatalf("write the module's test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if !strings.Contains(out, "--- PASS: TestApprovalsRecheckASignersPermission") {
		t.Fatalf("the generated module's test did not pass:\n%s", out)
	}
}

const approvalsRecheckORMTest = `package orm

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-approvals-identity-db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
)

func TestApprovalsRecheckASignersPermission(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("ormgen_approvals_recheck_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	defer db.Close()

	// The identity runtime's rows, written past the repositories as its
	// store writes them: a reviewer, and the role that lets them approve.
	reviewer, role := uuid.New(), uuid.New()
	exec := func(text string, values ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, text, values...); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
	}
	exec("INSERT INTO account (id, email) VALUES ($1, 'reviewer@example.com')", reviewer)
	exec("INSERT INTO role (id, name, permissions) VALUES ($1, 'reviewer', ARRAY['changes.approve'])", role)
	grant := func() {
		exec("INSERT INTO user_role_grant (user_id, role_id, granted_at) VALUES ($1, $2, now())", reviewer, role)
	}
	grant()

	id, err := types.ParseIdentityUUID(reviewer.String())
	if err != nil {
		t.Fatal(err)
	}
	// The reviewer as the route's caller hands them to the core: their
	// subject and the permissions their roles held when they signed.
	asReviewer := WithBehaviorCaller(WithUserID(ctx, id), rules.Caller{Subject: id.String(), Permissions: []string{"changes.approve"}})
	change, err := db.Change.CreateOne(asReviewer, &ChangeCreate{Title: "Bump the timeout"})
	if err != nil {
		t.Fatalf("create the change: %v", err)
	}
	signOff := func() {
		t.Helper()
		if _, err := db.Change.Approvals().SignOff(asReviewer, *change.Id, ChangeApprovalsSignOffParams{State: ChangeApprovalsGateReady}); err != nil {
			t.Fatalf("sign off: %v", err)
		}
	}
	ready := func() error {
		_, err := db.Change.Status().Transition(asReviewer, *change.Id, ChangeStatusTransitionParams{To: ChangeStatusStateReady})
		return err
	}
	back := func() {
		t.Helper()
		if _, err := db.Change.Status().Transition(asReviewer, *change.Id, ChangeStatusTransitionParams{To: ChangeStatusStateOpen}); err != nil {
			t.Fatalf("move back to open: %v", err)
		}
	}
	vetoed := func(what string) {
		t.Helper()
		err := ready()
		refusal, ok := rules.AsRefusal(err)
		if !ok || refusal.Code != rules.CodeVetoed || !strings.Contains(string(refusal.Details), "approvals_missing") {
			t.Fatalf("%s: %v, want the gate's approvals_missing veto", what, err)
		}
	}

	// The reviewer signs while they hold the permission, and the role's
	// grant is revoked after: the core reads the reviewer's permissions
	// again, and the sign-off counts for nothing.
	signOff()
	exec("DELETE FROM user_role_grant WHERE user_id = $1", reviewer)
	vetoed("ready with the reviewer's role revoked")

	// Granted back, the same sign-off counts again.
	grant()
	if err := ready(); err != nil {
		t.Fatalf("ready with the reviewer's role granted back: %v", err)
	}
	back()

	// A disabled reviewer holds no permission, and one enabled again does.
	signOff()
	exec("INSERT INTO user_credential (user_id, password_hash, password_changed_at, disabled_at) VALUES ($1, 'x', now(), now())", reviewer)
	vetoed("ready with the reviewer disabled")
	exec("UPDATE user_credential SET disabled_at = NULL WHERE user_id = $1", reviewer)
	if err := ready(); err != nil {
		t.Fatalf("ready with the reviewer enabled again: %v", err)
	}
}
`
