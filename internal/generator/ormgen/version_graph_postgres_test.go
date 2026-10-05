package ormgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
)

const versionGraphFixture = "fixture-version-graph-db"

// TestGeneratedVersionGraphORM generates the DDL, the Go types module and
// the ORM module for fixture-version-graph-db, whose version graph the
// loader expands into ordinary types, then builds, vets and tests the ORM.
// Its generated test applies the DDL to the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (and skips without it) and shows
// the graph tables hold: one entityKey lives on two refs, a second row for
// the same (entityKey, ref) is refused, a ref that member rows reference
// cannot be deleted, and a patch row keeps the member row version it names
// out of the generated prune function.
func TestGeneratedVersionGraphORM(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateVersionGraphModule(t)
	if err := os.WriteFile(filepath.Join(ormDir, "version_graph_test.go"), []byte(versionGraphORMTest), 0o644); err != nil {
		t.Fatalf("write version graph test: %v", err)
	}
	runVersionGraphModule(t, ormDir)
}

// generateVersionGraphModule writes the Go types module, the ORM module and
// the DDL (under the ORM's testdata) of fixture-version-graph-db into a
// temporary tree, and returns the ORM module's directory.
func generateVersionGraphModule(t *testing.T) string {
	t.Helper()
	paths := testpaths.Local(t)

	schema, err := loader.LoadService(filepath.Join(fixturesDir, versionGraphFixture))
	if err != nil {
		t.Fatalf("load %s: %v", versionGraphFixture, err)
	}
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	typesModule := "example.com/schemas/types/go/" + versionGraphFixture
	tempRoot := t.TempDir()
	typesDir := filepath.Join(tempRoot, "types", "go", versionGraphFixture)
	ormDir := filepath.Join(tempRoot, "orm", versionGraphFixture)

	typesOutput, err := typegen.Generate(schema, typegen.Options{
		SchemaName: versionGraphFixture,
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
		SchemaName:  versionGraphFixture,
		ModulePath:  "example.com/schemas/orm/" + versionGraphFixture,
		TypesModule: typesModule,
		Clock:       fixedClock,
	})
	if err != nil {
		t.Fatalf("generate orm: %v", err)
	}
	if err := SetReplacePaths(ormOutput, paths, ormDir); err != nil {
		t.Fatalf("set orm replace paths: %v", err)
	}
	if err := WriteORM(ormOutput, ormDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: versionGraphFixture})
	if err != nil {
		t.Fatalf("generate ddl: %v", err)
	}
	if err := sqlgen.WriteDDL(ddl, filepath.Join(ormDir, "testdata")); err != nil {
		t.Fatalf("write ddl: %v", err)
	}
	createSQL := filepath.Join(ormDir, "testdata", "create.sql")
	pgtest.WriteCreateSQL(t, createSQL, createSQL)
	return ormDir
}

// runVersionGraphModule tidies, builds, vets and tests the generated ORM
// module, and returns the test output. It links the version-graph binding,
// so it needs the core's archive (make versiongraph).
func runVersionGraphModule(t *testing.T, ormDir string) string {
	t.Helper()
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = ormDir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	var testOutput string
	for _, args := range [][]string{{"build", "./..."}, {"vet", "./..."}, {"test", "-count=1", "-v", "./..."}} {
		// No cmd.Env: exec then sets PWD to cmd.Dir, which keeps the
		// module's relative replace paths valid under a symlinked temp dir.
		cmd := exec.Command("go", args...)
		cmd.Dir = ormDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated ORM module: %v\n%s", strings.Join(args, " "), err, out)
		}
		if args[0] == "test" {
			t.Logf("generated ORM tests:\n%s", out)
			testOutput = string(out)
		}
	}
	return testOutput
}

const versionGraphORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-version-graph-db"
)

func TestVersionGraphTablesOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the version graph tables against Postgres")
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
	schema := fmt.Sprintf("version_graph_orm_%d", time.Now().UnixNano())
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

	cook, err := types.ParseIdentityUUID("5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90")
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithUserID(ctx, cook)

	recipe, err := db.Recipe.CreateOne(ctx, &types.Recipe{Title: "Bread"})
	if err != nil {
		t.Fatalf("create recipe: %v", err)
	}
	main, err := db.RecipeRef.CreateOne(ctx, &types.RecipeRef{Root: *recipe, Name: "main"})
	if err != nil {
		t.Fatalf("create main ref: %v", err)
	}
	draft, err := db.RecipeRef.CreateOne(ctx, &types.RecipeRef{Root: *recipe, Name: "draft", ParentRef: main})
	if err != nil {
		t.Fatalf("create draft ref: %v", err)
	}
	step := func(ref *types.RecipeRef, entityKey *types.IdentityUUID, instruction string) (*types.Step, error) {
		return db.Step.CreateOne(ctx, &types.Step{
			Recipe:      *recipe,
			Ref:         *ref,
			EntityKey:   entityKey,
			Position:    1,
			Instruction: instruction,
			Timings:     types.GenericJSON(` + "`" + `{"knead": 10}` + "`" + `),
		})
	}

	// The database generates entityKey on insert; a change set overrides
	// the same entity with its own row.
	mix, err := step(main, nil, "Mix")
	if err != nil {
		t.Fatalf("create step on main: %v", err)
	}
	if mix.EntityKey == nil {
		t.Fatal("the step on main came back without a generated entityKey")
	}
	override, err := step(draft, mix.EntityKey, "Mix well")
	if err != nil {
		t.Fatalf("the same entityKey on a second ref was refused: %v", err)
	}
	if *override.EntityKey != *mix.EntityKey || *override.Id == *mix.Id {
		t.Fatalf("override = %+v, want the entityKey of %+v on a row of its own", override, mix)
	}

	// One row per (entityKey, ref).
	_, err = step(draft, mix.EntityKey, "Mix twice")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "uq_step_entity_ref" {
		t.Fatalf("a second row for one (entityKey, ref) = %v, want a unique violation of uq_step_entity_ref", err)
	}

	// A ref that member rows reference cannot be deleted.
	_, err = pool.Exec(ctx, "DELETE FROM recipe_ref WHERE id = $1", draft.Id.ToUUID())
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "fk_step_ref_id" {
		t.Fatalf("deleting a referenced ref = %v, want a foreign key violation of fk_step_ref_id", err)
	}

	// A patch pins the member row version it names against pruning. Both
	// steps reach version 3; the patch names version 1 of the first.
	rest, err := step(main, nil, "Rest")
	if err != nil {
		t.Fatalf("create second step: %v", err)
	}
	for _, id := range []*types.IdentityUUID{mix.Id, rest.Id} {
		for _, instruction := range []string{"again", "once more"} {
			if _, err := pool.Exec(ctx, "UPDATE step SET instruction = $1 WHERE id = $2", instruction, id.ToUUID()); err != nil {
				t.Fatalf("update step: %v", err)
			}
		}
	}
	commit, err := db.RecipeCommit.CreateOne(ctx, &types.RecipeCommit{Root: *recipe, Ref: *main, SchemaEpoch: 1, ContentHash: "sha256:bread"})
	if err != nil {
		t.Fatalf("create commit: %v", err)
	}
	if _, err := db.RecipePatch.CreateOne(ctx, &types.RecipePatch{
		Commit:        *commit,
		EntityKind:    types.RecipeEntityKind_Step,
		EntityKey:     *mix.EntityKey,
		EntityId:      *mix.Id,
		EntityVersion: 1,
		Operation:     types.RecipePatchOperation_Add,
	}); err != nil {
		t.Fatalf("create patch: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE step_history SET recorded_at = now() - interval '400 days'"); err != nil {
		t.Fatalf("age history: %v", err)
	}
	var pruned int64
	if err := pool.QueryRow(ctx, "SELECT step_prune_history(365)").Scan(&pruned); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 3 {
		t.Fatalf("pruned %d history rows, want 3: version 2 of the pinned step and versions 1 and 2 of the other", pruned)
	}
	for _, tc := range []struct {
		id   *types.IdentityUUID
		want []int64
	}{
		{mix.Id, []int64{1, 3}},
		{rest.Id, []int64{3}},
	} {
		rows, err := pool.Query(ctx, "SELECT _version FROM step_history WHERE id = $1 ORDER BY _version", tc.id.ToUUID())
		if err != nil {
			t.Fatalf("read history: %v", err)
		}
		var got []int64
		for rows.Next() {
			var version int64
			if err := rows.Scan(&version); err != nil {
				t.Fatalf("scan history: %v", err)
			}
			got = append(got, version)
		}
		rows.Close()
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("history versions of %v = %v, want %v", *tc.id, got, tc.want)
		}
	}
}
`
