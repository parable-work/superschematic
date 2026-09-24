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
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

const nestedArraysFixture = "fixture-nested-arrays-db"

// loadNestedArraysFixture loads fixture-nested-arrays-db.
func loadNestedArraysFixture(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, nestedArraysFixture))
	if err != nil {
		t.Fatalf("load %s: %v", nestedArraysFixture, err)
	}
	return schema
}

// addBoardMoves adds a closed union and a Board column that is a list of
// lists of it to a loaded fixture-nested-arrays-db: moves, one inner list of
// BoardMove (PlaceMove or ClearMove, told apart by kind) per turn. Only the
// JSON and YAML schema forms declare unions, so the column is added to the
// IR here, as extendFixtureForCompileCoverage does for fixture-db, and the
// fixture's goldens do not change.
func addBoardMoves(schema *ir.Schema) {
	placeKind, clearKind := "place", "clear"
	schema.Types["PlaceMove"] = &ir.TypeDef{
		Name: "PlaceMove",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &placeKind},
			{Name: "x", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
			{Name: "y", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
			{Name: "state", TypeRef: ir.TypeRef{Name: "CellState"}, Required: true},
		},
	}
	schema.Types["ClearMove"] = &ir.TypeDef{
		Name: "ClearMove",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &clearKind},
			{Name: "x", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
			{Name: "y", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
		},
	}
	schema.Unions["BoardMove"] = &ir.UnionDef{Name: "BoardMove", Types: []string{"PlaceMove", "ClearMove"}}
	board := schema.Types["Board"]
	board.Fields = append(board.Fields, &ir.FieldDef{
		Name:     "moves",
		Comment:  "Moves, one inner list per turn.",
		TypeRef:  ir.TypeRef{Name: "BoardMove", IsArray: true, IsArrayOfArrays: true},
		Required: true,
	})
}

// nestedArraysCreateSQL writes the DDL sqlgen generates for schema into dir
// and returns its create.sql path.
func nestedArraysCreateSQL(t *testing.T, schema *ir.Schema, dir string) string {
	t.Helper()
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: nestedArraysFixture})
	if err != nil {
		t.Fatalf("generate ddl: %v", err)
	}
	if err := sqlgen.WriteDDL(ddl, dir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}
	return filepath.Join(dir, "create.sql")
}

// TestArraysOfArraysOnPostgres applies the DDL sqlgen generates for
// fixture-nested-arrays-db to a real Postgres and round-trips lists of lists
// through the JSON codec ormgen generates: ragged lists, empty outer and
// inner lists, nil inner lists, a nil optional list and a nil required one.
//
// The check lives in testdata/pgarrays, a module of its own, so the Postgres
// driver stays out of this module. It needs only the generated utils.go;
// TestGeneratedArraysOfArraysORM runs the same shapes through the generated
// repositories. Set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to a URL whose
// role may create schemas (a throwaway postgres:15-alpine container's
// postgres user is enough); the test creates and drops its own schema.
func TestArraysOfArraysOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the array-of-arrays columns against Postgres")
	}
	createSQL := nestedArraysCreateSQL(t, loadNestedArraysFixture(t), t.TempDir())

	ormDir := t.TempDir()
	if err := WriteORM(generateFixture(t, nestedArraysFixture), ormDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	utils, err := os.ReadFile(filepath.Join(ormDir, "utils.go"))
	if err != nil {
		t.Fatal(err)
	}

	module := t.TempDir()
	if err := os.CopyFS(module, os.DirFS(filepath.Join("testdata", "pgarrays"))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "utils.go"), utils, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-v", "./...")
	cmd.Dir = module
	cmd.Env = append(os.Environ(),
		"GOFLAGS=-mod=readonly",
		"PGARRAYS_DATABASE_URL="+dsn,
		"PGARRAYS_CREATE_SQL="+createSQL,
	)
	out, err := cmd.CombinedOutput()
	t.Logf("pgarrays:\n%s", out)
	if err != nil {
		t.Fatalf("pgarrays failed: %v", err)
	}
	if !strings.Contains(string(out), "--- PASS: TestArraysOfArraysOnPostgres") {
		t.Fatal("pgarrays did not run its test")
	}
}

