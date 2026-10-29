package ormgen

// nearestParityScript is the TypeScript side of
// TestNearestParityOfGoAndTypeScriptOnPostgres: Nearest calls over
// fixture-vectors-db's repositories (D84), recorded as tableParityScript
// records its calls.
const nearestParityScript = `import { readFileSync, writeFileSync } from 'node:fs';
import pg from 'pg';
import type { OrmPool, OrmQueryable } from '@superschematic/http-runtime/postgres';
import { createOrm } from '@TYPES@/orm';

const [createSQL, out] = process.argv.slice(2);
const dsn = process.env.SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL!;
const schema = 'nearest_parity_ts_' + process.pid + '_' + Date.now();
const admin = new pg.Client({ connectionString: dsn });
await admin.connect();
await admin.query('CREATE SCHEMA ' + schema);
const base = new pg.Pool({ connectionString: dsn, options: '-c search_path=' + schema + ',public' });
try {
  await base.query(readFileSync(createSQL!, 'utf8'));
  const calls: { name: string; statements: string[] }[] = [];
  const record = (q: OrmQueryable): OrmQueryable => ({
    query(config) {
      calls[calls.length - 1]!.statements.push(config.text);
      return q.query(config as never) as never;
    },
  });
  const pool: OrmPool = {
    ...record(base as unknown as OrmQueryable),
    async connect() {
      const client = await base.connect();
      return { ...record(client as unknown as OrmQueryable), release: (err?: Error | boolean) => client.release(err) };
    },
  };
  const call = async <T>(name: string, fn: () => Promise<T>): Promise<T> => {
    calls.push({ name, statements: [] });
    return fn();
  };
  const orm = createOrm(pool, { user: 'TKbHsHDO6x9RCjbs5c2u4', fieldKeys: 'AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=' });
  const east = await call('createEast', () => orm.passage.createOne({ title: 'east', topic: 'maps', embedding: [1, 0, 0] }));
  const up = await call('createUp', () => orm.passage.createOne({ title: 'up', topic: 'sky', embedding: [0, 0, 1], titleEmbedding: [0, 1, 0] }));
  await call('nearestCosine', () => orm.passage.nearest('embedding', [1, 0.5, 0], { k: 2 }));
  await call('nearestL2', () => orm.passage.nearest('embedding', [1, 0.5, 0], { k: 1, metric: 'l2' }));
  await call('nearestIPFiltered', () => orm.passage.nearest('embedding', [1, 0.5, 0], { metric: 'ip', filter: { topic: { in: ['maps', 'sky'] }, title: { ilike: 'e' } } }));
  await call('deleteEast', () => orm.passage.deleteOne(east.id));
  await call('nearestWithDeleted', () => orm.passage.nearest('embedding', [1, 0.5, 0], { k: 3, includeDeleted: true }));
  await call('nearestTitle', () => orm.passage.nearest('titleEmbedding', [0, 1, 0]));
  // The index's settings, set for a transaction of its own, and the keys
  // it leaves out (D89).
  await call('nearestTuned', () =>
    orm.passage.nearest('embedding', [1, 0.5, 0], { k: 2, efSearch: 64, iterativeScan: 'relaxed_order', maxScanTuples: 5000, excludeIds: [up.id] })
  );
  await call('nearestIvfflat', () => orm.passage.nearest('embedding', [1, 0.5, 0], { metric: 'l2', probes: 2, maxProbes: 4, iterativeScan: 'strict_order' }));
  await call('nearestInTransaction', () => orm.transaction(tx => tx.passage.nearest('embedding', [1, 0.5, 0], { efSearch: 100 })));
  const wide = Array.from({ length: 1024 }, (_, i) => (i === 3 ? 0.5 : 0));
  await call('createText', () => orm.textEmbedding.createOne({ text: 'three', embedding: wide }));
  await call('nearestText', () => orm.textEmbedding.nearest('embedding', wide, { k: 1 }));
  // NoteChunk, which @table and @column name, and whose note is sealed
  // (D85): its names quoted in every statement, its rows opened.
  await call('createNorth', () => orm.chunk.createOne({ text: 'north', embedding: [0, 1, 0], note: 'private' }));
  const eastChunk = await call('createEastChunk', () => orm.chunk.createOne({ text: 'east', embedding: [1, 0, 0] }));
  await call('nearestChunk', () => orm.chunk.nearest('embedding', [1, 0.5, 0], { k: 2, metric: 'l2', filter: { text: { in: ['north', 'east'] } } }));
  await call('deleteEastChunk', () => orm.chunk.deleteOne(eastChunk.id));
  await call('nearestChunkWithDeleted', () => orm.chunk.nearest('embedding', [1, 0.5, 0], { includeDeleted: true }));
  writeFileSync(out!, JSON.stringify(calls, null, 2));
} finally {
  await base.end();
  await admin.query('DROP SCHEMA ' + schema + ' CASCADE');
  await admin.end();
}
`

