package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// softDeletableRoot gives the Recipe graph's root of schema, a load of
// fixture-version-graph-db, a deletedAt and a deletedBy, as a project's
// root that is soft-deleted has (D139): the fixture's own root has none.
func softDeletableRoot(t *testing.T, schema *ir.Schema) {
	t.Helper()
	root := schema.Types["Recipe"]
	if root == nil || root.VersionGraph == nil {
		t.Fatalf("the fixture has no Recipe graph root: %+v", root)
	}
	root.Fields = append(root.Fields,
		&ir.FieldDef{Name: "deletedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}},
		&ir.FieldDef{Name: "deletedBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}},
	)
}

// releasedLiveRoot is the live condition of part_released when Recipe is
// soft-deletable: the row's root is a live row of recipe.
const releasedLiveRoot = "EXISTS (SELECT 1 FROM recipe AS live_root WHERE live_root.id = part_released.recipe_id AND live_root.deleted_at IS NULL)"

// TestReleasedRowsOfASoftDeletedRootAreLeftOut: when the graph's root is
// soft-deletable, the repository of a member's released rows reads them
// held to the root's live row (D139), by the statements' live condition,
// as a soft-deletable table's reads are held to its own; the soft delete
// clears no row, so a restore sees them again. A root without deletedAt,
// the fixture's own, leaves the released rows' reads as they were.
func TestReleasedRowsOfASoftDeletedRootAreLeftOut(t *testing.T) {
	plain := generateFixture(t, versionGraphFixture)
	schema, err := loader.LoadService(filepath.Join(fixturesDir, versionGraphFixture))
	if err != nil {
		t.Fatalf("load %s: %v", versionGraphFixture, err)
	}
	softDeletableRoot(t, schema)
	output, err := Generate(schema, Options{
		SchemaName:  versionGraphFixture,
		ModulePath:  "example.com/schemas/orm/" + versionGraphFixture,
		TypesModule: "example.com/schemas/types/go/" + versionGraphFixture,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	released := func(output *ORMOutput) Repository {
		t.Helper()
		for _, repo := range output.Repositories {
			if repo.TypeName == "PartReleased" {
				return repo
			}
		}
		t.Fatal("no repository of PartReleased")
		return Repository{}
	}
	if repo := released(plain); repo.LiveRoot || repo.ReadsLive() || repo.SQL.Live != "" {
		t.Errorf("PartReleased of a root without deletedAt: LiveRoot %v, Live %q; want none", repo.LiveRoot, repo.SQL.Live)
	}
	repo := released(output)
	if !repo.LiveRoot || !repo.ReadsLive() || repo.HasSoftDelete {
		t.Errorf("PartReleased of a soft-deletable root: LiveRoot %v, ReadsLive %v, HasSoftDelete %v; want the first two alone", repo.LiveRoot, repo.ReadsLive(), repo.HasSoftDelete)
	}
	if repo.SQL.Live != " AND "+releasedLiveRoot || repo.SQL.LiveWhere != " WHERE "+releasedLiveRoot || repo.SQL.LiveCondition != releasedLiveRoot {
		t.Errorf("PartReleased's live statements = %q, %q, %q; want the root's live row", repo.SQL.Live, repo.SQL.LiveWhere, repo.SQL.LiveCondition)
	}
	if repo.SQL.SoftDelete != "" || repo.SQL.SoftDeleteMany != "" {
		t.Errorf("PartReleased has a soft delete statement: %q", repo.SQL.SoftDelete)
	}
	for _, other := range output.Repositories {
		if other.TypeName != "PartReleased" && other.LiveRoot {
			t.Errorf("%s, no table of released rows, has a live root", other.TypeName)
		}
	}
	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	file := generatedFile(t, outDir, "repository_part_released.go")
	// GetOne and GetManyByIDs append it; FindOne, FindMany and findRelated
	// write it as the whole WHERE or after the filter's; FindPage adds it
	// as a condition.
	if got := strings.Count(file, releasedLiveRoot); got != 9 {
		t.Errorf("repository_part_released.go holds the live condition %d times, want 9, in each of its reads", got)
	}
	if doc := strings.Join(strings.Fields(strings.ReplaceAll(file, "\n// ", " ")), " "); !strings.Contains(doc, "Its reads leave out the rows of a soft-deleted root, unless IncludeDeleted asks for them, and a restore of the root sees them again") {
		t.Error("PartReleasedRepository's doc comment does not say it leaves out a soft-deleted root's rows")
	}
}

// TestReleasedRowsOfASoftDeletedRootOnPostgres builds the ORM of
// fixture-version-graph-db with a soft-deletable root, applies its DDL to
// the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, and runs
// releasedSoftDeleteTest in it: the released rows of a root its repository
// soft-deletes are out of every read, unless IncludeDeleted asks for them,
// another root's stay, and once the root is restored the same rows read
// again (D139).
func TestReleasedRowsOfASoftDeletedRootOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the released rows of a soft-deleted root against Postgres")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, versionGraphFixture))
	if err != nil {
		t.Fatalf("load %s: %v", versionGraphFixture, err)
	}
	softDeletableRoot(t, schema)
	ormDir := generateORMModuleOf(t, schema, versionGraphFixture)
	if err := os.WriteFile(filepath.Join(ormDir, "released_soft_delete_test.go"), []byte(releasedSoftDeleteTest), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := runORMModule(t, ormDir); !strings.Contains(out, "--- PASS: TestReleasedRowsOfASoftDeletedRoot ") {
		t.Fatalf("the generated ORM module did not pass TestReleasedRowsOfASoftDeletedRoot:\n%s", out)
	}
}

// releasedSoftDeleteTest runs in the generated ORM module, in a schema of
// its own: two recipes, each with released parts written as a release
// writes them, then one soft-deleted through its repository, read, and
// restored.
const releasedSoftDeleteTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-version-graph-db"
)

func TestReleasedRowsOfASoftDeletedRoot(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("released_soft_go_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ddl, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(ddl)); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	cook, err := ParseUserID("5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90")
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithUserID(ctx, cook)

	// Two recipes, each with a tagged commit and the released parts a
	// release of it writes.
	release := func(title string, headings ...string) *types.Recipe {
		recipe, err := db.Recipe.CreateOne(ctx, &types.Recipe{Title: title})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		root := recipe.Id.ToUUID().String()
		var ref, commit string
		if err := pool.QueryRow(ctx, "INSERT INTO recipe_ref (root_id, name, created_by, updated_by) VALUES ($1, 'main', $2, $2) RETURNING id::text", root, cook.ToUUID().String()).Scan(&ref); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, "INSERT INTO recipe_commit (root_id, ref_id, schema_epoch, content_hash, \"sequence\", created_by) VALUES ($1, $2, 1, 'hash', 1, $3) RETURNING id::text", root, ref, cook.ToUUID().String()).Scan(&commit); err != nil {
			t.Fatal(err)
		}
		for i, heading := range headings {
			if _, err := pool.Exec(ctx, "INSERT INTO part_released (recipe_id, entity_key, rank, heading, commit_id, \"sequence\") VALUES ($1, gen_random_uuid(), $2, $3, $4, 1)", root, string(rune('F'+i)), heading, commit); err != nil {
				t.Fatal(err)
			}
		}
		return recipe
	}
	bread := release("Bread", "Mix", "Knead", "Bake")
	release("Soup", "Boil")
	parts := db.GetPartReleasedRepository()
	headings := func(rows []*types.PartReleased) string {
		var out []string
		for _, row := range rows {
			out = append(out, row.Heading)
		}
		sort.Strings(out)
		return fmt.Sprint(out)
	}
	ofBread := &PartReleasedFilter{RecipeID: &UUIDFilter{Eq: bread.Id}}
	fields := PartReleasedFields{Id: true, Heading: true, Recipe: true}
	read := &PartReleasedFindOptions{Fields: fields}
	breadRows, total, err := parts.FindMany(ctx, ofBread, read)
	if err != nil || total != 3 {
		t.Fatalf("FindMany of Bread's released parts = %d rows (%v), want 3", total, err)
	}
	breadPart := breadRows[0]

	// Bread is soft-deleted through its repository: its released rows are
	// out of every read, and Soup's stay.
	if err := db.Recipe.DeleteOne(ctx, *bread.Id); err != nil {
		t.Fatalf("soft-delete Bread: %v", err)
	}
	var kept int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM part_released WHERE recipe_id = $1", bread.Id.ToUUID().String()).Scan(&kept); err != nil || kept != 3 {
		t.Fatalf("part_released holds %d of Bread's rows (%v): the soft delete clears none", kept, err)
	}
	rows, total, err := parts.FindMany(ctx, nil, read)
	if err != nil || total != 1 || headings(rows) != "[Boil]" {
		t.Fatalf("FindMany after the delete = %s, total %d (%v); want Soup's alone", headings(rows), total, err)
	}
	if rows, total, err := parts.FindMany(ctx, ofBread, read); err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("FindMany of Bread's = %d rows (%v), want none", total, err)
	}
	if _, err := parts.GetOne(ctx, *breadPart.Id, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetOne of Bread's part = %v, want ErrNotFound", err)
	}
	if got, err := parts.GetManyByIDs(ctx, []types.IdentityUUID{*breadPart.Id}); err != nil || len(got) != 0 {
		t.Fatalf("GetManyByIDs of Bread's part = %d rows (%v), want none", len(got), err)
	}
	if row, err := parts.FindOne(ctx, ofBread, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindOne of Bread's = %+v (%v), want ErrNotFound", row, err)
	}
	page, err := parts.FindPage(ctx, nil, &PartReleasedPageOptions{Fields: fields})
	if err != nil || headings(page.Rows) != "[Boil]" {
		t.Fatalf("FindPage after the delete = %+v (%v), want Soup's alone", page, err)
	}
	// IncludeDeleted reads them, as it reads a soft-deleted row.
	if rows, total, err := parts.FindMany(ctx, ofBread, &PartReleasedFindOptions{Fields: fields, IncludeDeleted: true}); err != nil || total != 3 || headings(rows) != "[Bake Knead Mix]" {
		t.Fatalf("FindMany of Bread's, deleted included = %s (%v)", headings(rows), err)
	}
	if page, err := parts.FindPage(ctx, ofBread, &PartReleasedPageOptions{Fields: fields, IncludeDeleted: true}); err != nil || len(page.Rows) != 3 {
		t.Fatalf("FindPage of Bread's, deleted included = %+v (%v)", page, err)
	}
	// In a transaction too.
	if err := db.Transaction(ctx, func(tx TxInterface) error {
		_, total, err := tx.GetPartReleasedRepository().FindMany(ctx, ofBread, read)
		if err == nil && total != 0 {
			err = fmt.Errorf("a transaction reads %d of Bread's released parts", total)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// A restore of Bread sees the same rows again.
	if _, err := pool.Exec(ctx, "UPDATE recipe SET deleted_at = NULL, deleted_by = NULL WHERE id = $1", bread.Id.ToUUID().String()); err != nil {
		t.Fatal(err)
	}
	if rows, total, err := parts.FindMany(ctx, ofBread, read); err != nil || total != 3 || headings(rows) != "[Bake Knead Mix]" {
		t.Fatalf("FindMany of Bread's after the restore = %s (%v)", headings(rows), err)
	}
	if got, err := parts.GetOne(ctx, *breadPart.Id, nil); err != nil || *got.Id != *breadPart.Id {
		t.Fatalf("GetOne of Bread's part after the restore = %+v (%v)", got, err)
	}
	if got, err := parts.GetManyByIDs(ctx, []types.IdentityUUID{*breadPart.Id}); err != nil || len(got) != 1 {
		t.Fatalf("GetManyByIDs after the restore = %d rows (%v)", len(got), err)
	}
	if _, total, err := parts.FindMany(ctx, nil, read); err != nil || total != 4 {
		t.Fatalf("FindMany of every released part after the restore = %d (%v)", total, err)
	}
}
`
