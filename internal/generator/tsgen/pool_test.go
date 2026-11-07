package tsgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// poolFixture is the DB service of D104's Pool: an Account that implements
// Pool, ranked by its rank, beside a Budget that meters its tokens by the
// hour, whose rechecks are events of its event queue.
const poolFixture = "fixture-pool-db"

// poolCheck is the file tsc checks the fixture's repositories against:
// Pool's markOut and reportRoom under pool, each taking the row's key, and
// its pick, an operation of the table as a whole (D104), taking none and no
// preconditions, its match and prefer typed by the table's own fields, each
// taking a value and no null, and its answer a member or none with until; a table whose behaviors keep
// sub-tables has no upserts, and one whose behaviors emit events hands an
// event of its event queue to a handler in the transaction that spends its
// claim (runEvent).
const poolCheck = `import type { AccountRow, FixturePoolDbOrm } from './orm';
import { fixturePoolDbRules } from './orm';

export async function choose(orm: FixturePoolDbOrm, owner: AccountRow['owner'], id: AccountRow['id']): Promise<string | number | undefined> {
  const out = await orm.account.pool.markOut(id, { reason: 'rate limited' });
  const until: number = out.outUntil;
  void until;
  const checked = await orm.account.pool.reportRoom(id, { room: true, near: true }, { caller: { subject: 'worker', permissions: ['accounts.pool'] } });
  const availability: 'open' | 'near' | 'out' = checked.availability;
  void availability;
  const picked = await orm.account.pool.pick({ match: { owner, rank: 1 }, prefer: { vendor: 'anthropic' }, exclude: [id] }, { caller: { subject: 'worker' } });
  if (picked.member === null) return picked.until;
  const order: number | null = picked.member.order;
  void order;
  return picked.member.key;
}

// @ts-expect-error pick runs on the table as a whole: it takes no key.
export const keyed = (orm: FixturePoolDbOrm, id: AccountRow['id']) => orm.account.pool.pick(id, {});
// @ts-expect-error an operation of the table takes no preconditions.
export const fenced = (orm: FixturePoolDbOrm) => orm.account.pool.pick({}, { preconditions: { Pool: {} } });
// @ts-expect-error match holds own fields of the table alone.
export const stray = (orm: FixturePoolDbOrm) => orm.account.pool.pick({ match: { poolAvailability: 'open' } });
// @ts-expect-error a member of match takes a value: the core reads a null one as absent (D12).
export const nothing = (orm: FixturePoolDbOrm) => orm.account.pool.pick({ match: { rank: null } });

export async function handle(orm: FixturePoolDbOrm, event: { rowKey: AccountRow['id']; useId: string; kind: string; at: number; payload: unknown }): Promise<boolean> {
  const run = await orm.transaction(tx => tx.account.runEvent(event, { claim: { id: 'message', token: 'token' } }, async () => {}));
  return run.ran;
}

// @ts-expect-error A table whose behaviors keep a sub-table has no upserts.
export const upsert = (orm: FixturePoolDbOrm) => orm.account.upsertOne;
export const queue: string = fixturePoolDbRules.types.Account.events!.table;
export const pick: boolean = fixturePoolDbRules.types.Account.uses[0]!.operations[2]!.table === true;
`

// TestPoolTablesCompile type-checks fixture-pool-db's generated package
// (buildTSPackages, over the local http runtime and @superschematic/rules)
// against poolCheck: Pool, whose operations of a row and of the table as a
// whole orm.ts types from its declaration as the config and the table's
// fields narrow them (D104), with no code of its own in the generator.
func TestPoolTablesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' type check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, poolFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", poolFixture, err)
	}
	buildTSPackages(t, []tsPackageCase{{
		name:     poolFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"pool.check.ts": poolCheck},
	}})
}
