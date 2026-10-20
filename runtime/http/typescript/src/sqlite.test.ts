import { afterEach, describe, expect, test } from 'bun:test';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { PING_TIMEOUT_MS, isSqlDatabase, ping, type SqlDatabase } from './d1';
import { openSqlite, sqliteFile, type SqliteDatabase } from './sqlite';

/*
SqlDatabase over bun:sqlite (D64): what stack dev opens a sqlite-first
database with, held to the D1 behaviors an implementation could rely on.
*/

const opened: SqliteDatabase[] = [];
const dirs: string[] = [];

function open(): SqliteDatabase {
  const dir = mkdtempSync(join(tmpdir(), 'sqlite-test-'));
  dirs.push(dir);
  const db = openSqlite(join(dir, 'shop.db'));
  opened.push(db);
  return db;
}

afterEach(() => {
  for (const db of opened.splice(0)) db.close();
  for (const dir of dirs.splice(0)) rmSync(dir, { recursive: true, force: true });
});

async function shop(): Promise<SqliteDatabase> {
  const db = open();
  const created = await db.exec(
    [
      'CREATE TABLE product (id INTEGER PRIMARY KEY, sku TEXT NOT NULL UNIQUE, name TEXT NOT NULL, in_stock INTEGER NOT NULL, image BLOB)',
      '',
      'CREATE TABLE stock (product_id INTEGER NOT NULL REFERENCES product (id), level INTEGER NOT NULL)',
    ].join('\n')
  );
  expect(created.count).toBe(2);
  return db;
}

describe('openSqlite', () => {
  test('is a SqlDatabase', async () => {
    const db = open();
    expect(isSqlDatabase(db)).toBe(true);
    expect(isSqlDatabase({ prepare() {} })).toBe(false);
    await ping(db);
    expect(PING_TIMEOUT_MS).toBe(2000);
  });

  test('binds ? and ?N, booleans as 1 and 0, and bytes', async () => {
    const db = await shop();
    const insert = db.prepare('INSERT INTO product (sku, name, in_stock, image) VALUES (?, ?, ?, ?)');
    const tea = await insert.bind('tea', 'Tea', true, new Uint8Array([1, 2])).run();
    expect(tea.success).toBe(true);
    expect(tea.results).toEqual([]);
    expect(tea.meta.changes).toBe(1);
    expect(tea.meta.last_row_id).toBe(1);
    expect(tea.meta.changed_db).toBe(true);
    await insert.bind('cocoa', 'Cocoa', false, new Uint8Array([3]).buffer).run();
    // ?2 and ?1 bind by their numbers, and one value serves both ?1s.
    const row = await db.prepare('SELECT name, in_stock FROM product WHERE sku = ?2 AND id = ?1 AND ?1 = 1').bind(1, 'tea').first();
    expect(row).toEqual({ name: 'Tea', in_stock: 1 });
    expect(await db.prepare('SELECT name FROM product WHERE in_stock = ?').bind(false).first<string>('name')).toBe('Cocoa');
    const image = await db.prepare('SELECT image FROM product WHERE sku = ?').bind('tea').first<Uint8Array>('image');
    expect(Array.from(image!)).toEqual([1, 2]);
  });

  test('first, all, run and raw answer as D1 does', async () => {
    const db = await shop();
    await db.prepare('INSERT INTO product (sku, name, in_stock) VALUES (?, ?, 1), (?, ?, 0)').bind('tea', 'Tea', 'cocoa', 'Cocoa').run();
    const all = await db.prepare('SELECT sku FROM product ORDER BY sku').all<{ sku: string }>();
    expect(all.results).toEqual([{ sku: 'cocoa' }, { sku: 'tea' }]);
    expect(all.meta.changes).toBe(0);
    expect(all.meta.changed_db).toBe(false);
    expect(await db.prepare('SELECT sku FROM product WHERE sku = ?').bind('none').first()).toBeNull();
    expect(await db.prepare('SELECT sku FROM product WHERE sku = ?').bind('none').first('sku')).toBeNull();
    await expect(db.prepare('SELECT sku FROM product').first('price')).rejects.toThrow('has no column price');
    expect(await db.prepare('SELECT sku, in_stock FROM product ORDER BY sku').raw()).toEqual([
      ['cocoa', 0],
      ['tea', 1],
    ]);
    expect(await db.prepare('SELECT sku FROM product ORDER BY sku').raw({ columnNames: true })).toEqual([['sku'], ['cocoa'], ['tea']]);
    const returned = await db.prepare('UPDATE product SET in_stock = 1 WHERE sku = ? RETURNING sku').bind('cocoa').run();
    expect(returned.results).toEqual([{ sku: 'cocoa' }]);
    expect(returned.meta.changes).toBe(1);
  });

  test('batch is atomic', async () => {
    const db = await shop();
    const insert = db.prepare('INSERT INTO product (sku, name, in_stock) VALUES (?, ?, 1)');
    const done = await db.batch([insert.bind('tea', 'Tea'), insert.bind('cocoa', 'Cocoa'), db.prepare('SELECT count(*) AS n FROM product')]);
    expect(done.map(result => result.meta.changes)).toEqual([1, 1, 0]);
    expect(done[2]!.results).toEqual([{ n: 2 }]);
    // The second insert breaks the unique sku: the first is rolled back.
    await expect(db.batch([insert.bind('coffee', 'Coffee'), insert.bind('tea', 'Tea again')])).rejects.toThrow('UNIQUE');
    expect(await db.prepare('SELECT count(*) AS n FROM product').first('n')).toBe(2);
    await expect(db.batch([open().prepare('SELECT 1')])).rejects.toThrow('was not prepared on the SQLite database');
  });

  test('enforces foreign keys, as D1 does', async () => {
    const db = await shop();
    await expect(db.prepare('INSERT INTO stock (product_id, level) VALUES (?, ?)').bind(42, 1).run()).rejects.toThrow('FOREIGN KEY');
  });

  test('exec runs a script one line per statement', async () => {
    const db = await shop();
    const result = await db.exec("INSERT INTO product (sku, name, in_stock) VALUES ('tea', 'Tea', 1)\n\nDELETE FROM product WHERE sku = 'none'\n");
    expect(result.count).toBe(2);
    expect(result.duration).toBeGreaterThanOrEqual(0);
    expect(await db.prepare('SELECT count(*) AS n FROM product').first('n')).toBe(1);
  });

  test('a closed database answers nothing', async () => {
    const db = open();
    db.close();
    await expect(db.prepare('SELECT 1').first()).rejects.toThrow('is closed');
    await expect(db.exec('SELECT 1')).rejects.toThrow('is closed');
  });
});