// TestGeneratedArraysOfArraysORM generates the Go types module and the ORM
// module for fixture-nested-arrays-db with the moves column of
// addBoardMoves, then builds, vets and tests the ORM. Its generated test
// writes lists of lists, a list of lists of a union among them, through
// CreateOne, CreateMany and UpdateOne and reads them back through GetOne and
// FindMany against the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL,
// and skips without it; the union decoder is also run without it.
func TestGeneratedArraysOfArraysORM(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)

	schema := loadNestedArraysFixture(t)
	addBoardMoves(schema)
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	typesModule := "example.com/schemas/types/go/" + nestedArraysFixture
	tempRoot := t.TempDir()
	typesDir := filepath.Join(tempRoot, "types", "go", nestedArraysFixture)
	ormDir := filepath.Join(tempRoot, "orm", nestedArraysFixture)

	typesOutput, err := typegen.Generate(schema, typegen.Options{
		SchemaName: nestedArraysFixture,
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
		SchemaName:  nestedArraysFixture,
		ModulePath:  "example.com/schemas/orm/" + nestedArraysFixture,
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

	runArraysOfArraysORMModule(t, ormDir, nestedArraysCreateSQL(t, schema, t.TempDir()))
}

// runArraysOfArraysORMModule adds the round-trip test and its DDL
// (testdata/create.sql) to a generated fixture-nested-arrays-db ORM module
// and builds, vets and tests it.
func runArraysOfArraysORMModule(t *testing.T, ormDir, createSQL string) {
	t.Helper()
	ddl, err := os.ReadFile(createSQL)
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(ormDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "testdata", "create.sql"), ddl, 0o644); err != nil {
		t.Fatalf("write create.sql: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "arrays_of_arrays_test.go"), []byte(arraysOfArraysORMTest), 0o644); err != nil {
		t.Fatalf("write round-trip test: %v", err)
	}

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = ormDir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
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
		}
	}
}

