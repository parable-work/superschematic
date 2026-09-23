package ormgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGeneratedORMCompiles generates the types module and the ORM module for
// fixture-db into a temp tree mirroring the dist layout (orm/<name> resolves
// types via ../../types/go/<name>), wires the real superscalar module, and
// runs `go build`. This is the end-to-end compile check for the ormgen port.
func TestGeneratedORMCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}

	paths := testpaths.Local(t)

	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	extendFixtureForCompileCoverage(schema)

	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	typesModule := "example.com/schemas/types/go/fixture-db"

	tempRoot := t.TempDir()
	typesDir := filepath.Join(tempRoot, "types", "go", "fixture-db")
	ormDir := filepath.Join(tempRoot, "orm", "fixture-db")

	typesOutput, err := typegen.Generate(schema, typegen.Options{
		SchemaName: "fixture-db",
		ModulePath: typesModule,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("generate types: %v", err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
		t.Fatalf("set types replace paths: %v", err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("write types: %v", err)
	}

	ormOutput, err := Generate(schema, Options{
		SchemaName:  "fixture-db",
		ModulePath:  "example.com/schemas/orm/fixture-db",
		TypesModule: typesModule,
		Clock:       fixedClock,
	})
	if err != nil {
		t.Fatalf("generate orm: %v", err)
	}
	if ormOutput == nil {
		t.Fatal("expected ORM output for fixture-db")
	}
	if err := SetReplacePaths(ormOutput, paths, ormDir); err != nil {
		t.Fatalf("set orm replace paths: %v", err)
	}
	if err := WriteORM(ormOutput, ormDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	ddlOutput, err := sqlgen.Generate(schema, sqlgen.Options{
		SchemaName: "fixture-db",
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("generate ddl: %v", err)
	}
	ddlDir := filepath.Join(tempRoot, "ddl", "fixture-db")
	if err := sqlgen.WriteDDL(ddlOutput, ddlDir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}
	createSQL, err := os.ReadFile(filepath.Join(ddlDir, "create.sql"))
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}

	historyDecoderTest := `package orm

import (
	"testing"

	types "example.com/schemas/types/go/fixture-db"
)

func TestDecodeTenantHistoryDataSnakeCaseRoundTrip(t *testing.T) {
	raw := []byte(` + "`" + `{
		"created_at": "2026-01-02T03:04:05Z",
		"updated_at": "2026-01-02T04:05:06Z",
		"id": "00000000-0000-0000-0000-000000000001",
		"name": "Acme Corp",
		"slug": "acme",
		"email": "admin@example.com",
		"status": "active",
		"is_active": true,
		"seat_count": 12,
		"metadata": {"tier": "pro"},
		"_version": 2
	}` + "`" + `)

	got, err := decodeTenantHistoryData(raw)
	if err != nil {
		t.Fatalf("decodeTenantHistoryData returned error: %v", err)
	}
	if got.Id.ToUUID().String() != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("Id = %s", got.Id.ToUUID())
	}
	if got.Status != types.TenantStatus_Active {
		t.Fatalf("Status = %q", got.Status)
	}
	if got.Version != 2 {
		t.Fatalf("Version = %d, want 2", got.Version)
	}
	if got.UpdatedAt == nil {
		t.Fatal("UpdatedAt was not decoded")
	}
	if len(got.Metadata) == 0 {
		t.Fatal("Metadata was not decoded")
	}
}
`
	if err := os.WriteFile(filepath.Join(ormDir, "history_decoder_test.go"), []byte(historyDecoderTest), 0o644); err != nil {
		t.Fatalf("write history decoder test: %v", err)
	}
	// ApplyTo covers the field shapes extendFixtureForCompileCoverage adds:
	// an optional array, a nullable enum, a required relation and updatedBy.
	applyToTest := `package orm

import (
	"testing"

	types "example.com/schemas/types/go/fixture-db"
)

func applyToUUID(t *testing.T, raw string) types.IdentityUUID {
	t.Helper()
	id, err := types.ParseIdentityUUID(raw)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", raw, err)
	}
	return id
}

func TestTenantUserUpdateApplyTo(t *testing.T) {
	var nilUpdate *TenantUserUpdate
	nilUpdate.ApplyTo(&types.TenantUser{})
	(&TenantUserUpdate{}).ApplyTo(nil)

	status := types.TenantStatus_Active
	invitedBy := applyToUUID(t, "00000000-0000-0000-0000-000000000003")
	row := types.TenantUser{
		DisplayName: "Alice",
		Roles:       []string{"admin"},
		LastStatus:  &status,
		InvitedBy:   &invitedBy,
	}
	name := "Alice Cooper"
	tenantID := applyToUUID(t, "00000000-0000-0000-0000-000000000001")
	actor := applyToUUID(t, "00000000-0000-0000-0000-000000000002")
	ignored := []string{"ignored"}
	update := &TenantUserUpdate{
		DisplayName:       &name,
		Roles:             &ignored,
		RolesSetNull:      true,
		LastStatusSetNull: true,
		TenantID:          &tenantID,
		UpdatedBy:         &actor,
	}
	update.ApplyTo(&row)

	if row.DisplayName != name {
		t.Fatalf("DisplayName = %q, want %q", row.DisplayName, name)
	}
	if row.Roles != nil {
		t.Fatalf("Roles = %v, want nil: SetNull wins over a value", row.Roles)
	}
	if row.LastStatus != nil {
		t.Fatalf("LastStatus = %v, want nil", *row.LastStatus)
	}
	if row.InvitedBy == nil || row.InvitedBy.ToUUID() != invitedBy.ToUUID() {
		t.Fatalf("InvitedBy changed without being set: %v", row.InvitedBy)
	}
	if row.Tenant.Id == nil || row.Tenant.Id.ToUUID() != tenantID.ToUUID() {
		t.Fatalf("Tenant.Id = %v, want %s", row.Tenant.Id, tenantID.ToUUID())
	}
	if row.UpdatedBy.ToUUID() != actor.ToUUID() {
		t.Fatalf("UpdatedBy = %s, want %s", row.UpdatedBy.ToUUID(), actor.ToUUID())
	}

	(&TenantUserUpdate{DisplayNameSetNull: true}).ApplyTo(&row)
	if row.DisplayName != "" {
		t.Fatalf("DisplayName = %q after SetNull, want empty", row.DisplayName)
	}
}
`
	if err := os.WriteFile(filepath.Join(ormDir, "apply_to_test.go"), []byte(applyToTest), 0o644); err != nil {
		t.Fatalf("write apply to test: %v", err)
	}
	strategyATest := `package orm

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-db"
)

const strategyADDLSQL = ` + "`" + string(createSQL) + "`" + `

func TestStrategyACompositeHistoryAsOf(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM Strategy A integration test")
	}

	ctx := context.Background()
	basePool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect base pool: %v", err)
	}
	defer basePool.Close()

	schemaName := fmt.Sprintf("superschematic_strategy_a_%d", time.Now().UnixNano())
	if _, err := basePool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", schemaName)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() {
		_, _ = basePool.Exec(context.Background(), fmt.Sprintf("DROP SCHEMA %s CASCADE", schemaName))
	}()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		t.Fatalf("acquire ddl connection: %v", err)
	}
	if _, err := conn.Conn().PgConn().Exec(ctx, strategyADDLSQL).ReadAll(); err != nil {
		conn.Release()
		pool.Close()
		t.Fatalf("apply generated ddl: %v", err)
	}
	conn.Release()

	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("connect generated orm: %v", err)
	}
	defer db.Close()

	tenantID := mustUUID(t, "00000000-0000-0000-0000-000000000001")
	tenantTwoID := mustUUID(t, "00000000-0000-0000-0000-000000000002")
	userOneID := mustUUID(t, "00000000-0000-0000-0000-000000000101")
	userTwoID := mustUUID(t, "00000000-0000-0000-0000-000000000102")
	userThreeID := mustUUID(t, "00000000-0000-0000-0000-000000000103")
	// created_by / updated_by are NOT NULL on tenant_user (added by
	// extendFixtureForCompileCoverage); raw inserts must supply an actor.
	actorID := mustUUID(t, "00000000-0000-0000-0000-0000000000ff")

	execStrategyASQL(t, pool, ` + "`" + `INSERT INTO tenant (id, name, slug, email, status, is_active, seat_count, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)` + "`" + `, tenantID.ToUUID(), "Acme", "acme", "admin@example.com", "active", true, 5, ` + "`" + `{"tier":"basic"}` + "`" + `)
	execStrategyASQL(t, pool, ` + "`" + `INSERT INTO tenant_user (id, tenant_id, display_name, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5)` + "`" + `, userOneID.ToUUID(), tenantID.ToUUID(), "Alice", actorID.ToUUID(), actorID.ToUUID())
	t0 := strategyADBTime(t, pool)

	execStrategyASQL(t, pool, ` + "`" + `UPDATE tenant_user SET display_name = $1 WHERE id = $2` + "`" + `, "Alice Cooper", userOneID.ToUUID())
	t1 := strategyADBTime(t, pool)

	execStrategyASQL(t, pool, ` + "`" + `UPDATE tenant SET name = $1 WHERE id = $2` + "`" + `, "Acme Labs", tenantID.ToUUID())
	execStrategyASQL(t, pool, ` + "`" + `INSERT INTO tenant_user (id, tenant_id, display_name, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5)` + "`" + `, userTwoID.ToUUID(), tenantID.ToUUID(), "Bob", actorID.ToUUID(), actorID.ToUUID())
	t2 := strategyADBTime(t, pool)

	execStrategyASQL(t, pool, ` + "`" + `DELETE FROM tenant_user WHERE id = $1` + "`" + `, userOneID.ToUUID())
	t3 := strategyADBTime(t, pool)

	execStrategyASQL(t, pool, ` + "`" + `INSERT INTO tenant (id, name, slug, email, status, is_active, seat_count, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)` + "`" + `, tenantTwoID.ToUUID(), "Other Corp", "other", "other@example.com", "active", true, 5, ` + "`" + `{"tier":"basic"}` + "`" + `)
	execStrategyASQL(t, pool, ` + "`" + `UPDATE tenant_user SET tenant_id = $1 WHERE id = $2` + "`" + `, tenantTwoID.ToUUID(), userTwoID.ToUUID())
	t4 := strategyADBTime(t, pool)

	snapshot0 := reconstructTenantSnapshotAt(t, db, tenantID, t0)
	if snapshot0.Name != "Acme" || snapshot0.Version != 1 {
		t.Fatalf("t0 parent = (%q, version %d), want Acme version 1", snapshot0.Name, snapshot0.Version)
	}
	if len(snapshot0.Users) != 1 {
		t.Fatalf("t0 users len = %d, want 1", len(snapshot0.Users))
	}
	userOneAtT0 := findSnapshotUser(t, snapshot0.Users, userOneID)
	if userOneAtT0.DisplayName != "Alice" || userOneAtT0.Version != 1 {
		t.Fatalf("t0 user one = (%q, version %d), want Alice version 1", userOneAtT0.DisplayName, userOneAtT0.Version)
	}

	snapshot1 := reconstructTenantSnapshotAt(t, db, tenantID, t1)
	userOneAtT1 := findSnapshotUser(t, snapshot1.Users, userOneID)
	if snapshot1.Version != 1 || userOneAtT1.Version != 2 {
		t.Fatalf("t1 versions = parent %d child %d, want independent versions 1 and 2", snapshot1.Version, userOneAtT1.Version)
	}
	if userOneAtT1.DisplayName != "Alice Cooper" {
		t.Fatalf("t1 user one display name = %q, want Alice Cooper", userOneAtT1.DisplayName)
	}

	snapshot2 := reconstructTenantSnapshotAt(t, db, tenantID, t2)
	if snapshot2.Name != "Acme Labs" || snapshot2.Version != 2 {
		t.Fatalf("t2 parent = (%q, version %d), want Acme Labs version 2", snapshot2.Name, snapshot2.Version)
	}
	if len(snapshot2.Users) != 2 {
		t.Fatalf("t2 users len = %d, want 2", len(snapshot2.Users))
	}
	userTwoAtT2 := findSnapshotUser(t, snapshot2.Users, userTwoID)
	if userTwoAtT2.DisplayName != "Bob" || userTwoAtT2.Version != 1 {
		t.Fatalf("t2 user two = (%q, version %d), want Bob version 1", userTwoAtT2.DisplayName, userTwoAtT2.Version)
	}

	snapshot3 := reconstructTenantSnapshotAt(t, db, tenantID, t3)
	if len(snapshot3.Users) != 1 {
		t.Fatalf("t3 users len = %d, want deleted child excluded", len(snapshot3.Users))
	}
	remainingUser := findSnapshotUser(t, snapshot3.Users, userTwoID)
	if remainingUser.DisplayName != "Bob" {
		t.Fatalf("t3 remaining user = %q, want Bob", remainingUser.DisplayName)
	}

	snapshot4 := reconstructTenantSnapshotAt(t, db, tenantID, t4)
	if len(snapshot4.Users) != 0 {
		t.Fatalf("t4 users len = %d, want child moved to another tenant excluded", len(snapshot4.Users))
	}

	// Regression: hard delete of a versioned row must succeed and
	// record a tombstone at OLD._version + 1. The buggy trigger wrote the
	// tombstone at OLD._version, colliding on the unique (id, _version) history
	// index and aborting every hard delete with 23505 -- so the DELETE at t3
	// above already fails on the old template. These assertions pin the tombstone
	// version, operation, and pre-delete image.
	//
	// userOneID: insert -> update -> hard delete. History ends (v3, DELETE) with
	// the last-known image (Alice Cooper).
	userOneHistory, err := db.TenantUser.ListVersions(context.Background(), userOneID, nil)
	if err != nil {
		t.Fatalf("list versions user one: %v", err)
	}
	if len(userOneHistory) != 3 {
		t.Fatalf("user one history len = %d, want 3 (INSERT, UPDATE, DELETE)", len(userOneHistory))
	}
	userOneTombstone := userOneHistory[len(userOneHistory)-1]
	if userOneTombstone.Operation != "DELETE" || userOneTombstone.Version != 3 {
		t.Fatalf("user one tombstone = (op %q, version %d), want (DELETE, 3)", userOneTombstone.Operation, userOneTombstone.Version)
	}
	if userOneTombstone.Value == nil || userOneTombstone.Value.DisplayName != "Alice Cooper" {
		t.Fatalf("user one tombstone image = %+v, want pre-delete DisplayName Alice Cooper", userOneTombstone.Value)
	}

	// never-updated row: insert then hard delete. History = (v1 INSERT), (v2 DELETE).
	// Inserted after t4 so the as-of snapshots above are unaffected.
	execStrategyASQL(t, pool, ` + "`" + `INSERT INTO tenant_user (id, tenant_id, display_name, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5)` + "`" + `, userThreeID.ToUUID(), tenantID.ToUUID(), "Carol", actorID.ToUUID(), actorID.ToUUID())
	execStrategyASQL(t, pool, ` + "`" + `DELETE FROM tenant_user WHERE id = $1` + "`" + `, userThreeID.ToUUID())

	userThreeHistory, err := db.TenantUser.ListVersions(context.Background(), userThreeID, nil)
	if err != nil {
		t.Fatalf("list versions user three: %v", err)
	}
	if len(userThreeHistory) != 2 {
		t.Fatalf("user three history len = %d, want 2 (INSERT, DELETE)", len(userThreeHistory))
	}
	if userThreeHistory[0].Operation != "INSERT" || userThreeHistory[0].Version != 1 {
		t.Fatalf("user three row 0 = (op %q, version %d), want (INSERT, 1)", userThreeHistory[0].Operation, userThreeHistory[0].Version)
	}
	if userThreeHistory[1].Operation != "DELETE" || userThreeHistory[1].Version != 2 {
		t.Fatalf("user three row 1 = (op %q, version %d), want (DELETE, 2)", userThreeHistory[1].Operation, userThreeHistory[1].Version)
	}
	if userThreeHistory[1].Value == nil || userThreeHistory[1].Value.DisplayName != "Carol" {
		t.Fatalf("user three tombstone image = %+v, want pre-delete DisplayName Carol", userThreeHistory[1].Value)
	}

	// as-of exclusion: a snapshot after the delete must not include userThreeID.
	tsAfterDelete := strategyADBTime(t, pool)
	snapshotAfterDelete := reconstructTenantSnapshotAt(t, db, tenantID, tsAfterDelete)
	for _, u := range snapshotAfterDelete.Users {
		if u.Id != nil && u.Id.ToUUID() == userThreeID.ToUUID() {
			t.Fatalf("userThreeID present in snapshot after hard delete, want excluded")
		}
	}
}

func mustUUID(t *testing.T, raw string) types.IdentityUUID {
	t.Helper()
	id, err := types.ParseIdentityUUID(raw)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", raw, err)
	}
	return id
}

func execStrategyASQL(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec sql %q: %v", sql, err)
	}
}

func strategyADBTime(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var ts time.Time
	if err := pool.QueryRow(context.Background(), "SELECT clock_timestamp()").Scan(&ts); err != nil {
		t.Fatalf("read db timestamp: %v", err)
	}
	return ts
}

func reconstructTenantSnapshotAt(t *testing.T, db *Database, tenantID types.IdentityUUID, ts time.Time) *types.Tenant {
	t.Helper()
	tenant, err := db.Tenant.GetAsOf(context.Background(), tenantID, ts)
	if err != nil {
		t.Fatalf("get tenant as of %s: %v", ts.Format(time.RFC3339Nano), err)
	}
	childRecords, err := db.TenantUser.ListAsOfByTenantID(context.Background(), tenantID, ts, nil)
	if err != nil {
		t.Fatalf("list tenant users as of %s: %v", ts.Format(time.RFC3339Nano), err)
	}
	tenant.Users = make([]types.TenantUser, 0, len(childRecords))
	for _, record := range childRecords {
		tenant.Users = append(tenant.Users, *record.Value)
	}
	return tenant
}

func findSnapshotUser(t *testing.T, users []types.TenantUser, id types.IdentityUUID) types.TenantUser {
	t.Helper()
	for _, user := range users {
		if user.Id != nil && user.Id.ToUUID() == id.ToUUID() {
			return user
		}
	}
	t.Fatalf("user %s not found in snapshot", id.ToUUID())
	return types.TenantUser{}
}
`
	if err := os.WriteFile(filepath.Join(ormDir, "strategy_a_history_test.go"), []byte(strategyATest), 0o644); err != nil {
		t.Fatalf("write strategy a history test: %v", err)
	}

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = ormDir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = ormDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Errorf("generated ORM module does not compile: %v\n%s", err, out)
	}

	test := exec.Command("go", "test", "./...")
	test.Dir = ormDir
	if out, err := test.CombinedOutput(); err != nil {
		t.Errorf("generated ORM module tests fail: %v\n%s", err, out)
	}

	vet := exec.Command("go", "vet", "./...")
	vet.Dir = ormDir
	if out, err := vet.CombinedOutput(); err != nil {
		t.Errorf("generated ORM module fails go vet: %v\n%s", err, out)
	}
}

// extendFixtureForCompileCoverage mutates the loaded fixture-db IR to
// exercise template branches the fixture schema does not reach:
// createdBy/updatedBy user audit fields, scalar arrays, optional enums,
// and optional non-audit datetime scalars. Soft-delete fields (deletedAt/
// deletedBy) now live on the fixture's TenantUser itself. The mutation
// reuses scalars the fixture already resolves so the generated types
// module stays compilable.
func extendFixtureForCompileCoverage(schema *ir.Schema) {
	tenantUser := schema.Types["TenantUser"]
	tenantUser.Fields = append(tenantUser.Fields,
		&ir.FieldDef{Name: "createdBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		&ir.FieldDef{Name: "updatedBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		&ir.FieldDef{Name: "roles", TypeRef: ir.TypeRef{Name: "string", IsArray: true}},
		&ir.FieldDef{Name: "lastStatus", TypeRef: ir.TypeRef{Name: "TenantStatus"}},
		&ir.FieldDef{Name: "lastSeenAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}},
		&ir.FieldDef{Name: "invitedBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}},
	)
}
