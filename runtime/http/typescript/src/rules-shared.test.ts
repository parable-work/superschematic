import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { execute, readSharedRow, runCreate, runDelete, runOperation, type CoreCaller, type RulesHost, type RulesLayout, type RulesStore } from './rules';
import { bumpVersion, columnTypes, core, postgresDatabase, rulesCore, rulesDsn, sqliteDatabase, type TestDatabase } from './rules.testing';

// A use's shared table (D103's step 4) through the adapter over a real
// database of each dialect: an invoice table whose number is a Numbering
// field, written by hand as sqlgen writes one, its label on the field's own
// column and its seq beside it, and Numbering's counts, a table of the use
// kept for the table as a whole, keyed by the scope and held to a version
// of its own. Each create reads its count and writes it held to the version
// it read, so a write another connection commits between the read and the
// apply, an update or the first insert of the count, moves the command, which
// runs again and takes the next number; two transactions that number at once
// on Postgres take two numbers; and a delete of a row leaves the count where
// it was. The Postgres cases are skipped without
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL.

const config = { format: 'INV-{seq:3}', scope: { link: 'customer' } };

const layout: RulesLayout = {
  types: {
    Invoice: {
      type: 'Invoice',
      table: 'invoice',
      key: { field: 'id', column: 'id', type: 'string' },
      version: '_version',
      own: [
        { field: 'title', column: 'title', type: 'string' },
        { field: 'customerId', column: 'customer_id', type: 'string' },
      ],
      uses: [
        {
          id: 'number',
          behavior: 'Numbering',
          anchor: 'number',
          fields: [
            { name: 'seq', field: 'numberSeq', column: 'number_seq', type: 'Generic.Int64' },
            { name: 'label', field: 'number', column: 'number', type: 'string', value: true },
          ],
          operations: [{ name: 'assignNumber', writes: true }],
          shared: [
            {
              name: 'counts',
              table: 'invoice_number_counts',
              key: { name: 'scope', field: 'scope', column: 'scope', type: 'string' },
              fields: [
                { name: 'last', field: 'last', column: 'last', type: 'Generic.Int64' },
                { name: 'counting', field: 'counting', column: 'counting', type: 'string' },
              ],
              version: '_version',
            },
          ],
        },
      ],
      spec: {
        name: 'Invoice',
        uses: [{ behavior: 'Numbering', config, anchor: { field: 'number' } }],
        fields: [
          { name: 'title', type: 'string' },
          { name: 'customerId', type: 'string', relation: { name: 'customer', type: 'Customer' } },
        ],
      },
    },
  },
};

const alice: CoreCaller = { subject: 'alice', permissions: [] };