const arraysOfArraysORMTest = `package orm

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-nested-arrays-db"
)

// TestDecodeBoardMovesJSONUnion runs the union decoder of the moves column
// without a database: each innermost element is decoded through the
// union's wrapper, and a failure names both indexes.
func TestDecodeBoardMovesJSONUnion(t *testing.T) {
	moves, err := decodeBoardMovesJSONUnion([]byte(` + "`" + `[[{"kind":"place","x":1,"y":2,"state":"filled"},{"kind":"clear","x":1,"y":2}],[]]` + "`" + `))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := [][]types.BoardMove{
		{types.PlaceMove{Kind: "place", X: 1, Y: 2, State: types.CellState_Filled}, types.ClearMove{Kind: "clear", X: 1, Y: 2}},
		{},
	}
	if !reflect.DeepEqual(moves, want) {
		t.Fatalf("moves = %#v, want %#v", moves, want)
	}
	if moves, err := decodeBoardMovesJSONUnion([]byte("null")); err != nil || moves != nil {
		t.Fatalf("null = %#v, %v; want nil, nil", moves, err)
	}
	_, err = decodeBoardMovesJSONUnion([]byte(` + "`" + `[[{"kind":"place","x":1,"y":2,"state":"filled"},{"kind":"flip"}]]` + "`" + `))
	if err == nil || !strings.Contains(err.Error(), "decode item [0][1]") {
		t.Fatalf("unknown member err = %v, want one naming item [0][1]", err)
	}
}

func TestBoardArraysOfArraysOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM against Postgres")
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
	schema := fmt.Sprintf("arrays_of_arrays_orm_%d", time.Now().UnixNano())
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

	place := types.PlaceMove{Kind: "place", X: 1, Y: 2, State: types.CellState_Filled}
	clear := types.ClearMove{Kind: "clear", X: 1, Y: 2}
	ragged := &types.Board{
		Labels: [][]string{{"a", "b", "c"}, {"d"}, {}},
		States: [][]types.CellState{{types.CellState_Filled}, {}, {types.CellState_Empty, types.CellState_Filled}},
		Walls:  [][]types.BoardPoint{{{X: 1, Y: 2}, {X: 3, Y: 4}}, nil},
		Scores: [][]float64{{1.5}, {2, 3, 4}, {}},
		Moves:  [][]types.BoardMove{{place, clear}, nil, {place}},
	}
	created, err := db.Board.CreateOne(ctx, ragged)
	if err != nil {
		t.Fatalf("CreateOne: %v", err)
	}
	if created.Id == nil {
		t.Fatal("CreateOne returned no id")
	}
	id := *created.Id
	// A nil inner list is stored, and read back, as an empty list.
	wantWalls := [][]types.BoardPoint{{{X: 1, Y: 2}, {X: 3, Y: 4}}, {}}
	wantMoves := [][]types.BoardMove{{place, clear}, {}, {place}}
	for label, got := range map[string]*types.Board{"CreateOne": created, "GetOne": mustGetBoard(t, db, id, nil)} {
		if !reflect.DeepEqual(got.Labels, ragged.Labels) || !reflect.DeepEqual(got.States, ragged.States) ||
			!reflect.DeepEqual(got.Walls, wantWalls) || !reflect.DeepEqual(got.Scores, ragged.Scores) ||
			!reflect.DeepEqual(got.Moves, wantMoves) {
			t.Fatalf("%s = %+v, want the ragged lists with walls %+v and moves %+v", label, got, wantWalls, wantMoves)
		}
	}
	for column, want := range map[string]string{
		"walls": ` + "`" + `[[{"x": 1, "y": 2}, {"x": 3, "y": 4}], []]` + "`" + `,
		"moves": ` + "`" + `[[{"x": 1, "y": 2, "kind": "place", "state": "filled"}, {"x": 1, "y": 2, "kind": "clear"}], [], [{"x": 1, "y": 2, "kind": "place", "state": "filled"}]]` + "`" + `,
	} {
		var text string
		if err := pool.QueryRow(ctx, "SELECT "+column+"::text FROM board WHERE id = $1", id.ToUUID()).Scan(&text); err != nil {
			t.Fatalf("read %s: %v", column, err)
		}
		if text != want {
			t.Fatalf("%s column = %s, want %s", column, text, want)
		}
	}

	selected := mustGetBoard(t, db, id, &BoardGetOptions{Fields: BoardFields{Labels: true, Scores: true, Moves: true}})
	if !reflect.DeepEqual(selected.Labels, ragged.Labels) || !reflect.DeepEqual(selected.Scores, ragged.Scores) ||
		!reflect.DeepEqual(selected.Moves, wantMoves) || selected.States != nil {
		t.Fatalf("selected GetOne = %+v, want only labels, scores and moves", selected)
	}

	empty := &types.Board{Labels: [][]string{}, States: [][]types.CellState{{}}, Walls: [][]types.BoardPoint{}, Moves: [][]types.BoardMove{{}}}
	createdMany, err := db.Board.CreateMany(ctx, []*types.Board{empty})
	if err != nil {
		t.Fatalf("CreateMany: %v", err)
	}
	emptyID := *createdMany[0].Id
	gotEmpty := mustGetBoard(t, db, emptyID, nil)
	if gotEmpty.Labels == nil || len(gotEmpty.Labels) != 0 || !reflect.DeepEqual(gotEmpty.States, empty.States) ||
		gotEmpty.Walls == nil || len(gotEmpty.Walls) != 0 || gotEmpty.Scores != nil || !reflect.DeepEqual(gotEmpty.Moves, empty.Moves) {
		t.Fatalf("empty lists = %+v, want empty labels and walls, one empty states and moves list, no scores", gotEmpty)
	}

	all, total, err := db.Board.FindMany(ctx, nil, &BoardFindOptions{Fields: BoardFields{}.All()})
	if err != nil {
		t.Fatalf("FindMany: %v", err)
	}
	if total != 2 || len(all) != 2 {
		t.Fatalf("FindMany = %d rows (total %d), want 2", len(all), total)
	}
	for _, got := range all {
		want := empty
		if got.Id != nil && got.Id.ToUUID() == id.ToUUID() {
			want = &types.Board{Labels: ragged.Labels, States: ragged.States, Walls: wantWalls, Scores: ragged.Scores, Moves: wantMoves}
		}
		if !reflect.DeepEqual(got.Labels, want.Labels) || !reflect.DeepEqual(got.States, want.States) ||
			!reflect.DeepEqual(got.Walls, want.Walls) || !reflect.DeepEqual(got.Scores, want.Scores) ||
			!reflect.DeepEqual(got.Moves, want.Moves) {
			t.Fatalf("FindMany row = %+v, want %+v", got, want)
		}
	}

	newLabels := [][]string{{}, {"x", "y"}, {"z"}}
	newMoves := [][]types.BoardMove{{clear}, {}}
	updated, err := db.Board.UpdateOne(ctx, id, &BoardUpdate{Labels: &newLabels, ScoresSetNull: true, Moves: &newMoves})
	if err != nil {
		t.Fatalf("UpdateOne: %v", err)
	}
	for label, got := range map[string]*types.Board{"UpdateOne": updated, "GetOne after update": mustGetBoard(t, db, id, nil)} {
		if !reflect.DeepEqual(got.Labels, newLabels) || got.Scores != nil || !reflect.DeepEqual(got.Walls, wantWalls) ||
			!reflect.DeepEqual(got.Moves, newMoves) {
			t.Fatalf("%s = %+v, want new labels and moves, no scores, walls unchanged", label, got)
		}
	}
}

func mustGetBoard(t *testing.T, db *Database, id types.IdentityUUID, opts *BoardGetOptions) *types.Board {
	t.Helper()
	board, err := db.Board.GetOne(context.Background(), id, opts)
	if err != nil {
		t.Fatalf("GetOne: %v", err)
	}
	return board
}
`
