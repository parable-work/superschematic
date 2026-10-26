package ormgen

import (
	"os"
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

const vectorsFixture = "fixture-vectors-db"

// TestVectorsAndDecimalsOnPostgres generates the Go types, the ORM and the
// DDL of fixture-vectors-db (D84) and runs vectorsORMTest in the generated
// module against the Postgres with pgvector that
// SUPERSCHEMATIC_VECTOR_TEST_DATABASE_URL names. create.sql enables the
// vector extension and declares NUMERIC(p,s), vector(n) and the HNSW and
// IVFFlat indexes; the module writes and reads signed amounts, decimals and
// vectors through the repositories, and finds neighbours with Nearest by
// each metric, through a filter and around a soft-deleted row.
func TestVectorsAndDecimalsOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	t.Setenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL", pgtest.VectorDatabaseURL(t))
	paths := testpaths.Local(t)

	schema, err := loader.LoadService(filepath.Join(fixturesDir, vectorsFixture))
	if err != nil {
		t.Fatalf("load %s: %v", vectorsFixture, err)
	}
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	typesModule := "example.com/schemas/types/go/" + vectorsFixture
	tempRoot := t.TempDir()
	typesDir := filepath.Join(tempRoot, "types", "go", vectorsFixture)
	ormDir := filepath.Join(tempRoot, "orm", vectorsFixture)

	typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: vectorsFixture, ModulePath: typesModule, Clock: fixedClock})
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
		SchemaName:  vectorsFixture,
		ModulePath:  "example.com/schemas/orm/" + vectorsFixture,
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

	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: vectorsFixture, Clock: fixedClock})
	if err != nil {
		t.Fatalf("generate ddl: %v", err)
	}
	ddlDir := t.TempDir()
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}
	createSQL, err := os.ReadFile(filepath.Join(ddlDir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	// The acceptance (G3): the extension, a 1024-dimension column and its
	// HNSW index, and the narrowed NUMERIC (G5).
	for _, want := range []string{
		"CREATE EXTENSION IF NOT EXISTS vector;",
		"embedding vector(1024) NOT NULL\n",
		"CREATE INDEX idx_text_embedding_embedding ON text_embedding USING HNSW (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);",
		"amount BIGINT NOT NULL,",
		"rate NUMERIC(10,4) NOT NULL,",
		"rates NUMERIC(6,3)[],",
	} {
		if !strings.Contains(string(createSQL), want) {
			t.Fatalf("create.sql has no %q:\n%s", want, createSQL)
		}
	}

	out := runGeneratedORMModule(t, ormDir, filepath.Join(ddlDir, "create.sql"), "vectors_test.go", vectorsORMTest)
	if !strings.Contains(out, "--- PASS: TestVectorsOnPostgres") || !strings.Contains(out, "--- PASS: TestDecimalsOnPostgres") {
		t.Fatal("the generated ORM module did not run TestVectorsOnPostgres and TestDecimalsOnPostgres")
	}
}

const vectorsORMTest = `package orm

import (
	"context"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-vectors-db"
)

func openVectors(t *testing.T) (context.Context, *pgxpool.Pool, *Database) {
	t.Helper()
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
	t.Cleanup(base.Close)
	schema := fmt.Sprintf("vectors_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		t.Fatalf("connect generated orm: %v", err)
	}
	return ctx, pool, db
}

// TestDecimalsOnPostgres writes Finance.SignedMoney and Generic.Decimal
// columns and reads them back: a NUMERIC(10,4) holds 1.5 as 1.5000, which
// reads back as the canonical 1.5; an unbounded NUMERIC keeps every digit;
// a decimal filter compares numbers.
func TestDecimalsOnPostgres(t *testing.T) {
	ctx, pool, db := openVectors(t)
	quantity := types.GenericDecimal("-12345678901234567890.123456789")
	debit, err := db.LedgerLine.CreateOne(ctx, &types.LedgerLine{
		Amount: -1250, Currency: "USD", Rate: "1.50", Quantity: &quantity, BookedAt: types.TemporalDateTime(time.Now()),
	})
	if err != nil {
		t.Fatalf("CreateOne: %v", err)
	}
	if debit.Amount != -1250 || debit.Rate != "1.5" || debit.Confidence != nil || debit.Quantity == nil || *debit.Quantity != quantity {
		t.Fatalf("CreateOne = amount %v rate %q confidence %v quantity %v", debit.Amount, debit.Rate, debit.Confidence, debit.Quantity)
	}
	var stored string
	if err := pool.QueryRow(ctx, "SELECT rate::text FROM ledger_line WHERE id = $1", debit.Id.ToUUID()).Scan(&stored); err != nil || stored != "1.5000" {
		t.Fatalf("rate is stored as %q, %v; want 1.5000", stored, err)
	}
	confidence := types.GenericDecimal("0.75")
	credit, err := db.LedgerLine.CreateOne(ctx, &types.LedgerLine{
		Amount: 9007199254740991, Currency: "EUR", Rate: "1234.5678", Confidence: &confidence, BookedAt: types.TemporalDateTime(time.Now()),
	})
	if err != nil {
		t.Fatalf("CreateOne: %v", err)
	}
	got, err := db.LedgerLine.GetOne(ctx, *credit.Id, nil)
	if err != nil {
		t.Fatalf("GetOne: %v", err)
	}
	if got.Amount != 9007199254740991 || got.Rate != "1234.5678" || got.Confidence == nil || *got.Confidence != "0.75" {
		t.Fatalf("GetOne = %+v", got)
	}
	gte := "1.50"
	lines, _, err := db.LedgerLine.FindMany(ctx, &LedgerLineFilter{Rate: &DecimalFilter{Gte: &gte, Lt: ptr("1000")}}, &LedgerLineFindOptions{Fields: (LedgerLineFields{}).All()})
	if err != nil {
		t.Fatalf("FindMany: %v", err)
	}
	if len(lines) != 1 || *lines[0].Id != *debit.Id {
		t.Fatalf("FindMany by rate kept %d lines, want the debit", len(lines))
	}
	updated, err := db.LedgerLine.UpdateOne(ctx, *debit.Id, &LedgerLineUpdate{Rate: decimalPtr("2.25")})
	if err != nil || updated.Rate != "2.25" {
		t.Fatalf("UpdateOne = %+v, %v", updated, err)
	}
	// A decimal the scalar refuses is refused before Postgres sees it.
	if _, err := db.LedgerLine.UpdateOne(ctx, *debit.Id, &LedgerLineUpdate{Rate: decimalPtr("1e3")}); err == nil {
		t.Fatal("UpdateOne wrote the rate 1e3")
	}
}

func ptr(s string) *string { return &s }

func decimalPtr(s string) *types.GenericDecimal {
	d := types.GenericDecimal(s)
	return &d
}

// TestVectorsOnPostgres writes vector(n) columns and reads them back, and
// finds neighbours with Nearest.
func TestVectorsOnPostgres(t *testing.T) {
	ctx, pool, db := openVectors(t)
	passages := []struct {
		title, topic string
		embedding    types.EmbeddingVector
	}{
		{"east", "maps", types.EmbeddingVector{1, 0, 0}},
		{"north-east", "maps", types.EmbeddingVector{0.7, 0.7, 0}},
		{"north", "maps", types.EmbeddingVector{0, 1, 0}},
		{"up", "sky", types.EmbeddingVector{0, 0, 1}},
		{"far east", "maps", types.EmbeddingVector{10, 0.5, 0}},
	}
	ids := map[string]types.IdentityUUID{}
	for _, p := range passages {
		topic := p.topic
		created, err := db.Passage.CreateOne(ctx, &types.Passage{Title: p.title, Topic: topic, Embedding: p.embedding})
		if err != nil {
			t.Fatalf("CreateOne %s: %v", p.title, err)
		}
		if !reflect.DeepEqual(created.Embedding, p.embedding) || created.TitleEmbedding != nil {
			t.Fatalf("CreateOne %s = embedding %v title embedding %v", p.title, created.Embedding, created.TitleEmbedding)
		}
		ids[p.title] = *created.Id
	}
	// The column holds pgvector's text form.
	var text string
	if err := pool.QueryRow(ctx, "SELECT embedding::text FROM passage WHERE id = $1", ids["north-east"].ToUUID()).Scan(&text); err != nil || text != "[0.7,0.7,0]" {
		t.Fatalf("embedding is stored as %q, %v", text, err)
	}
	title := types.EmbeddingVector{0.25, -0.5, 1e-07}
	updated, err := db.Passage.UpdateOne(ctx, ids["up"], &PassageUpdate{TitleEmbedding: &title, Rates: &[]types.GenericDecimal{"1.5", "-0.125"}})
	if err != nil {
		t.Fatalf("UpdateOne: %v", err)
	}
	if updated.TitleEmbedding == nil || !reflect.DeepEqual(*updated.TitleEmbedding, title) || !reflect.DeepEqual(updated.Rates, []types.GenericDecimal{"1.5", "-0.125"}) {
		t.Fatalf("UpdateOne = title embedding %v rates %v", updated.TitleEmbedding, updated.Rates)
	}

	names := func(neighbours []PassageNeighbour) []string {
		var out []string
		for _, n := range neighbours {
			out = append(out, n.Row.Title)
		}
		return out
	}
	query := types.EmbeddingVector{1, 0.1, 0}
	cosine, err := db.Passage.Nearest(ctx, PassageVectorEmbedding, query, &PassageNearestOptions{K: 3})
	if err != nil {
		t.Fatalf("Nearest: %v", err)
	}
	// By cosine, far east (10, 0.5, 0) points nearer the query's way than
	// east does, however far it is.
	if got := names(cosine); !reflect.DeepEqual(got, []string{"far east", "east", "north-east"}) {
		t.Fatalf("Nearest by cosine = %v", got)
	}
	for i := 1; i < len(cosine); i++ {
		if cosine[i].Distance < cosine[i-1].Distance {
			t.Fatalf("Nearest is not in order of distance: %+v", cosine)
		}
	}
	if d := cosine[0].Distance; d < 0 || d > 0.01 {
		t.Fatalf("the cosine distance of far east is %v", d)
	}
	l2, err := db.Passage.Nearest(ctx, PassageVectorEmbedding, query, &PassageNearestOptions{K: 2, Metric: VectorL2})
	if err != nil {
		t.Fatalf("Nearest by L2: %v", err)
	}
	if got := names(l2); !reflect.DeepEqual(got, []string{"east", "north-east"}) {
		t.Fatalf("Nearest by L2 = %v", got)
	}
	if want := math.Sqrt(0.01); math.Abs(l2[0].Distance-want) > 1e-6 {
		t.Fatalf("the L2 distance of east is %v, want %v", l2[0].Distance, want)
	}
	ip, err := db.Passage.Nearest(ctx, PassageVectorEmbedding, query, &PassageNearestOptions{K: 1, Metric: VectorInnerProduct})
	if err != nil {
		t.Fatalf("Nearest by inner product: %v", err)
	}
	if got := names(ip); !reflect.DeepEqual(got, []string{"far east"}) || ip[0].Distance > -10 {
		t.Fatalf("Nearest by inner product = %v, distance %v", got, ip[0].Distance)
	}
	// A filter keeps its rows, and a soft-deleted row is left out.
	sky := "sky"
	filtered, err := db.Passage.Nearest(ctx, PassageVectorEmbedding, query, &PassageNearestOptions{Filter: &PassageFilter{Topic: &StringFilter{Eq: &sky}}})
	if err != nil {
		t.Fatalf("Nearest with a filter: %v", err)
	}
	if got := names(filtered); !reflect.DeepEqual(got, []string{"up"}) {
		t.Fatalf("Nearest with a filter = %v", got)
	}
	// A soft delete stamps who made it.
	if err := db.Passage.DeleteOne(WithUserID(ctx, types.NewUUID()), ids["east"]); err != nil {
		t.Fatalf("DeleteOne: %v", err)
	}
	live, err := db.Passage.Nearest(ctx, PassageVectorEmbedding, query, &PassageNearestOptions{K: 1, Metric: VectorL2})
	if err != nil || !reflect.DeepEqual(names(live), []string{"north-east"}) {
		t.Fatalf("Nearest after a delete = %v, %v", names(live), err)
	}
	all, err := db.Passage.Nearest(ctx, PassageVectorEmbedding, query, &PassageNearestOptions{K: 1, Metric: VectorL2, IncludeDeleted: true})
	if err != nil || !reflect.DeepEqual(names(all), []string{"east"}) {
		t.Fatalf("Nearest with deleted rows = %v, %v", names(all), err)
	}
	// The optional vector field, which only one row holds.
	byTitle, err := db.Passage.Nearest(ctx, PassageVectorTitleEmbedding, title, nil)
	if err != nil || !reflect.DeepEqual(names(byTitle)[:1], []string{"up"}) {
		t.Fatalf("Nearest by title embedding = %v, %v", names(byTitle), err)
	}
	if _, err := db.Passage.Nearest(ctx, PassageVectorEmbedding, types.EmbeddingVector{1, 2}, nil); err == nil || !strings.Contains(err.Error(), "3 dimensions") {
		t.Fatalf("Nearest with a vector of 2 dimensions: %v", err)
	}

	// 1024 dimensions, through the HNSW index's metric.
	wide := make(types.EmbeddingVector, 1024)
	wide[7] = 1
	created, err := db.TextEmbedding.CreateOne(ctx, &types.TextEmbedding{Text: "seven", Embedding: wide})
	if err != nil {
		t.Fatalf("CreateOne text embedding: %v", err)
	}
	read, err := db.TextEmbedding.GetOne(ctx, *created.Id, nil)
	if err != nil || len(read.Embedding) != 1024 || read.Embedding[7] != 1 {
		t.Fatalf("GetOne text embedding: %v", err)
	}
	near, err := db.TextEmbedding.Nearest(ctx, TextEmbeddingVectorEmbedding, wide, &TextEmbeddingNearestOptions{K: 1})
	if err != nil || len(near) != 1 || near[0].Row.Text != "seven" {
		t.Fatalf("Nearest text embedding = %+v, %v", near, err)
	}
}
`