const dialects: { name: string; skip: boolean; open: () => Promise<TestDatabase> }[] = [
  { name: 'bun:sqlite', skip: rulesCore === undefined, open: async () => sqliteDatabase() },
  { name: 'Postgres', skip: rulesCore === undefined || rulesDsn === '', open: postgresDatabase },
];

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(`a use's shared table on ${dialect.name}`, () => {
    let db: TestDatabase;
    let clock = Date.UTC(2026, 2, 1);
    const p = (n: number) => (db.dialect === 'postgres' ? `$${n}` : `?${n}`);

    beforeAll(async () => {
      db = await dialect.open();
      const t = columnTypes(db.dialect);
      await db.exec(`CREATE TABLE invoice (id ${t.key}, title ${t.text}, customer_id ${t.text}, number ${t.text}, number_seq ${t.integer}, _version ${t.version})`);
      for (const ddl of bumpVersion('invoice', db.dialect)) await db.exec(ddl);
      // A label unique within its customer, as the docs show: a racing
      // create must meet its count's fence before this index, or it fails
      // as an error rather than running again.
      await db.exec('CREATE UNIQUE INDEX uq_invoice_customer_number ON invoice (customer_id, number)');
      await db.exec(`CREATE TABLE invoice_number_counts (scope ${t.key}, last ${t.integer}, counting ${t.text}, _version ${t.version})`);
      for (const ddl of bumpVersion('invoice_number_counts', db.dialect)) await db.exec(ddl);
    });
    afterAll(async () => db?.close());

    const inTransaction = <T>(fn: (host: RulesHost) => Promise<T>, wrap: (store: RulesStore) => RulesStore = s => s): Promise<T> => {
      const now = (clock += 1000);
      return db.transaction(store => fn({ core: core(), layout, store: wrap(store), now: () => now }));
    };

    const label = async (id: string) => (await db.rows(`SELECT number FROM invoice WHERE id = ${p(1)}`, [id]))[0]?.number;
    const count = async (scope: string) => {
      const row = (await db.rows(`SELECT last, _version FROM invoice_number_counts WHERE scope = ${p(1)}`, [scope]))[0];
      return row === undefined ? undefined : { last: Number(row.last), version: Number(row._version) };
    };

    /** A store whose first apply sees another connection's write first, as one committed between the read and the apply. */
    const racing = (text: string, values: readonly unknown[]) => (store: RulesStore): RulesStore => {
      let calls = 0;
      return {
        ...store,
        async apply(statements) {
          if (calls++ === 0) await db.concurrently(text, values);
          return store.apply(statements);
        },
      };
    };

    test('a create inserts its count at version 1 and the next updates it held to its version', async () => {
      await inTransaction(host => runCreate(host, 'Invoice', { title: 'a', customerId: 'c1' }, alice, { id: 'i1' }));
      expect(await label('i1')).toBe('INV-001');
      expect(await count('c1')).toEqual({ last: 1, version: 1 });
      await inTransaction(host => runCreate(host, 'Invoice', { title: 'b', customerId: 'c1' }, alice, { id: 'i2' }));
      expect(await label('i2')).toBe('INV-002');
      expect(await count('c1')).toEqual({ last: 2, version: 2 });
      expect(await inTransaction(host => readSharedRow(host, 'Invoice', 'number', 'counts', 'c1'))).toEqual({
        key: 'c1',
        values: { last: 2, counting: '{"format":"INV-{seq:3}","reset":null,"scope":"customer"}' },
        version: 2,
      });
      expect(await inTransaction(host => readSharedRow(host, 'Invoice', 'number', 'counts', 'c9'))).toBeNull();
    });

    test('a count another connection moves between the read and the apply moves the create, which runs again and takes the next number', async () => {
      const result = await inTransaction(
        host => execute(host, 'Invoice', { caller: alice, now: clock, action: { kind: 'create', id: 'i3', own: { title: 'c', customerId: 'c1' }, params: {} } }),
        racing(`UPDATE invoice_number_counts SET last = last + 1, _version = _version + 1 WHERE scope = ${p(1)}`, ['c1'])
      );
      expect(result.attempts).toBe(2);
      expect(await label('i3')).toBe('INV-004');
      expect(await count('c1')).toEqual({ last: 4, version: 4 });
    });

    test("another connection's first insert of a count moves the create too, which then updates it", async () => {
      const result = await inTransaction(
        host => execute(host, 'Invoice', { caller: alice, now: clock, action: { kind: 'create', id: 'j1', own: { title: 'd', customerId: 'c2' }, params: {} } }),
        racing(`INSERT INTO invoice_number_counts (scope, last, counting, _version) VALUES (${p(1)}, 5, ${p(2)}, 1)`, ['c2', '{"format":"INV-{seq:3}","reset":null,"scope":"customer"}'])
      );
      expect(result.attempts).toBe(2);
      expect(await label('j1')).toBe('INV-006');
      expect(await count('c2')).toEqual({ last: 6, version: 2 });
    });

    test('a delete leaves the count where it was, a gap, and assignNumber on a numbered row writes nothing', async () => {
      await inTransaction(host => runDelete(host, 'Invoice', 'i3', alice));
      await inTransaction(host => runCreate(host, 'Invoice', { title: 'e', customerId: 'c1' }, alice, { id: 'i5' }));
      expect(await label('i5')).toBe('INV-005');
      expect(await inTransaction(host => runOperation(host, 'Invoice', 'i5', 'number', 'assignNumber', {}, alice))).toEqual({ seq: 5, label: 'INV-005' });
      expect(await count('c1')).toEqual({ last: 5, version: 5 });
    });

    test.skipIf(dialect.name !== 'Postgres')('two transactions that number at once take two numbers: the second waits on the first and runs again over it', async () => {
      await Promise.all(
        ['k1', 'k2', 'k3', 'k4'].map(id => inTransaction(host => runCreate(host, 'Invoice', { title: id, customerId: 'c3' }, alice, { id })))
      );
      const labels = (await db.rows(`SELECT number FROM invoice WHERE customer_id = $1 ORDER BY number`, ['c3'])).map(row => row.number);
      expect(labels).toEqual(['INV-001', 'INV-002', 'INV-003', 'INV-004']);
      expect(await count('c3')).toEqual({ last: 4, version: 4 });
    });
  });
}
