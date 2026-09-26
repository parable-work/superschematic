package ormgen

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixturesDir = "../../loader/tsreader/testdata/services"

func generateFixtureDB(t *testing.T) *ORMOutput {
	t.Helper()
	return generateFixture(t, "fixture-db")
}

func generateFixture(t *testing.T, svc string) *ORMOutput {
	t.Helper()

	schema, err := loader.LoadService(filepath.Join(fixturesDir, svc))
	if err != nil {
		t.Fatalf("load %s: %v", svc, err)
	}

	output, err := Generate(schema, Options{
		SchemaName:  svc,
		ModulePath:  "example.com/schemas/orm/" + svc,
		TypesModule: "example.com/schemas/types/go/" + svc,
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil {
		t.Fatalf("expected ORM output for %s", svc)
	}
	return output
}

// TestWriteORMGolden generates the ORM module for the fixture-db and
// fixture-nested-arrays-db services and compares every emitted file against
// its golden copy. Regenerate with:
// go test ./internal/generator/ormgen -run TestWriteORMGolden -update
func TestWriteORMGolden(t *testing.T) {
	common := []string{
		"database.go", "interfaces.go", "query.go", "utils.go",
		"go.mod", "README.md", "Makefile",
	}
	for _, tc := range []struct {
		svc          string
		repositories []string
	}{
		{"fixture-db", []string{"repository_tenant.go", "repository_tenant_user.go"}},
		{"fixture-nested-arrays-db", []string{"repository_board.go"}},
	} {
		t.Run(tc.svc, func(t *testing.T) {
			output := generateFixture(t, tc.svc)

			outDir := t.TempDir()
			if err := WriteORM(output, outDir); err != nil {
				t.Fatalf("write orm: %v", err)
			}

			files := append(append([]string{}, common...), tc.repositories...)
			entries, err := os.ReadDir(outDir)
			if err != nil {
				t.Fatalf("read output dir: %v", err)
			}
			if len(entries) != len(files) {
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("expected %d output files, got %d: %v", len(files), len(entries), names)
			}

			goldenDir := filepath.Join("testdata", "golden", tc.svc)
			for _, name := range files {
				got, err := os.ReadFile(filepath.Join(outDir, name))
				if err != nil {
					t.Fatalf("read generated %s: %v", name, err)
				}

				goldenPath := filepath.Join(goldenDir, name)
				if *update {
					if err := os.MkdirAll(goldenDir, 0o755); err != nil {
						t.Fatalf("create golden dir: %v", err)
					}
					if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
						t.Fatalf("write golden %s: %v", name, err)
					}
					continue
				}

				want, err := os.ReadFile(goldenPath)
				if err != nil {
					t.Fatalf("read golden %s: %v", name, err)
				}
				if string(got) != string(want) {
					t.Errorf("%s differs from golden (run with -update to accept)", name)
				}
			}
		})
	}
}

// TestGenerateFixtureDBShape verifies the extracted repository model against
// the fixture-db schema: UUID plumbing type, relationship wiring, ordered
// members, and audit classification.
func TestGenerateFixtureDBShape(t *testing.T) {
	output := generateFixtureDB(t)

	if output.UUIDGoType != "types.IdentityUUID" {
		t.Errorf("UUIDGoType = %q, want types.IdentityUUID", output.UUIDGoType)
	}
	if output.UserIDGoType != "types.IdentityUUID" {
		t.Errorf("UserIDGoType = %q, want fallback to UUID type", output.UserIDGoType)
	}
	if !output.HasSoftDeletes {
		t.Error("fixture-db TenantUser has deletedAt; HasSoftDeletes should be true")
	}
	if !output.HasVersionedRepositories {
		t.Error("fixture-db Tenant is @versioned; HasVersionedRepositories should be true")
	}
	if !output.HasGenericJSON {
		t.Error("fixture-db Tenant.metadata uses Generic.JSON; HasGenericJSON should be true")
	}

	if len(output.Repositories) != 2 {
		t.Fatalf("expected 2 repositories, got %d", len(output.Repositories))
	}

	tenant := output.Repositories[0]
	if tenant.TypeName != "Tenant" || tenant.Name != "TenantRepository" {
		t.Fatalf("unexpected first repository: %+v", tenant)
	}
	if !tenant.PrimaryKeyIsUUID || tenant.PrimaryKeyType != "types.IdentityUUID" {
		t.Errorf("Tenant PK = %q (uuid=%v), want types.IdentityUUID", tenant.PrimaryKeyType, tenant.PrimaryKeyIsUUID)
	}
	if !tenant.Versioned {
		t.Fatal("Tenant repository should be marked versioned")
	}
	if tenant.HistoryTableName != "tenant_history" || tenant.QuotedHistoryTableName != "tenant_history" {
		t.Errorf("Tenant history table = (%q, %q), want tenant_history", tenant.HistoryTableName, tenant.QuotedHistoryTableName)
	}
	if tenant.HistoryRecordedAtCol != "recorded_at" || tenant.HistoryDataCol != "data" || tenant.HistoryOperationCol != "operation" {
		t.Errorf("Tenant history columns wrong: recorded=%q data=%q operation=%q", tenant.HistoryRecordedAtCol, tenant.HistoryDataCol, tenant.HistoryOperationCol)
	}

	var versionField *Field
	for i := range tenant.Fields {
		if tenant.Fields[i].Name == "_version" {
			versionField = &tenant.Fields[i]
			break
		}
	}
	if versionField == nil {
		t.Fatal("Tenant fields missing synthetic _version")
	}
	if versionField.DBName != "_version" || versionField.GoType != "int64" || !versionField.IsInternalMetadata || !versionField.IsAutoGenerated {
		t.Errorf("Tenant _version field wrong: %+v", *versionField)
	}
	if len(tenant.OrderedMembers) == 0 || tenant.OrderedMembers[len(tenant.OrderedMembers)-1].Field == nil ||
		tenant.OrderedMembers[len(tenant.OrderedMembers)-1].Field.DBName != "_version" {
		t.Errorf("Tenant ordered members should end with _version: %+v", tenant.OrderedMembers)
	}
	var metadataField *Field
	for i := range tenant.Fields {
		if tenant.Fields[i].Name == "metadata" {
			metadataField = &tenant.Fields[i]
			break
		}
	}
	if metadataField == nil || !metadataField.PreservesExplicitJSONNull {
		t.Fatalf("Tenant.metadata must keep an explicit JSON null: %+v", metadataField)
	}
	if len(tenant.Relationships) != 1 || !tenant.Relationships[0].IsArray {
		t.Fatalf("expected one hasMany relationship on Tenant, got %+v", tenant.Relationships)
	}
	users := tenant.Relationships[0]
	if users.HasManyParentRelationField != "Tenant" || users.HasManyParentFilterField != "TenantID" {
		t.Errorf("hasMany parent mapping = (%q, %q), want (Tenant, TenantID)",
			users.HasManyParentRelationField, users.HasManyParentFilterField)
	}

	// hasMany relationships must not participate in column scan order.
	for _, member := range tenant.OrderedMembers {
		if member.IsRelationship {
			t.Errorf("Tenant has unexpected to-one relationship member %q", member.Relationship.FieldName)
		}
	}

	tenantUser := output.Repositories[1]
	if tenantUser.TypeName != "TenantUser" {
		t.Fatalf("unexpected second repository: %+v", tenantUser)
	}
	if !tenantUser.Versioned {
		t.Fatal("TenantUser repository should be marked versioned")
	}
	if len(tenantUser.Relationships) != 1 || tenantUser.Relationships[0].IsArray {
		t.Fatalf("expected one to-one relationship on TenantUser, got %+v", tenantUser.Relationships)
	}
	rel := tenantUser.Relationships[0]
	if rel.DBColumnName != "tenant_id" || rel.TargetType != "Tenant" {
		t.Errorf("relationship = %+v, want tenant_id -> Tenant", rel)
	}

	fieldsByName := map[string]Field{}
	for _, f := range tenantUser.Fields {
		fieldsByName[f.Name] = f
	}
	if f := fieldsByName["createdAt"]; !f.IsAuditField || !f.IsDateTimeScalar {
		t.Errorf("createdAt flags wrong: %+v", f)
	}
	if f := fieldsByName["id"]; !f.IsPrimaryKey || !f.IsUUIDScalar || !f.IsAutoGenerated {
		t.Errorf("id flags wrong: %+v", f)
	}
	if f := fieldsByName["displayName"]; f.IsRequired || f.GoType != "string" {
		t.Errorf("displayName flags wrong: %+v", f)
	}
	if !tenantUser.NeedsSQLNull {
		t.Error("TenantUser has an optional string field; NeedsSQLNull should be true")
	}
}

func TestGenerateSnapshotUpdateBuilder(t *testing.T) {
	output := generateFixtureDB(t)
	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	queryFile, err := os.ReadFile(filepath.Join(outDir, "query.go"))
	if err != nil {
		t.Fatalf("read query.go: %v", err)
	}
	generated := string(queryFile)
	for _, expected := range []string{
		"func NewTenantUserSnapshotUpdate(input *types.TenantUser) *TenantUserUpdate",
		"update.DisplayNameSetNull = true",
		"update.DisplayName = &input.DisplayName",
		"update.IsActive = &input.IsActive",
	} {
		if !strings.Contains(generated, expected) {
			t.Errorf("query.go missing generated snapshot update fragment %q", expected)
		}
	}
}

func TestGenerateApplyTo(t *testing.T) {
	output := generateFixtureDB(t)
	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	queryFile, err := os.ReadFile(filepath.Join(outDir, "query.go"))
	if err != nil {
		t.Fatalf("read query.go: %v", err)
	}
	generated := string(queryFile)
	for _, expected := range []string{
		"func (u *TenantUserUpdate) ApplyTo(row *types.TenantUser)",
		"if u.DisplayNameSetNull {",
		"row.Tenant.Id = u.TenantID",
		"func (u *TenantUpdate) ApplyTo(row *types.Tenant)",
	} {
		if !strings.Contains(generated, expected) {
			t.Errorf("query.go missing generated ApplyTo fragment %q", expected)
		}
	}
}

func TestGenerateVersionedChildHistoryAsOfMethod(t *testing.T) {
	output := generateFixtureDB(t)

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	repoFile, err := os.ReadFile(filepath.Join(outDir, "repository_tenant_user.go"))
	if err != nil {
		t.Fatalf("read repository_tenant_user.go: %v", err)
	}
	if !strings.Contains(string(repoFile), "func (r *TenantUserRepository) ListAsOfByTenantID(") {
		t.Fatal("TenantUser repository should expose ListAsOfByTenantID for Strategy A reconstruction")
	}
	if !strings.Contains(string(repoFile), "func (r *TenantUserRepository) UpdateOneIfVersion(ctx context.Context, id types.IdentityUUID, expectedVersion int64, update *TenantUserUpdate)") {
		t.Fatal("TenantUser repository should expose UpdateOneIfVersion")
	}
	if !strings.Contains(string(repoFile), `query += fmt.Sprintf(" AND _version = $%d", paramNum)`) {
		t.Fatal("TenantUser UpdateOneIfVersion should constrain the update by _version")
	}

	interfacesFile, err := os.ReadFile(filepath.Join(outDir, "interfaces.go"))
	if err != nil {
		t.Fatalf("read interfaces.go: %v", err)
	}
	if !strings.Contains(string(interfacesFile), "ListAsOfByTenantID(ctx context.Context, tenantID types.IdentityUUID, ts time.Time, opts *TenantUserHistoryOptions)") {
		t.Fatal("TenantUser interface should expose ListAsOfByTenantID")
	}
	if !strings.Contains(string(interfacesFile), "UpdateOneIfVersion(ctx context.Context, id types.IdentityUUID, expectedVersion int64, update *TenantUserUpdate)") {
		t.Fatal("TenantUser interface should expose UpdateOneIfVersion")
	}
}

// TestGenerateReferencedByAndAuditTimestampFilters covers the two filters a
// scan over "rows nothing else references, untouched since T" needs. Without
// them the caller pages rows and drops the non-candidates in Go, which can
// never page past a prefix of rows it always drops: once one page's worth of
// permanently-skipped rows exists, the scan stops making progress.
func TestGenerateReferencedByAndAuditTimestampFilters(t *testing.T) {
	output := generateFixtureDB(t)

	var tenant *Repository
	for i := range output.Repositories {
		if output.Repositories[i].TypeName == "Tenant" {
			tenant = &output.Repositories[i]
		}
	}
	if tenant == nil {
		t.Fatal("fixture-db has no Tenant repository")
	}
	if len(tenant.ReferencedBy) != 1 {
		t.Fatalf("Tenant.ReferencedBy = %+v, want the TenantUser.tenant edge", tenant.ReferencedBy)
	}
	ref := tenant.ReferencedBy[0]
	if ref.FilterField != "ReferencedByTenantUserTenant" || ref.QuotedSourceTable != "tenant_user" || ref.QuotedSourceColumn != "tenant_id" {
		t.Fatalf("inbound edge = %+v, want tenant_user.tenant_id", ref)
	}
	if !ref.SourceHasSoftDelete {
		t.Fatal("TenantUser is soft-deletable; the probe needs its IncludeDeleted knob")
	}

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	queryFile, err := os.ReadFile(filepath.Join(outDir, "query.go"))
	if err != nil {
		t.Fatalf("read query.go: %v", err)
	}
	got := string(queryFile)

	for _, want := range []string{
		"ReferencedByTenantUserTenant *ReferencedByFilter",
		"probe := `SELECT 1 FROM tenant_user WHERE tenant_user.tenant_id = tenant.id`",
		"probe += ` AND tenant_user.deleted_at IS NULL`",
		"conditions = append(conditions, `NOT EXISTS (`+probe+`)`)",
		// createdAt/updatedAt are filterable; the rest of the audit set is not.
		"CreatedAt *DateTimeFilter",
		"UpdatedAt *DateTimeFilter",
		"conditions = append(conditions, fmt.Sprintf(`tenant.updated_at < $%d`, paramNum))",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("query.go missing %q", want)
		}
	}
	// deleted_at stays out: the repositories append their own soft-delete
	// predicate, and a second one from a filter would contradict it.
	if strings.Contains(got, "DeletedAt *DateTimeFilter") {
		t.Error("deleted_at must not be filterable; IncludeDeleted owns that predicate")
	}
	if strings.Contains(got, "UpdatedBy *UUIDFilter") {
		t.Error("audit actor columns must stay unfilterable")
	}
}