describe('sqliteFile', () => {
  test('reads the file a sqlite: URL names, as the migration runner does', () => {
    expect(sqliteFile('sqlite:/tmp/shop.sqlite')).toBe('/tmp/shop.sqlite');
    expect(sqliteFile('sqlite:///tmp/shop.sqlite')).toBe('/tmp/shop.sqlite');
    expect(sqliteFile('sqlite://shop.sqlite')).toBe('shop.sqlite');
    expect(sqliteFile('SQLITE:shop.sqlite')).toBe('shop.sqlite');
  });

  test('refuses any other URL, naming its scheme alone', () => {
    expect(() => sqliteFile('postgres://shop:secret@127.0.0.1/shop')).toThrow(
      "the database's URL is a postgres: URL; a sqlite-first database's is sqlite:<path>"
    );
    expect(() => sqliteFile('postgres://shop:secret@127.0.0.1/shop')).not.toThrow('secret');
    expect(() => sqliteFile('sqlite:shop.sqlite?mode=ro')).toThrow('a sqlite: URL with a query or no path');
    expect(() => sqliteFile('sqlite:')).toThrow('a sqlite: URL with a query or no path');
    expect(() => sqliteFile('/tmp/shop.sqlite')).toThrow('is no URL');
  });

  test('names a file openSqlite opens', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'sqlite-test-'));
    dirs.push(dir);
    const db = openSqlite(sqliteFile(`sqlite:${join(dir, 'shop.sqlite')}`));
    opened.push(db);
    await ping(db);
    expect(db.path).toBe(join(dir, 'shop.sqlite'));
  });
});

describe('ping', () => {
  test('rejects a database that does not answer in time', async () => {
    const never: SqlDatabase = {
      prepare: () => ({ first: () => new Promise(() => {}) }) as never,
      batch: async () => [],
      exec: async () => ({ count: 0, duration: 0 }),
    };
    await expect(ping(never, 20)).rejects.toThrow('did not answer within 20 ms');
  });

  test('rejects a database that answers SELECT 1 wrongly', async () => {
    const wrong: SqlDatabase = {
      prepare: () => ({ first: async () => 2 }) as never,
      batch: async () => [],
      exec: async () => ({ count: 0, duration: 0 }),
    };
    await expect(ping(wrong)).rejects.toThrow('answered SELECT 1 with 2');
  });
});