// nearestParityORMTest is the Go side: the same calls through the Go ORM
// over a pool whose tracer records each statement.
const nearestParityORMTest = `package orm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-vectors-db"
)

type parityCall struct {
	Name       string   ` + "`json:\"name\"`" + `
	Statements []string ` + "`json:\"statements\"`" + `
}

type recorder struct {
	mu    sync.Mutex
	calls []parityCall
}

func (r *recorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) > 0 {
		r.calls[len(r.calls)-1].Statements = append(r.calls[len(r.calls)-1].Statements, data.SQL)
	}
	return ctx
}

func (r *recorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *recorder) call(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, parityCall{Name: name})
}

func TestNearestParity(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	out := os.Getenv("TABLE_PARITY_OUT")
	if dsn == "" || out == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL and TABLE_PARITY_OUT to run the parity script")
	}
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
	schema := fmt.Sprintf("nearest_parity_go_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	rec := &recorder{}
	cfg.ConnConfig.Tracer = rec
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	db, err := ConnectWithPool(pool, WithFieldKeys("AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	alice, err := types.ParseIdentityUUID("TKbHsHDO6x9RCjbs5c2u4")
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithUserID(ctx, alice)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", rec.calls[len(rec.calls)-1].Name, err)
		}
	}
	str := func(s string) *string { return &s }
	query := types.EmbeddingVector{1, 0.5, 0}

	rec.call("createEast")
	east, err := db.Passage.CreateOne(ctx, &types.Passage{Title: "east", Topic: "maps", Embedding: types.EmbeddingVector{1, 0, 0}})
	must(err)
	rec.call("createUp")
	title := types.EmbeddingVector{0, 1, 0}
	up, err := db.Passage.CreateOne(ctx, &types.Passage{Title: "up", Topic: "sky", Embedding: types.EmbeddingVector{0, 0, 1}, TitleEmbedding: &title})
	must(err)
	rec.call("nearestCosine")
	_, err = db.Passage.Nearest(ctx, PassageVectorEmbedding, query, &PassageNearestOptions{K: 2})
	must(err)
	rec.call("nearestL2")
	_, err = db.Passage.Nearest(ctx, PassageVectorEmbedding, query, &PassageNearestOptions{K: 1, Metric: VectorL2})
	must(err)
	rec.call("nearestIPFiltered")
	_, err = db.Passage.Nearest(ctx, PassageVectorEmbedding, query, &PassageNearestOptions{Metric: VectorInnerProduct,
		Filter: &PassageFilter{Topic: &StringFilter{In: []string{"maps", "sky"}}, Title: &StringFilter{ILike: str("e")}}})
	must(err)
	rec.call("deleteEast")
	must(db.Passage.DeleteOne(ctx, *east.Id))
	rec.call("nearestWithDeleted")
	_, err = db.Passage.Nearest(ctx, PassageVectorEmbedding, query, &PassageNearestOptions{K: 3, IncludeDeleted: true})
	must(err)
	rec.call("nearestTitle")
	_, err = db.Passage.Nearest(ctx, PassageVectorTitleEmbedding, title, nil)
	must(err)
	rec.call("nearestTuned")
	_, err = db.Passage.Nearest(ctx, PassageVectorEmbedding, query, &PassageNearestOptions{K: 2, EfSearch: 64, IterativeScan: IterativeScanRelaxed, MaxScanTuples: 5000, ExcludeIDs: []types.IdentityUUID{*up.Id}})
	must(err)
	rec.call("nearestIvfflat")
	_, err = db.Passage.Nearest(ctx, PassageVectorEmbedding, query, &PassageNearestOptions{Metric: VectorL2, Probes: 2, MaxProbes: 4, IterativeScan: IterativeScanStrict})
	must(err)
	rec.call("nearestInTransaction")
	must(db.Transaction(ctx, func(tx TxInterface) error {
		_, err := tx.GetPassageRepository().Nearest(ctx, PassageVectorEmbedding, query, &PassageNearestOptions{EfSearch: 100})
		return err
	}))
	wide := make(types.EmbeddingVector, 1024)
	wide[3] = 0.5
	rec.call("createText")
	_, err = db.TextEmbedding.CreateOne(ctx, &types.TextEmbedding{Text: "three", Embedding: wide})
	must(err)
	rec.call("nearestText")
	_, err = db.TextEmbedding.Nearest(ctx, TextEmbeddingVectorEmbedding, wide, &TextEmbeddingNearestOptions{K: 1})
	must(err)
	rec.call("createNorth")
	_, err = db.Chunk.CreateOne(ctx, &types.Chunk{Text: "north", Embedding: types.EmbeddingVector{0, 1, 0}, Note: "private"})
	must(err)
	rec.call("createEastChunk")
	eastChunk, err := db.Chunk.CreateOne(ctx, &types.Chunk{Text: "east", Embedding: types.EmbeddingVector{1, 0, 0}})
	must(err)
	rec.call("nearestChunk")
	_, err = db.Chunk.Nearest(ctx, ChunkVectorEmbedding, query, &ChunkNearestOptions{K: 2, Metric: VectorL2, Filter: &ChunkFilter{Text: &StringFilter{In: []string{"north", "east"}}}})
	must(err)
	rec.call("deleteEastChunk")
	must(db.Chunk.DeleteOne(ctx, *eastChunk.Id))
	rec.call("nearestChunkWithDeleted")
	_, err = db.Chunk.Nearest(ctx, ChunkVectorEmbedding, query, &ChunkNearestOptions{IncludeDeleted: true})
	must(err)

	data, err := json.MarshalIndent(rec.calls, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
`