// TestGeneratePruneHistoryMethod verifies that a versioned table declaring
// retentionDays gets a typed PruneHistory wrapper over the generated SQL
// prune function, on the repository, the interface, and the NoOp stub.
func TestGeneratePruneHistoryMethod(t *testing.T) {
	retentionDays := 90
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID"}
	schema.Types["EventLog"] = &ir.TypeDef{
		Name:            "EventLog",
		Role:            ir.RoleDBTable,
		Versioned:       true,
		VersionedConfig: &ir.VersionedConfig{RetentionDays: &retentionDays},
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true, AutoGenerated: true},
			{Name: "message", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schema.Types["AuditLog"] = &ir.TypeDef{
		Name:      "AuditLog",
		Role:      ir.RoleDBTable,
		Versioned: true,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true, AutoGenerated: true},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName:  "synthetic",
		ModulePath:  "example.com/orm/synthetic",
		TypesModule: "example.com/types/synthetic",
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	var eventLog, auditLog *Repository
	for i := range output.Repositories {
		switch output.Repositories[i].TypeName {
		case "EventLog":
			eventLog = &output.Repositories[i]
		case "AuditLog":
			auditLog = &output.Repositories[i]
		}
	}
	if eventLog == nil || auditLog == nil {
		t.Fatalf("missing repositories: %+v", output.Repositories)
	}
	if !eventLog.HasPruneHistory || eventLog.PruneFunctionName != "event_log_prune_history" {
		t.Fatalf("EventLog prune metadata = (%v, %q), want (true, event_log_prune_history)", eventLog.HasPruneHistory, eventLog.PruneFunctionName)
	}
	if auditLog.HasPruneHistory {
		t.Fatal("AuditLog declares no retentionDays; HasPruneHistory should be false")
	}

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	repoFile, err := os.ReadFile(filepath.Join(outDir, "repository_event_log.go"))
	if err != nil {
		t.Fatalf("read repository_event_log.go: %v", err)
	}
	if !strings.Contains(string(repoFile), "func (r *EventLogRepository) PruneHistory(ctx context.Context, retentionDays, chunkRows int) (int64, error)") {
		t.Fatal("EventLog repository should expose PruneHistory")
	}
	if !strings.Contains(string(repoFile), "SELECT event_log_prune_history($1, $2)") {
		t.Fatal("EventLog PruneHistory should call the generated SQL prune function with a row cap")
	}
	// A first pass over months of accumulated history must not be one
	// unbounded DELETE: the wrapper repeats a capped call until the candidate
	// set is exhausted.
	if !strings.Contains(string(repoFile), "if maxRows == nil || deleted < int64(chunkRows) {") {
		t.Fatal("EventLog PruneHistory should chunk until the backlog is drained")
	}
	// Callers that report per-table prune counts need the pruned table's
	// identity. Without this accessor they re-derive it from the Go field or
	// type name, which is a hand-maintained mirror of a schema-defined name.
	if !strings.Contains(string(repoFile), `func (r *EventLogRepository) PruneHistoryFunctionName() string {
	return "event_log_prune_history"
}`) {
		t.Fatal("EventLog repository should expose the generated prune function name")
	}

	auditFile, err := os.ReadFile(filepath.Join(outDir, "repository_audit_log.go"))
	if err != nil {
		t.Fatalf("read repository_audit_log.go: %v", err)
	}
	if strings.Contains(string(auditFile), "PruneHistory") {
		t.Fatal("AuditLog repository must not expose PruneHistory without retentionDays")
	}

	interfacesFile, err := os.ReadFile(filepath.Join(outDir, "interfaces.go"))
	if err != nil {
		t.Fatalf("read interfaces.go: %v", err)
	}
	if !strings.Contains(string(interfacesFile), "PruneHistory(ctx context.Context, retentionDays, chunkRows int) (int64, error)") {
		t.Fatal("EventLog interface should expose PruneHistory")
	}
	if !strings.Contains(string(interfacesFile), "func (r *NoOpEventLogRepository) PruneHistory(_ context.Context, _, _ int) (int64, error)") {
		t.Fatal("NoOp EventLog repository should stub PruneHistory")
	}
	if !strings.Contains(string(interfacesFile), "PruneHistoryFunctionName() string") {
		t.Fatal("EventLog interface should expose PruneHistoryFunctionName")
	}
	if !strings.Contains(string(interfacesFile), "func (r *NoOpEventLogRepository) PruneHistoryFunctionName() string") {
		t.Fatal("NoOp EventLog repository should stub PruneHistoryFunctionName")
	}
	if strings.Contains(string(auditFile), "PruneHistoryFunctionName") {
		t.Fatal("AuditLog repository must not expose PruneHistoryFunctionName without retentionDays")
	}
}

// TestGenerateDateTimeFilterIsNullPredicate verifies the generated
// DateTimeFilter carries an IsNull predicate and that buildWhereClause emits
// IS NULL / IS NOT NULL for a non-audit DateTime column. Without this a caller
// cannot filter "retractedAt IS NULL" server-side: the
// keyset scan workaround in the revert path existed only because the generated
// DateTime filter had comparison ops but no null predicate.
func TestGenerateDateTimeFilterIsNullPredicate(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID"}
	schema.Scalars["Temporal.DateTime"] = &ir.ScalarDef{Name: "Temporal.DateTime"}

	schema.Types["Record"] = &ir.TypeDef{
		Name: "Record",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true, AutoGenerated: true},
			// Non-audit, nullable datetime: the shape that needs IS NULL filtering.
			{Name: "retractedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName:  "synthetic",
		ModulePath:  "example.com/orm/synthetic",
		TypesModule: "example.com/types/synthetic",
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil {
		t.Fatal("expected output")
	}

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	queryFile, err := os.ReadFile(filepath.Join(outDir, "query.go"))
	if err != nil {
		t.Fatalf("read query.go: %v", err)
	}
	query := string(queryFile)

	// The DateTimeFilter type must expose the IsNull predicate field.
	start := strings.Index(query, "type DateTimeFilter struct {")
	if start == -1 {
		t.Fatal("query.go missing DateTimeFilter struct")
	}
	end := strings.Index(query[start:], "}")
	if end == -1 {
		t.Fatal("DateTimeFilter struct not terminated")
	}
	dtBlock := query[start : start+end]
	// gofmt aligns struct field columns, so match on the name, not exact spacing.
	if !strings.Contains(dtBlock, "IsNull") {
		t.Errorf("DateTimeFilter must carry an IsNull predicate; got:\n%s", dtBlock)
	}

	// buildWhereClause must emit IS NULL / IS NOT NULL for the datetime column.
	if !strings.Contains(query, "`record.retracted_at IS NULL`") {
		t.Error("buildWhereClause should emit IS NULL for a DateTime column")
	}
	if !strings.Contains(query, "`record.retracted_at IS NOT NULL`") {
		t.Error("buildWhereClause should emit IS NOT NULL for a DateTime column")
	}
}

// TestGenerateCreateOnePreservesExplicitPrimaryKey verifies a schema-owned UUID
// can be supplied by a deployment manifest while ordinary creates still use the
// database default.
func TestGenerateCreateOnePreservesExplicitPrimaryKey(t *testing.T) {
	output := generateFixtureDB(t)

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	repoFile, err := os.ReadFile(filepath.Join(outDir, "repository_tenant.go"))
	if err != nil {
		t.Fatalf("read repository_tenant.go: %v", err)
	}
	repo := string(repoFile)
	start := strings.Index(repo, "func (r *TenantRepository) CreateOne(")
	if start == -1 {
		t.Fatal("repository_tenant.go missing CreateOne")
	}
	nextFunc := strings.Index(repo[start+1:], "\nfunc (r *TenantRepository)")
	if nextFunc == -1 {
		t.Fatal("repository_tenant.go CreateOne is not bounded by another method")
	}
	createOne := repo[start : start+1+nextFunc]

	if !strings.Contains(createOne, "if primaryKey, ok := tenantUUIDValue(input.Id)") {
		t.Error("CreateOne must detect an explicit schema-owned primary key")
	}
	if !strings.Contains(createOne, "fields = append(fields, `id`)") {
		t.Error("CreateOne must insert the explicit primary-key column")
	}
	if !strings.Contains(createOne, "values = append(values, primaryKey.ToUUID())") {
		t.Error("CreateOne must bind the explicit primary-key value")
	}
}

// TestGenerateCreateManyIncludesRelationFK verifies the generated CreateMany
// carries the same relation foreign-key columns CreateOne does. The bulk insert
// used to omit Relation<> FK columns entirely: fieldNames
// and the per-row value loop only ranged over scalar fields, so bulk-inserting
// rows with relations silently dropped the FK and callers fell back to per-row
// CreateOne. TenantUser has a required to-one Tenant relationship (tenant_id).
func TestGenerateCreateManyIncludesRelationFK(t *testing.T) {
	output := generateFixtureDB(t)

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	repoFile, err := os.ReadFile(filepath.Join(outDir, "repository_tenant_user.go"))
	if err != nil {
		t.Fatalf("read repository_tenant_user.go: %v", err)
	}
	repo := string(repoFile)

	// Isolate the CreateMany body so the assertions cannot pass on CreateOne's
	// FK handling (which was never the bug).
	start := strings.Index(repo, "func (r *TenantUserRepository) CreateMany(")
	if start == -1 {
		t.Fatal("repository_tenant_user.go missing CreateMany")
	}
	nextFunc := strings.Index(repo[start+1:], "\nfunc (r *TenantUserRepository)")
	var createMany string
	if nextFunc == -1 {
		createMany = repo[start:]
	} else {
		createMany = repo[start : start+1+nextFunc]
	}

	// The FK column must join the fixed column list and each row's values.
	if !strings.Contains(createMany, `fieldNames = append(fieldNames, "tenant_id")`) {
		t.Error("CreateMany column list must include the tenant_id relation FK")
	}
	if !strings.Contains(createMany, "values = append(values, relID.ToUUID())") {
		t.Error("CreateMany per-row values must bind the tenant_id relation FK")
	}
}

// TestGenerateSkipsNonTableSchemas verifies that schemas without DBTable
// types produce no ORM output.
func TestGenerateSkipsNonTableSchemas(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}

	output, err := Generate(schema, Options{SchemaName: "fixture-api"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output != nil {
		t.Fatalf("expected nil output for API schema, got %d repositories", len(output.Repositories))
	}
}

// TestGenerateSyntheticAuditAndSoftDelete exercises createdBy/updatedBy user
// audit typing, soft-delete detection, injected primary keys, and the
// many-to-many skip on a hand-built IR.
func TestGenerateSyntheticAuditAndSoftDelete(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID"}
	schema.Scalars["Identity.UserID"] = &ir.ScalarDef{Name: "Identity.UserID"}
	schema.Scalars["Temporal.DateTime"] = &ir.ScalarDef{Name: "Temporal.DateTime"}

	schema.Types["Document"] = &ir.TypeDef{
		Name: "Document",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true, AutoGenerated: true},
			{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "createdBy", TypeRef: ir.TypeRef{Name: "Identity.UserID"}, Required: true},
			{Name: "updatedBy", TypeRef: ir.TypeRef{Name: "Identity.UserID"}, Required: true},
			{Name: "deletedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}},
			{Name: "deletedBy", TypeRef: ir.TypeRef{Name: "Identity.UserID"}},
			{Name: "labels", TypeRef: ir.TypeRef{Name: "Label", IsArray: true}, ManyToMany: true},
		},
	}
	schema.Types["Label"] = &ir.TypeDef{
		Name: "Label",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName:  "synthetic",
		ModulePath:  "example.com/orm/synthetic",
		TypesModule: "example.com/types/synthetic",
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil {
		t.Fatal("expected output")
	}

	if output.UserIDGoType != "types.IdentityUserID" {
		t.Errorf("UserIDGoType = %q, want types.IdentityUserID", output.UserIDGoType)
	}
	if !output.HasSoftDeletes {
		t.Error("expected HasSoftDeletes")
	}

	var document, label *Repository
	for i := range output.Repositories {
		switch output.Repositories[i].TypeName {
		case "Document":
			document = &output.Repositories[i]
		case "Label":
			label = &output.Repositories[i]
		}
	}
	if document == nil || label == nil {
		t.Fatalf("missing repositories: %+v", output.Repositories)
	}

	if !document.HasSoftDelete || !document.HasDeletedBy {
		t.Errorf("Document soft delete flags wrong: softDelete=%v deletedBy=%v", document.HasSoftDelete, document.HasDeletedBy)
	}
	if len(document.Relationships) != 0 {
		t.Errorf("manyToMany must be skipped; got relationships %+v", document.Relationships)
	}
	if !document.NeedsTime {
		t.Error("soft-deleted table needs the time import")
	}

	// Label has no @key field: an auto-generated UUID id is injected first.
	if label.PrimaryKeyCol != "id" || !label.PrimaryKeyIsUUID {
		t.Errorf("Label PK = %q (uuid=%v), want injected id", label.PrimaryKeyCol, label.PrimaryKeyIsUUID)
	}
	if label.Fields[0].Name != "id" || !label.Fields[0].IsAutoGenerated {
		t.Errorf("Label first field = %+v, want injected id", label.Fields[0])
	}
	if !label.OrderedMembers[0].Field.IsPrimaryKey {
		t.Error("injected id must lead the ordered members")
	}
	if label.PrimaryKeyType != "types.IdentityUUID" {
		t.Errorf("Label PK type = %q, want types.IdentityUUID", label.PrimaryKeyType)
	}
}

// TestOptionalMapFieldsAreNilable checks every optional map value shape: the
// map itself is the nilable value (never dereferenced, never compared with a
// zero value), and the update holds the same map[string]*T typegen emits.
// TestGeneratedORMCompiles compiles the shapes the types module supports.
func TestOptionalMapFieldsAreNilable(t *testing.T) {
	schema := ir.NewSchema("maps", ir.SchemaKindDB)
	schema.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID"}
	schema.Scalars["Temporal.DateTime"] = &ir.ScalarDef{Name: "Temporal.DateTime"}
	schema.Enums["Status"] = &ir.EnumDef{Name: "Status", Values: []ir.EnumValueDef{{Name: "On", SerializedAs: "on"}}}
	schema.Types["Detail"] = &ir.TypeDef{
		Name:   "Detail",
		Role:   ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{{Name: "note", TypeRef: ir.TypeRef{Name: "string"}, Required: true}},
	}
	schema.Types["Record"] = &ir.TypeDef{
		Name: "Record",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "labels", TypeRef: ir.TypeRef{Name: "string", IsMap: true}},
			{Name: "statusByName", TypeRef: ir.TypeRef{Name: "Status", IsMap: true}},
			{Name: "seenAtByName", TypeRef: ir.TypeRef{Name: "Temporal.DateTime", IsMap: true}},
			{Name: "detailsByName", TypeRef: ir.TypeRef{Name: "Detail", IsMap: true}},
			{Name: "detailListsByName", TypeRef: ir.TypeRef{Name: "Detail", IsMap: true, IsArray: true}},
			{Name: "requiredLabels", TypeRef: ir.TypeRef{Name: "string", IsMap: true}, Required: true},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName:  "maps",
		ModulePath:  "example.com/orm/maps",
		TypesModule: "example.com/types/maps",
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	fields := map[string]Field{}
	for _, field := range output.Repositories[0].Fields {
		fields[field.Name] = field
	}
	for _, name := range []string{"labels", "statusByName", "seenAtByName", "detailsByName", "detailListsByName"} {
		field := fields[name]
		if !field.OptionalNilCheck || field.DerefValue || field.IsNullableEnum || field.IsNullableScalar || !field.NullableMapValues {
			t.Errorf("%s: optional map flags = %+v, want a nil-checked, non-dereferenced map of *T", name, field)
		}
	}
	if fields["requiredLabels"].NullableMapValues {
		t.Error("a required map holds values, not pointers")
	}
	if output.Repositories[0].NeedsSQLNull {
		t.Error("an optional map of string is a JSON column and needs no sql.NullString")
	}

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	queryFile, err := os.ReadFile(filepath.Join(outDir, "query.go"))
	if err != nil {
		t.Fatalf("read query.go: %v", err)
	}
	// gofmt aligns struct fields; compare with runs of blanks collapsed.
	generated := strings.Join(strings.Fields(string(queryFile)), " ")
	for _, want := range []string{
		"Labels *map[string]*string",
		"StatusByName *map[string]*types.Status",
		"SeenAtByName *map[string]*types.TemporalDateTime",
		"DetailsByName *map[string]*types.Detail",
		"DetailListsByName *map[string][]*types.Detail",
		"RequiredLabels *map[string]string",
		"update.StatusByName = &input.StatusByName",
		"row.StatusByName = nil",
		"row.StatusByName = *u.StatusByName",
		"row.DetailsByName = nil",
	} {
		if !strings.Contains(generated, want) {
			t.Errorf("query.go missing %q", want)
		}
	}
	for _, unwanted := range []string{"var zeroLabels", "var zeroStatusByName", "var zeroSeenAtByName", "var zeroDetailsByName", "row.SeenAtByName = u.SeenAtByName"} {
		if strings.Contains(generated, unwanted) {
			t.Errorf("query.go has %q; an optional map is nil-checked, not zero-compared or dereferenced", unwanted)
		}
	}
}

// TestHasManyWithoutDirectiveFails verifies that a list of a table type
// without @hasMany or @manyToMany is rejected.
func TestHasManyWithoutDirectiveFails(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID"}
	schema.Types["Parent"] = &ir.TypeDef{
		Name: "Parent",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "children", TypeRef: ir.TypeRef{Name: "Child", IsArray: true}, Required: true},
		},
	}
	schema.Types["Child"] = &ir.TypeDef{
		Name: "Child",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}

	_, err := Generate(schema, Options{SchemaName: "synthetic"})
	if err == nil || !strings.Contains(err.Error(), "lacks @hasMany") {
		t.Fatalf("expected hasMany validation error, got %v", err)
	}
}

// TestMapIRToGoType verifies primitive, scalar-symbol, and named-type mapping.
func TestMapIRToGoType(t *testing.T) {
	scalars := map[string]scalarLookup{
		"Identity.UUID": {symbol: "IdentityUUID"},
	}

	cases := map[string]string{
		"string":        "string",
		"number":        "float64",
		"boolean":       "bool",
		"Identity.UUID": "types.IdentityUUID",
		"TenantStatus":  "types.TenantStatus",
	}
	for irType, want := range cases {
		if got := mapIRToGoType(irType, scalars); got != want {
			t.Errorf("mapIRToGoType(%q) = %q, want %q", irType, got, want)
		}
	}
}

// TestGenerateCreateManyPreservesExplicitPrimaryKey verifies the batch insert
// carries supplied schema-owned UUIDs the same way CreateOne does, and refuses
// a batch that mixes explicit and default primary keys rather than silently
// dropping the supplied ids.
func TestGenerateCreateManyPreservesExplicitPrimaryKey(t *testing.T) {
	output := generateFixtureDB(t)

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	repoFile, err := os.ReadFile(filepath.Join(outDir, "repository_tenant.go"))
	if err != nil {
		t.Fatalf("read repository_tenant.go: %v", err)
	}
	repo := string(repoFile)
	start := strings.Index(repo, "func (r *TenantRepository) CreateMany(")
	if start == -1 {
		t.Fatal("repository_tenant.go missing CreateMany")
	}
	nextFunc := strings.Index(repo[start+1:], "\nfunc (r *TenantRepository)")
	if nextFunc == -1 {
		t.Fatal("repository_tenant.go CreateMany is not bounded by another method")
	}
	createMany := repo[start : start+1+nextFunc]

	if !strings.Contains(createMany, "if primaryKey, ok := tenantUUIDValue(inputs[0].Id)") {
		t.Error("CreateMany must detect an explicit schema-owned primary key on the first input")
	}
	if !strings.Contains(createMany, "inputs mix explicit and default primary keys") {
		t.Error("CreateMany must refuse a batch mixing explicit and default primary keys")
	}
	if !strings.Contains(createMany, "values = append(values, primaryKey.ToUUID())") {
		t.Error("CreateMany must bind the explicit primary-key values")
	}
}

// TestJSONUnionFieldsUseWrapperDispatch: a JSONB column typed with a closed
// union imported from a dependency decodes through the union's Wrapper, the
// nullable one stays a nil interface, and the ORM's go.mod replaces the
// dependency's types module.
func TestJSONUnionFieldsUseWrapperDispatch(t *testing.T) {
	contracts := ir.NewSchema("contracts", ir.SchemaKindGeneral)
	createdKind := "created"
	contracts.Types["CreatedRevision"] = &ir.TypeDef{
		Name: "CreatedRevision",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &createdKind},
		},
	}
	contracts.Unions["RevisionRef"] = &ir.UnionDef{Name: "RevisionRef", Types: []string{"CreatedRevision"}}

	db := ir.NewSchema("union-db", ir.SchemaKindDB)
	db.Imports = []ir.Import{{Package: "@schemas/contracts", Types: []string{"RevisionRef"}}}
	db.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString}
	db.Types["Event"] = &ir.TypeDef{
		Name: "Event",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "createdAgainst", TypeRef: ir.TypeRef{Name: "RevisionRef"}, Required: true, JsonField: true},
			{Name: "supersededBy", TypeRef: ir.TypeRef{Name: "RevisionRef"}, JsonField: true},
		},
	}

	output, err := Generate(db, Options{
		SchemaName:   "union-db",
		ModulePath:   "example.com/schemas/orm/union-db",
		TypesModule:  "example.com/schemas/types/go/union-db",
		Dependencies: map[string]*ir.Schema{"contracts": contracts},
		Clock:        codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(output.Repositories) != 1 {
		t.Fatalf("repositories = %d, want 1", len(output.Repositories))
	}
	if len(output.ModuleDependencyReplaces) != 1 ||
		output.ModuleDependencyReplaces[0].Module != "example.com/schemas/types/go/contracts" ||
		output.ModuleDependencyReplaces[0].RelPath != "../../types/go/contracts" {
		t.Fatalf("dependency replaces = %+v", output.ModuleDependencyReplaces)
	}
	fields := map[string]Field{}
	for _, field := range output.Repositories[0].Fields {
		fields[field.Name] = field
	}
	for _, name := range []string{"createdAgainst", "supersededBy"} {
		field := fields[name]
		if !field.IsJSONField || !field.IsUnion || field.JSONUnionDecoder == "" {
			t.Fatalf("%s not classified as a JSON union: %+v", name, field)
		}
		if field.DerefValue {
			t.Fatalf("%s must stay a nilable union interface, not a pointer: %+v", name, field)
		}
	}

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("WriteORM: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(outDir, "repository_event.go"))
	if err != nil {
		t.Fatalf("read repository: %v", err)
	}
	for _, want := range []string{
		"var wrapper types.RevisionRefWrapper",
		"decodeEventCreatedAgainstJSONUnion",
		"decodeEventSupersededByJSONUnion",
		"result.CreatedAgainst = decoded",
		"result.SupersededBy = decoded",
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("generated repository missing %q", want)
		}
	}
	module, err := os.ReadFile(filepath.Join(outDir, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(module), "replace example.com/schemas/types/go/contracts => ../../types/go/contracts") {
		t.Fatalf("go.mod lacks the dependency replace:\n%s", module)
	}
}

// TestGenerateRefusesTablesWithoutAUUIDScalar: the ORM keys rows, filters
// and its user context by a UUID scalar's Go type. A schema whose tables
// reach no UUID scalar has no such type in its types package, so Generate
// refuses it by name instead of writing an ORM that does not compile.
func TestGenerateRefusesTablesWithoutAUUIDScalar(t *testing.T) {
	schema := ir.NewSchema("slugs-only", ir.SchemaKindDB)
	schema.Scalars["Identity.Slug"] = &ir.ScalarDef{Name: "Identity.Slug"}
	schema.Types["Page"] = &ir.TypeDef{
		Name: "Page",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "slug", TypeRef: ir.TypeRef{Name: "Identity.Slug"}, Required: true, Key: true},
			{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName:  "slugs-only",
		ModulePath:  "example.com/orm/slugs-only",
		TypesModule: "example.com/types/slugs-only",
	})
	if err == nil {
		t.Fatalf("Generate accepted a schema without a UUID scalar (UUIDGoType %q)", output.UUIDGoType)
	}
	for _, want := range []string{"slugs-only", "UUID scalar"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// A required enum with a declared default inserts that default when the
// caller leaves the Go field at "" (never an enum member), whether the enum
// is local or imported from a dependency schema. Without it a struct literal
// that omits the field inserts an empty string and fails the column's CHECK.
// An optional enum and a required enum without a default keep the plain
// insert.
func TestRequiredEnumDefaultAppliesToLocalAndImportedEnums(t *testing.T) {
	enums := ir.NewSchema("enums", ir.SchemaKindGeneral)
	enums.Enums["OrderStatus"] = &ir.EnumDef{Name: "OrderStatus"}

	db := ir.NewSchema("order-db", ir.SchemaKindDB)
	db.Imports = []ir.Import{{Package: "@schemas/enums", Types: []string{"OrderStatus"}}}
	db.Enums["OrderPriority"] = &ir.EnumDef{Name: "OrderPriority"}
	db.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString}
	pending, normal := "pending", "normal"
	db.Types["Order"] = &ir.TypeDef{
		Name: "Order",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "status", TypeRef: ir.TypeRef{Name: "OrderStatus"}, Required: true, Default: &pending},
			{Name: "priority", TypeRef: ir.TypeRef{Name: "OrderPriority"}, Required: true, Default: &normal},
			{Name: "previousStatus", TypeRef: ir.TypeRef{Name: "OrderStatus"}},
			{Name: "fallbackStatus", TypeRef: ir.TypeRef{Name: "OrderStatus"}, Required: true},
		},
	}

	output, err := Generate(db, Options{
		SchemaName:   "order-db",
		ModulePath:   "example.com/schemas/orm/order-db",
		TypesModule:  "example.com/schemas/types/go/order-db",
		Dependencies: map[string]*ir.Schema{"enums": enums},
		Clock:        codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	fields := map[string]Field{}
	for _, field := range output.Repositories[0].Fields {
		fields[field.Name] = field
	}
	for name, want := range map[string]string{
		"status":         "pending",
		"priority":       "normal",
		"previousStatus": "",
		"fallbackStatus": "",
	} {
		if got := fields[name].EnumDefault; got != want {
			t.Errorf("%s EnumDefault = %q, want %q", name, got, want)
		}
	}

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("WriteORM: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(outDir, "repository_order.go"))
	if err != nil {
		t.Fatalf("read repository: %v", err)
	}
	for _, want := range []string{
		`values = append(values, types.OrderStatus("pending"))`,
		`values = append(values, types.OrderPriority("normal"))`,
	} {
		if n := strings.Count(string(source), want); n != 2 {
			t.Errorf("CreateOne and CreateMany must each insert the default; found %d of %q", n, want)
		}
	}
}
