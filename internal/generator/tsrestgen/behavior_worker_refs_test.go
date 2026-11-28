package tsrestgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// workerRefsAPI is the API whose behavior worker FilingDeadlines hands the
// events of fixture-worker-refs-db's Filing to the project's Workers
// (D104), over a database that declares @references and @attachments
// (D154), so its transaction type holds both.
const workerRefsAPI = "fixture-worker-refs-api"

// workerRefsDB is fixture-worker-refs-api's database, its one DB
// dependency.
const workerRefsDB = "fixture-worker-refs-db"

// workerRefsFixture is fixture-worker-refs-api with its database and
// apigen's endpoints for it, read with one registry.
func workerRefsFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, workerRefsDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", workerRefsDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, workerRefsAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", workerRefsAPI, err)
	}
	deps := map[string]*ir.Schema{workerRefsDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     workerRefsAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: workerRefsAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, depsInfo: workerRefsDeps}
}

// workerRefsDeps is fixture-worker-refs-api's Deps as the dispatch layer
// sets it.
var workerRefsDeps = DepsInfo{
	Database:             workerRefsDB,
	Dialect:              "postgres",
	ORM:                  "FixtureWorkerRefsDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-worker-refs-db-types",
}

// TestGeneratedWorkerOverReferencesCompiles type-checks the package for
// fixture-worker-refs-api against its type packages and the real runtime,
// with workerRefsCheck: behavior-workers.ts hands each event the
// transaction its behavior caller's ORM opens, which is the database's
// transaction type, references and attachments with it (D154); a Workers
// method reads both; and the ORM's transaction, and one nested in it,
// hands its function that type too.
func TestGeneratedWorkerOverReferencesCompiles(t *testing.T) {
	tree := materializeAPI(t, workerRefsFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "worker-refs.check.ts"), []byte(workerRefsCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}

const workerRefsCheck = `import type { Pool } from 'pg';
import type { FixtureWorkerRefsDbOrm, FixtureWorkerRefsDbTx } from '@schemas/fixture-worker-refs-db-types/orm';
import type { WorkerQueue } from '@superschematic/http-runtime/worker';
import { filingDeadlinesQueues } from './index';
import type { Workers } from './deps';

declare const orm: FixtureWorkerRefsDbOrm;
declare const pool: Pool;

export const workers: Workers = {
  async filingDeadlinesRecurrenceOpened(event, tx) {
    await tx.references.of({ table: 'Filing', key: event.key });
  },
  async filingDeadlinesRecurrenceReminded(event, tx) {
    await tx.attachments.list({ table: 'Filing', key: event.key });
  },
  async filingDeadlinesRecurrenceDue() {},
  async filingDeadlinesRecurrenceMissed() {},
};
export const queues: WorkerQueue[] = filingDeadlinesQueues(pool, orm, workers);
export const inTransaction: Promise<unknown> = orm.transaction(async tx => {
  const held: FixtureWorkerRefsDbTx = tx;
  await held.transaction(async nested => nested.references.of({ table: 'Filing', key: 'k' }));
  return orm.withBehaviorCaller(null).asSystem().transaction(caller => caller.attachments.list({ table: 'Filing', key: 'k' }));
});
`
