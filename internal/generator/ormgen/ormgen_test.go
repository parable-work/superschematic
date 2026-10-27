package ormgen

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
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
		// fixture-queue-db lists the sqlite dialect, whose queues the ORM
		// claims on SQLite too.
		SQLite: svc == queueFixture,
		Clock:  codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil {
		t.Fatalf("expected ORM output for %s", svc)
	}
	return output
}

// TestWriteORMGolden generates the ORM module for the fixture-db,
// fixture-nested-arrays-db, fixture-version-graph-db, fixture-optimistic-db
// and fixture-user-model-db services and compares every emitted file
// against its golden copy. Regenerate with:
// go test ./internal/generator/ormgen -run TestWriteORMGolden -update
func TestWriteORMGolden(t *testing.T) {
	common := []string{
		"database.go", "interfaces.go", "query.go", "utils.go",
		"go.mod", "README.md", "Makefile",
		// An upsert's result and a transaction's options (D89).
		"upsert.go", "transaction.go",
	}
	for _, tc := range []struct {
		svc          string
		repositories []string
	}{
		{"fixture-db", []string{"repository_tenant.go", "repository_tenant_user.go", "search.go"}},
		{"fixture-nested-arrays-db", []string{"repository_board.go"}},
		{"fixture-version-graph-db", []string{
			"repository_cover.go", "repository_ingredient.go", "repository_note.go", "repository_recipe.go",
			"repository_recipe_commit.go", "repository_recipe_patch.go", "repository_recipe_ref.go", "repository_recipe_release.go",
			"repository_recipe_snapshot_entry.go", "repository_step.go", "repository_tasting.go",
			"repository_utensil.go", "versiongraph_recipe.go",
		}},
		{"fixture-optimistic-db", []string{"repository_shelf.go", "repository_stock.go"}},
		{"fixture-user-model-db", []string{
			"repository_role.go", "repository_session.go", "repository_user.go",
			"repository_user_credential.go", "repository_user_role_grant.go",
		}},
		{"fixture-queue-db", []string{"repository_order.go", "repository_order_line.go", "repository_product.go", "queues.go", "queues_sqlite.go"}},
		// The tables both ORMs are held to (D79).
		{"fixture-ledger-db", []string{"repository_account.go", "repository_entry.go", "queues.go", "search.go"}},
		// Encrypted<T> fields (D85): encrypted.go seals and opens them.
		{"fixture-vault-db", []string{"repository_api_credential.go", "repository_plaid_item.go", "encrypted.go", "search.go"}},
		// Tables and columns @table and @column name (D85).
		{"fixture-naming-db", []string{"queues.go", "repository_category.go", "repository_line_item.go", "repository_note.go", "search.go"}},
		// Vectors and decimals, and Nearest (D84).
		{"fixture-vectors-db", []string{"repository_chunk.go", "repository_ledger_line.go", "repository_passage.go", "repository_text_embedding.go", "nearest.go", "encrypted.go"}},
		// A bank sync's upserts of many rows, only if changed (D89).
		{"fixture-sync-db", []string{"repository_bank_account.go", "repository_line_item.go", "queues.go"}},
		// Declared queries (D86): queries.go runs them.
		{"fixture-queries-db", []string{"repository_account.go", "repository_category.go", "repository_line_item.go", "repository_transfer_group.go", "queries.go"}},
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

// repositoryMethodsParagraph is the README paragraph that lists the
// repository methods, the fenced and history methods of a versioned table
// among them. It is the same for every schema without an @optimistic table.
const repositoryMethodsParagraph = "Repositories expose `GetOne`, `GetManyByIDs`, `FindOne`, `FindMany`,\n" +
	"`FindPage`, `CreateOne`, `CreateMany`, `UpdateOne`, `UpdateMany`, `DeleteOne`,\n" +
	"and `DeleteMany`. Versioned tables also expose `GetVersion`, `ListVersions`,\n" +
	"`GetAsOf`, `UpdateOneIfVersion`, and `DeleteOneIfVersion`, which soft-deletes\n" +
	"a table with soft deletes and hard-deletes one without. Tables with soft\n" +
	"deletes additionally expose `HardDeleteOne`.\n\n"

// optimisticMethodsSentence follows repositoryMethodsParagraph's last
// sentence, in the same paragraph, when a table is @optimistic: such a table
// has the fenced methods and no history.
const optimisticMethodsSentence = "deletes additionally expose `HardDeleteOne`.\n" +
	"Optimistic tables expose `UpdateOneIfVersion` and `DeleteOneIfVersion` too,\n" +
	"but keep no history, so they have no `GetVersion`, `ListVersions`, or\n" +
	"`GetAsOf`.\n\n"

// TestREADMEListsVersionFencedMethods: the README names the methods a
// versioned table adds, in the same words for every schema, and says what an
// @optimistic table has only when the schema declares one.
func TestREADMEListsVersionFencedMethods(t *testing.T) {
	for _, tc := range []struct {
		svc        string
		optimistic bool
	}{
		{"fixture-db", false},
		{"fixture-nested-arrays-db", false},
		{"fixture-version-graph-db", false},
		{"fixture-optimistic-db", true},
		{"fixture-user-model-db", false},
	} {
		t.Run(tc.svc, func(t *testing.T) {
			outDir := t.TempDir()
			if err := WriteORM(generateFixture(t, tc.svc), outDir); err != nil {
				t.Fatalf("write orm: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(outDir, "README.md"))
			if err != nil {
				t.Fatal(err)
			}
			readme := string(raw)
			want := repositoryMethodsParagraph
			if tc.optimistic {
				want = strings.TrimSuffix(repositoryMethodsParagraph, "deletes additionally expose `HardDeleteOne`.\n\n") + optimisticMethodsSentence
			}
			if !strings.Contains(readme, want) {
				t.Errorf("README.md does not contain\n%s\ngot\n%s", want, readme)
			}
			if !tc.optimistic && strings.Contains(readme, "Optimistic tables") {
				t.Error("README.md describes optimistic tables for a schema without one")
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
		"conditions = append(conditions, fmt.Sprintf(`NOT EXISTS (%s)`, probe))",
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

// TestGenerateSearchTextFilter covers D62's search in the ORM (amended): a
// table with search fields (fixture-db's Tenant.name) gets SearchText on its
// filter, each of whose words is an ILIKE condition on its search_text
// column, escaped, and search.go, which writes the words and their
// patterns. A table without search fields (TenantUser) gets neither field
// nor condition, and a schema without any gets no search.go, which a later
// build removes. Every table gets <Type>FilterConditions.
func TestGenerateSearchTextFilter(t *testing.T) {
	output := generateFixtureDB(t)
	if !output.HasSearch {
		t.Fatal("fixture-db's Tenant.name is a search field; the output should have search")
	}
	for _, repo := range output.Repositories {
		if want := repo.TypeName == "Tenant"; repo.HasSearch != want {
			t.Errorf("%s.HasSearch = %t, want %t", repo.TypeName, repo.HasSearch, want)
		}
	}

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}
	query := read("query.go")
	block := func(start string) string {
		t.Helper()
		i := strings.Index(query, start)
		if i < 0 {
			t.Fatalf("query.go has no %q", start)
		}
		end := strings.Index(query[i:], "\n}\n")
		return query[i : i+end]
	}
	if !regexp.MustCompile(`\n\s+SearchText\s+\*string\b`).MatchString(block("type TenantFilter struct {")) {
		t.Error("TenantFilter should have SearchText *string")
	}
	if strings.Contains(block("type TenantUserFilter struct {"), "SearchText") {
		t.Error("TenantUserFilter has no search fields, so no SearchText")
	}
	condition := "conditions = append(conditions, fmt.Sprintf(`tenant.search_text ILIKE $%d ESCAPE '\\'`, paramNum))"
	if !strings.Contains(block("func (f *TenantFilter) conditions("), condition) {
		t.Errorf("TenantFilter's conditions should hold %s", condition)
	}
	if strings.Contains(block("func (f *TenantUserFilter) conditions("), "search_text") {
		t.Error("TenantUserFilter's conditions should not touch search_text")
	}
	for _, want := range []string{
		"func TenantFilterConditions(filter *TenantFilter, args *[]any) []string {",
		"func TenantUserFilterConditions(filter *TenantUserFilter, args *[]any) []string {",
		"*args = append(*args, searchLikePattern(term))",
	} {
		if !strings.Contains(query, want) {
			t.Errorf("query.go is missing %q", want)
		}
	}
	search := read("search.go")
	for _, want := range []string{
		"const searchMaxTerms = 8",
		"func searchTerms(text string) []string {",
		"func searchLikePattern(term string) string {",
	} {
		if !strings.Contains(search, want) {
			t.Errorf("search.go is missing %q", want)
		}
	}

	// A schema without search fields writes no search.go, and removes one
	// an earlier build wrote.
	plain := generateFixture(t, queueFixture)
	if plain.HasSearch {
		t.Fatalf("%s has no search fields", queueFixture)
	}
	if err := WriteORM(plain, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "search.go")); !os.IsNotExist(err) {
		t.Errorf("search.go after a build without search fields: %v, want it removed", err)
	}
	if strings.Contains(read("query.go"), "SearchText") {
		t.Errorf("%s's query.go should not mention SearchText", queueFixture)
	}
}

// sortFieldNames are the schema names of repo's sort fields, in order.
func sortFieldNames(repo Repository) []string {
	names := make([]string, len(repo.SortFields))
	for i, field := range repo.SortFields {
		names[i] = field.Name
	}
	return names
}

// TestGenerateFindPage covers FindPage's surface in the ORM (D60, amended):
// fixture-db's Tenant sorts by its id, then by createdAt, name, slug, email
// and seatCount, each a required column of one value, and not by its enum
// status, its boolean isActive, its JSON metadata or its nullable
// updatedAt; TenantUser's nullable displayName is no sort field either.
// The repository takes its columns from a generated map, FindPage is on
// the interface and the NoOp repository, and the README lists it.
func TestGenerateFindPage(t *testing.T) {
	output := generateFixtureDB(t)
	want := map[string][]string{
		"Tenant":     {"id", "createdAt", "name", "slug", "email", "seatCount"},
		"TenantUser": {"id", "createdAt"},
	}
	for _, repo := range output.Repositories {
		if got := sortFieldNames(repo); strings.Join(got, ",") != strings.Join(want[repo.TypeName], ",") {
			t.Errorf("%s.SortFields = %q, want %q", repo.TypeName, got, want[repo.TypeName])
		}
	}

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}
	query := read("query.go")
	for _, want := range []string{
		`var ErrPageKey = errors.New("orm: the page key names no row of its sort")`,
		`TenantSortById        TenantSortField = "id"`,
		`TenantSortByCreatedAt TenantSortField = "createdAt"`,
		`TenantSortByName      TenantSortField = "name"`,
		`TenantSortBySeatCount TenantSortField = "seatCount"`,
		`TenantUserSortByCreatedAt TenantUserSortField = "createdAt"`,
		"func ParseTenantSort(text string) (TenantSort, error) {",
		`fmt.Errorf("orm: Tenant sorts by id, createdAt, name, slug, email and seatCount, not %q", text)`,
		"type TenantPageKey struct {\n\tId types.IdentityUUID `json:\"id\"`\n}",
		"return k.Id.ToUUID(), nil",
		"func tenantPageKeyOf(row *types.Tenant) *TenantPageKey {",
		"type TenantPageOptions struct {",
		"type TenantPage struct {",
	} {
		if !strings.Contains(query, want) {
			t.Errorf("query.go is missing %q", want)
		}
	}
	for _, unwanted := range []string{"TenantSortByStatus", "TenantSortByIsActive", "TenantSortByMetadata", "TenantSortByUpdatedAt", "TenantSortByVersion", "TenantUserSortByDisplayName", "TenantUserSortByDeletedAt"} {
		if strings.Contains(query, unwanted) {
			t.Errorf("query.go declares %s, which is no sort field", unwanted)
		}
	}

	repository := read("repository_tenant.go")
	for _, want := range []string{
		"var tenantSortColumns = map[TenantSortField]struct{ sql, key string }{",
		"TenantSortByName:      {`tenant.\"name\"`, `page_key.\"name\"`},",
		"func (r *TenantRepository) FindPage(ctx context.Context, filter *TenantFilter, opts *TenantPageOptions) (*TenantPage, error) {",
		"column, ok := tenantSortColumns[field]",
		"conditions = append(conditions, fmt.Sprintf(`tenant.id %s %s`, comparison, placeholder))",
		"conditions = append(conditions, fmt.Sprintf(`(%s, tenant.id) %s ((SELECT %s FROM tenant AS page_key WHERE page_key.id = %s), %s)`,",
		"query := `SELECT EXISTS (SELECT 1 FROM tenant WHERE tenant.id = $1)`",
		"query := fmt.Sprintf(`SELECT %s FROM tenant%s%s LIMIT %d`, selectFields, whereClause, orderByClause, limit+1)",
		`rows, err := r.findRows(ctx, "FindPage", query, args, fieldStrings, opts.Fields)`,
		`results, err := r.findRows(ctx, "FindMany", query, args, fieldStrings, opts.Fields)`,
	} {
		if !strings.Contains(repository, want) {
			t.Errorf("repository_tenant.go is missing %q", want)
		}
	}
	if strings.Contains(repository, "tenant.deleted_at") {
		t.Error("Tenant has no soft deletes, so FindPage adds no deleted_at condition")
	}
	if want := "conditions = append(conditions, `tenant_user.deleted_at IS NULL`)"; !strings.Contains(read("repository_tenant_user.go"), want) {
		t.Errorf("repository_tenant_user.go is missing %q", want)
	}

	interfaces := read("interfaces.go")
	for _, want := range []string{
		"FindPage(ctx context.Context, filter *TenantFilter, opts *TenantPageOptions) (*TenantPage, error)",
		"func (r *NoOpTenantRepository) FindPage(_ context.Context, _ *TenantFilter, _ *TenantPageOptions) (*TenantPage, error) {",
	} {
		if !strings.Contains(interfaces, want) {
			t.Errorf("interfaces.go is missing %q", want)
		}
	}
	if !strings.Contains(read("README.md"), "`FindPage(ctx, filter, opts)` reads a page") {
		t.Error("README.md does not describe FindPage")
	}
}

// sortRulesSchema is a table with a column of each kind that the sort
// rule (Field.Sortable) takes or leaves out, and a key that is not named
// id.
const sortRulesSchema = `{
  "name": "sort-rules",
  "kind": "DB",
  "scalars": {
    "Identity.UUID": { "name": "Identity.UUID", "languagePrimitive": "string" },
    "Temporal.DateTime": { "name": "Temporal.DateTime", "languagePrimitive": "string" },
    "Temporal.Date": { "name": "Temporal.Date", "languagePrimitive": "string" },
    "Geo.Location": { "name": "Geo.Location", "languagePrimitive": "string" },
    "Generic.Int64": { "name": "Generic.Int64", "languagePrimitive": "number" },
    "Generic.JSON": { "name": "Generic.JSON", "languagePrimitive": "string" }
  },
  "enums": {
    "Mood": { "name": "Mood", "values": [{ "name": "Calm", "serializedAs": "calm" }, { "name": "Busy", "serializedAs": "busy" }] }
  },
  "types": {
    "Entry": {
      "name": "Entry",
      "role": "DBTable",
      "fields": [
        { "name": "entryId", "typeRef": { "name": "Identity.UUID" }, "required": true, "key": true, "autoGenerated": true },
        { "name": "title", "typeRef": { "name": "string" }, "required": true },
        { "name": "token", "typeRef": { "name": "string" }, "required": true, "secret": true },
        { "name": "subtitle", "typeRef": { "name": "string" } },
        { "name": "rank", "typeRef": { "name": "Generic.Int64" }, "required": true },
        { "name": "score", "typeRef": { "name": "number" }, "required": true },
        { "name": "due", "typeRef": { "name": "Temporal.Date" }, "required": true },
        { "name": "ownerId", "typeRef": { "name": "Identity.UUID" }, "required": true },
        { "name": "done", "typeRef": { "name": "boolean" }, "required": true },
        { "name": "mood", "typeRef": { "name": "Mood" }, "required": true },
        { "name": "at", "typeRef": { "name": "Geo.Location" }, "required": true },
        { "name": "tags", "typeRef": { "name": "string", "isArray": true }, "required": true },
        { "name": "extra", "typeRef": { "name": "Generic.JSON" }, "required": true },
        { "name": "createdAt", "typeRef": { "name": "Temporal.DateTime" }, "required": true },
        { "name": "createdBy", "typeRef": { "name": "Identity.UUID" }, "required": true },
        { "name": "updatedAt", "typeRef": { "name": "Temporal.DateTime" }, "required": true }
      ]
    }
  }
}
`

// sortRulesService writes sortRulesSchema as a service into a temporary
// directory, and returns it.
func sortRulesService(t *testing.T) string {
	t.Helper()
	serviceDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(serviceDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "schema.config.json"), []byte(`{"name": "sort-rules", "kind": "DB", "outputs": {"types": {"go": {"enabled": true}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "src", "entries.schema.json"), []byte(sortRulesSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	return serviceDir
}

// TestSortableColumns holds the sort rule (Field.Sortable, D60 amended): a
// table sorts by its key, whatever its name, first, then by each required
// column of one value of text, a number, a time, a date or a UUID, and by
// createdAt and updatedAt among the audit fields; not by a nullable column,
// a @secret one, a boolean, an enum, a point, a list, JSON or another audit
// field.
func TestSortableColumns(t *testing.T) {
	schema, err := loader.LoadService(sortRulesService(t))
	if err != nil {
		t.Fatalf("load sort-rules: %v", err)
	}
	output, err := Generate(schema, Options{
		SchemaName:  "sort-rules",
		ModulePath:  "example.com/schemas/orm/sort-rules",
		TypesModule: "example.com/schemas/types/go/sort-rules",
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(output.Repositories) != 1 {
		t.Fatalf("got %d repositories, want Entry's", len(output.Repositories))
	}
	entry := output.Repositories[0]
	want := []string{"entryId", "title", "rank", "score", "due", "ownerId", "createdAt", "updatedAt"}
	if got := sortFieldNames(entry); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Entry.SortFields = %q, want %q", got, want)
	}
	if got := entry.SortFieldList(); got != "entryId, title, rank, score, due, ownerId, createdAt and updatedAt" {
		t.Errorf("Entry.SortFieldList() = %q", got)
	}

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	query, err := os.ReadFile(filepath.Join(outDir, "query.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"type EntryPageKey struct {\n\tEntryId types.IdentityUUID `json:\"entryId\"`\n}",
		"return k.EntryId.ToUUID(), nil",
	} {
		if !strings.Contains(string(query), want) {
			t.Errorf("query.go is missing %q", want)
		}
	}
	repository, err := os.ReadFile(filepath.Join(outDir, "repository_entry.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"EntrySortByRank:      {`entry.rank`, `page_key.rank`},",
		"byKey := field == EntrySortByEntryId",
		"FROM entry AS page_key WHERE page_key.entry_id = %s",
	} {
		if !strings.Contains(string(repository), want) {
			t.Errorf("repository_entry.go is missing %q", want)
		}
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

// TestGenerateGeoLocationColumns checks the code a Geo.Location column and a
// list of them generate without a database (TestGeoLocationColumnsOnPostgres
// runs it): a location filters on IS NULL only, since Postgres has no = for a
// point, and a POINT[] list converts through utils.go's helpers.
func TestGenerateGeoLocationColumns(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID"}
	schema.Scalars["Geo.Location"] = &ir.ScalarDef{Name: "Geo.Location", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "POINT", "json_schema": "object"}}
	schema.Types["Stop"] = &ir.TypeDef{
		Name: "Stop",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true, AutoGenerated: true},
			{Name: "dropoff", TypeRef: ir.TypeRef{Name: "Geo.Location"}},
			{Name: "route", TypeRef: ir.TypeRef{Name: "Geo.Location", IsArray: true}},
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
	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}

	query := read("query.go")
	if !regexp.MustCompile(`Dropoff\s+\*GeoPointFilter`).MatchString(query) {
		t.Error("StopFilter.Dropoff should be a *GeoPointFilter")
	}
	for _, want := range []string{"`stop.dropoff IS NULL`", "`stop.dropoff IS NOT NULL`"} {
		if !strings.Contains(query, want) {
			t.Errorf("buildWhereClause should emit %s for a location", want)
		}
	}
	for _, refused := range []string{"stop.dropoff = $", "stop.dropoff IN (", "stop.dropoff ILIKE"} {
		if strings.Contains(query, refused) {
			t.Errorf("buildWhereClause emits %q, which Postgres cannot evaluate for a point", refused)
		}
	}

	utils := read("utils.go")
	for _, want := range []string{
		"func geoLocationPoints(locations []types.GeoLocation) []pgtype.Point {",
		"func copyGeoLocationPoints(field string, points []pgtype.Point, dst *[]types.GeoLocation) error {",
		"func unmarshalGeoLocationPoints(raw []byte, dst *[]types.GeoLocation, field string) error {",
	} {
		if !strings.Contains(utils, want) {
			t.Errorf("utils.go is missing %q", want)
		}
	}
	repository := read("repository_stop.go")
	for _, want := range []string{
		"var tempRoute []pgtype.Point",
		`copyGeoLocationPoints("route", tempRoute, &result.Route)`,
		"values = append(values, geoLocationPoints(input.Route))",
		"values = append(values, geoLocationPoints(*update.Route))",
	} {
		if !strings.Contains(repository, want) {
			t.Errorf("repository_stop.go is missing %q", want)
		}
	}
	if strings.Contains(repository, "[]*types.GeoLocation") {
		t.Error("a POINT[] list should scan through []pgtype.Point, not pointer elements")
	}
}

// TestGenerateDurationColumns checks the code a Temporal.Duration column and
// a list of them generate without a database (TestDurationColumnsOnPostgres
// runs it): each crosses as a time.Duration, never through the scalar's
// Valuer or Scanner, and a duration filters on comparisons of
// time.Durations, not on a string's ILIKE.
func TestGenerateDurationColumns(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID"}
	schema.Scalars["Temporal.Duration"] = &ir.ScalarDef{Name: "Temporal.Duration", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "INTERVAL", "go": "TemporalDuration"}}
	schema.Types["Leg"] = &ir.TypeDef{
		Name:      "Leg",
		Role:      ir.RoleDBTable,
		Versioned: true,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true, AutoGenerated: true},
			{Name: "planned", TypeRef: ir.TypeRef{Name: "Temporal.Duration"}, Required: true},
			{Name: "delay", TypeRef: ir.TypeRef{Name: "Temporal.Duration"}},
			{Name: "splits", TypeRef: ir.TypeRef{Name: "Temporal.Duration", IsArray: true}},
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
	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}

	query := read("query.go")
	for _, field := range []string{"Planned", "Delay"} {
		if !regexp.MustCompile(field + `\s+\*DurationFilter`).MatchString(query) {
			t.Errorf("LegFilter.%s should be a *DurationFilter", field)
		}
	}
	if strings.Contains(query, "leg.planned ILIKE") {
		t.Error("buildWhereClause emits ILIKE, which Postgres cannot evaluate for an interval")
	}

	utils := read("utils.go")
	for _, want := range []string{
		"func intervalList[T ~int64](durations []T) []time.Duration {",
		"func copyIntervalElements[T ~int64](field string, values []*time.Duration, dst *[]T) error {",
		"func unmarshalHistoryInterval(raw []byte) (time.Duration, error) {",
		"func unmarshalHistoryIntervals[T ~int64](raw []byte, dst *[]T, field string) error {",
	} {
		if !strings.Contains(utils, want) {
			t.Errorf("utils.go is missing %q", want)
		}
	}
	repository := read("repository_leg.go")
	for _, want := range []string{
		"values = append(values, time.Duration(input.Planned))",
		"values = append(values, time.Duration(*input.Delay))",
		"values = append(values, time.Duration(*update.Planned))",
		"values = append(values, intervalList(input.Splits))",
		"(*time.Duration)(&result.Planned),",
		"var tempDelay *time.Duration",
		"result.Delay = (*types.TemporalDuration)(tempDelay)",
		"var tempSplits []*time.Duration",
		`copyIntervalElements("splits", tempSplits, &result.Splits)`,
		"duration, err := unmarshalHistoryInterval(rawValue)",
		`unmarshalHistoryIntervals(rawValue, &result.Splits, "splits")`,
	} {
		if !strings.Contains(repository, want) {
			t.Errorf("repository_leg.go is missing %q", want)
		}
	}
	for _, refused := range []string{"values = append(values, input.Planned)", "&result.Planned,", "[]*types.TemporalDuration"} {
		if strings.Contains(repository, refused) {
			t.Errorf("repository_leg.go has %q, which crosses the scalar's Valuer or Scanner", refused)
		}
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

// TestGenerateRefusesAGraphColumnNoValueClassReads adds to a graph member a
// Geo.Location, a JSON object the sql generator stores as POINT, shaped as
// the catalog's row hydrates it. No value class reads POINT, so the graph
// has no descriptor, and Generate fails with the descriptor's error, naming
// the field, the JSON object it holds and the SQL type, rather than writing
// a shell without one.
func TestGenerateRefusesAGraphColumnNoValueClassReads(t *testing.T) {
	const svc = "fixture-version-graph-db"
	schema, err := loader.LoadService(filepath.Join(fixturesDir, svc))
	if err != nil {
		t.Fatalf("load %s: %v", svc, err)
	}
	schema.Scalars["Geo.Location"] = &ir.ScalarDef{
		Name: "Geo.Location", LanguagePrimitive: ir.LanguageString, Primitive: "String", HasCustomParse: true,
		TypeMappings: map[string]string{"json_schema": ir.JSONSchemaObjectType, "sql": "POINT"},
	}
	schema.Types["Utensil"].Fields = append(schema.Types["Utensil"].Fields, &ir.FieldDef{
		Name: "shelf", TypeRef: ir.TypeRef{Name: "Geo.Location"}, Required: true,
	})

	_, err = Generate(schema, Options{
		SchemaName:  svc,
		ModulePath:  "example.com/schemas/orm/" + svc,
		TypesModule: "example.com/schemas/types/go/" + svc,
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err == nil || !strings.HasPrefix(err.Error(), "graphdesc: version graph Recipe: Utensil.shelf: ") ||
		!strings.Contains(err.Error(), "Geo.Location holds a JSON object but is stored as POINT,") {
		t.Fatalf("Generate = %v, want the descriptor's refusal of Utensil.shelf, a JSON object stored as POINT", err)
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
	// CreateOne and UpsertOne bind the default as a value, and CreateMany
	// and UpsertMany (D89) as a row's cell.
	// TestCreateBindsTheEnumDefaultWhenUnset in compile_test.go runs
	// CreateOne and CreateMany.
	for want, count := range map[string]int{
		`values = append(values, types.OrderStatus("pending"))`:  2,
		`values = append(values, types.OrderPriority("normal"))`: 2,
		`cell.value = types.OrderStatus("pending")`:              2,
		`cell.value = types.OrderPriority("normal")`:             2,
	} {
		if n := strings.Count(string(source), want); n != count {
			t.Errorf("CreateOne, UpsertOne and CreateMany must each insert the default; found %d of %q, want %d", n, want, count)
		}
	}
}
