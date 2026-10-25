import { afterEach, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { at, databaseKinds, type TestDatabase } from './fixture.testing';

/*
A User table may hold columns the store's insert leaves out: registration
writes a user's login and name alone. A required column with a
Default<T, V> takes V from its DEFAULT (D83), so registration succeeds; the
same column without one refuses the insert. The DDL is the fixture's with
a required active: Default<boolean, false> on the User table
(runtime/http/testdata/identity/defaults, which
TestIdentityStoreFixtureIsCurrent in internal/generator writes).
*/

const defaultsDir = new URL('../../../testdata/identity/defaults/', import.meta.url);
const ddl = {
  sqlite: readFileSync(new URL('sqlite/create.sql', defaultsDir), 'utf8'),
  postgres: readFileSync(new URL('create.sql', defaultsDir), 'utf8'),
};

const open: TestDatabase[] = [];
afterEach(async () => {
  while (open.length > 0) await open.pop()!.close();
});

for (const kind of databaseKinds) {
  const dialect = kind.name === 'postgres' ? 'postgres' : 'sqlite';
  describe(`registration with a defaulted column on ${kind.name}`, () => {
    test('a required column with a Default takes it, so registration writes the user', async () => {
      const db = await kind.open(ddl[dialect]);
      open.push(db);
      const user = await db.store().createUser({ login: 'ada@example.com', name: 'Ada', passwordHash: 'hash-of-ada', at: at() });
      expect(user).toMatchObject({ login: 'ada@example.com', name: 'Ada', disabled: false });
      const rows = await db.query('SELECT active FROM "user" WHERE email = ?', ['ada@example.com']);
      expect(rows.map(row => Boolean(row.active))).toEqual([false]);
    });

    test('the same column without its DEFAULT refuses the insert', async () => {
      const withoutDefault = ddl[dialect].replace(/(\bactive\b"? \w+) DEFAULT \w+ NOT NULL/u, '$1 NOT NULL');
      expect(withoutDefault).not.toBe(ddl[dialect]);
      const db = await kind.open(withoutDefault);
      open.push(db);
      let refused = false;
      try {
        await db.store().createUser({ login: 'ada@example.com', name: 'Ada', passwordHash: 'hash-of-ada', at: at() });
      } catch {
        refused = true;
      }
      expect(refused).toBe(true);
    });
  });
}
