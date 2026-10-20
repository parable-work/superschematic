// The wishlist's two implementations over one SQLite file, at each point of
// its second version's migration (scripts/check.sh, README.md):
//
//   bun scripts/wishlist-v2.ts <file> first      after the first version's plan
//   bun scripts/wishlist-v2.ts <file> expanded   after the second's expand phase
//   bun scripts/wishlist-v2.ts <file> contracted after its contract phase
//
// The first version is typescript/shop-wishlist, the second
// testdata/wishlist-v2/typescript/shop-wishlist; each imports only types, so
// Bun runs it from its source with no build, over openSqlite, the
// SqlDatabase stack dev gives it. Between the phases both run, as during a
// rollout; once contract drops note, the first version's statements fail
// and the second's go on.
import { openSqlite } from '../../../runtime/http/typescript/src/sqlite.ts';
import { create as first } from '../typescript/shop-wishlist/index.ts';
import { create as second } from '../testdata/wishlist-v2/typescript/shop-wishlist/index.ts';

const [file, point] = process.argv.slice(2);
if (!file || !['first', 'expanded', 'contracted'].includes(point ?? '')) {
  console.error('usage: bun scripts/wishlist-v2.ts <file> first|expanded|contracted');
  process.exit(2);
}

const db = openSqlite(file);
const logger = { debug() {}, info() {}, warn() {}, error() {} };
// The Deps each implementation reads: its database and its logger.
const deps = { db, logger } as any;
const v1 = first(deps).wishlist;
const v2 = second(deps).wishlist;
const shopper = 'ada';

const failures: string[] = [];
function expect(what: string, got: unknown, want: unknown): void {
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    failures.push(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
}
const notes = (view: any) => view.items.map((item: any) => [item.sku, item.note]);
const quantities = (view: any) => view.items.map((item: any) => [item.sku, item.quantity]);

switch (point) {
  case 'first':
    // The first version keeps a tea with a note.
    expect(
      'the first version keeps green-tea',
      notes(await v1.keepItem({ shopper, sku: 'green-tea', input: { note: 'for the mornings' } })),
      [['green-tea', 'for the mornings']]
    );
    break;
  case 'expanded': {
    // Both versions run: the first keeps a tea with a note, which leaves
    // its quantity null; the second keeps one with a quantity, and changes
    // the first's, which leaves its note.
    await v1.keepItem({ shopper, sku: 'oolong', input: { note: 'a gift' } });
    await v2.keepItem({ shopper, sku: 'matcha', input: { quantity: 3 } });
    await v2.keepItem({ shopper, sku: 'green-tea', input: { quantity: 2 } });
    expect('the first version reads', notes(await v1.getWishlist({ shopper })), [
      ['green-tea', 'for the mornings'],
      ['oolong', 'a gift'],
      ['matcha', null],
    ]);
    expect('the second version reads', quantities(await v2.getWishlist({ shopper })), [
      ['green-tea', 2],
      ['oolong', 1],
      ['matcha', 3],
    ]);
    break;
  }
  case 'contracted': {
    // note is gone: the second version reads and writes as before, and the
    // first version's statements fail.
    expect('the second version drops oolong', quantities(await v2.dropItem({ shopper, sku: 'oolong' })), [
      ['green-tea', 2],
      ['matcha', 3],
    ]);
    const columns = db
      .prepare("SELECT name FROM pragma_table_info('wishlist_item') ORDER BY cid")
      .raw<[string]>();
    // expand's ADD COLUMN put quantity last.
    expect('the columns', (await columns).map(([name]) => name), ['id', 'shopper', 'sku', 'added_at', 'quantity']);
    let failed = false;
    try {
      await v1.getWishlist({ shopper });
    } catch {
      failed = true;
    }
    expect('the first version fails once note is gone', failed, true);
    break;
  }
}
db.close();

if (failures.length > 0) {
  console.error(`the wishlist's second version, ${point}:\n${failures.join('\n')}`);
  process.exit(1);
}
console.log(`the wishlist's second version, ${point}: ok`);
