// The repositories of fixture-vault-db on Postgres, whose Encrypted<string>
// fields (D85) a write seals into envelopes bound to their row and a read
// opens into SecretValues, with the keys createOrm takes: a wrong key, no
// key and an envelope moved to another row fail with FieldEncryptionError,
// one statement cannot set an encrypted field of many rows, an upsert on any
// unique key seals one under the key of the row it updates (D89), and reencrypt
// reseals the envelopes of an older key under the current one.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { FieldEncryptionError, FieldKeyring, SecretValue, uuidToDb, type FieldEncryptionReason } from '@superschematic/http-runtime/postgres';
import { createOrm, type FixtureVaultDbOrm } from './orm';
import { bob, database, dsn, recording, type TestDatabase } from './harness';

// The keys of runtime/schema/testdata/fieldcrypt_parity.json.
const current = 'AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=';
const next = 'ZWZnaGlqa2xtbm9wcXJzdHV2d3h5ent8fX5/gIGCg4Q=';
const currentId = FieldKeyring.parse(current).currentId;
const nextId = FieldKeyring.parse(next).currentId;

let db: TestDatabase;
let orm: FixtureVaultDbOrm;

beforeAll(async () => {
  db = await database('ts_orm_vault');
  orm = createOrm(db.pool, { fieldKeys: new SecretValue(current) });
});

afterAll(async () => {
  await db?.drop();
});

/** The reason a read or a write failed with FieldEncryptionError. */
async function reason(op: () => Promise<unknown>): Promise<FieldEncryptionReason> {
  try {
    await op();
  } catch (error) {
    if (error instanceof FieldEncryptionError) return error.reason;
    throw error;
  }
  throw new Error('the operation did not fail');
}

/** The envelopes the row whose key is id holds, as psql would show them. */
async function stored(id: string): Promise<{ access_token: string; webhook_secret: string | null }> {
  const { rows } = await db.pool.query('SELECT access_token, webhook_secret FROM plaid_item WHERE id = $1', [uuidToDb(id)]);
  return rows[0];
}

describe.skipIf(dsn === '')('Encrypted<T> fields under the repositories', () => {
  test('a write seals each encrypted field into an envelope of its row, and a read opens it into a SecretValue', async () => {
    const item = await orm.plaidItem.createOne({ itemId: 'item-1', institution: 'First Bank', accessToken: 'access-sandbox-1', webhookSecret: new SecretValue('whsec-1') });
    expect(item.accessToken).toBeInstanceOf(SecretValue);
    expect([item.accessToken.reveal(), item.webhookSecret?.reveal()]).toEqual(['access-sandbox-1', 'whsec-1']);
    expect(JSON.stringify(item)).not.toContain('access-sandbox-1');
    // The key, which the envelope is bound to, is generated before the insert.
    const raw = await stored(item.id);
    expect(raw.access_token.startsWith(`v1:${currentId}:`)).toBe(true);
    expect(raw.access_token).not.toContain('access-sandbox-1');
    expect(raw.webhook_secret).not.toContain('whsec-1');

    const [second, third] = await orm.plaidItem.createMany([
      { itemId: 'item-2', institution: 'Second Bank', accessToken: 'access-sandbox-2' },
      { itemId: 'item-3', institution: 'Third Bank', accessToken: 'access-sandbox-3', webhookSecret: 'whsec-3' },
    ]);
    expect([second!.webhookSecret, third!.webhookSecret?.reveal()]).toEqual([null, 'whsec-3']);
    expect((await orm.plaidItem.getOne(item.id)).accessToken.reveal()).toBe('access-sandbox-1');
    const many = await orm.plaidItem.getManyByIds([item.id, second!.id]);
    expect(many.get(second!.id)?.accessToken.reveal()).toBe('access-sandbox-2');
    const found = await orm.plaidItem.findMany({ institution: { ilike: 'bank' } }, { orderBy: [{ field: 'itemId' }] });
    expect(found.rows.map(r => r.accessToken.reveal())).toEqual(['access-sandbox-1', 'access-sandbox-2', 'access-sandbox-3']);
    const page = await orm.plaidItem.findPage({ searchText: 'third' }, { limit: 1 });
    expect(page.rows[0]?.webhookSecret?.reveal()).toBe('whsec-3');

    const updated = await orm.plaidItem.updateOne(item.id, { accessToken: 'access-sandbox-1b', webhookSecret: null });
    expect([updated.accessToken.reveal(), updated.webhookSecret]).toEqual(['access-sandbox-1b', null]);
    expect((await stored(item.id)).webhook_secret).toBeNull();

    // A key the create sets is the one the envelope is bound to.
    const id = bob;
    const credential = await orm.apiCredential.createOne({ id, service: 'voyage', secret: 'pa-voyage' });
    expect([credential.id, credential.secret.reveal()]).toEqual([id, 'pa-voyage']);
    const generated = await orm.apiCredential.createOne({ service: 'plaid', secret: 'plaid-secret' });
    expect(generated.id).not.toBe(id);
    expect((await orm.apiCredential.findOne({ service: { eq: 'plaid' } }))?.secret.reveal()).toBe('plaid-secret');
  });

  test('a wrong key, no key and an envelope moved to another row fail with FieldEncryptionError', async () => {
    const a = await orm.plaidItem.createOne({ itemId: 'move-a', institution: 'A', accessToken: 'token-a' });
    const b = await orm.plaidItem.createOne({ itemId: 'move-b', institution: 'B', accessToken: 'token-b' });
    expect(await reason(() => createOrm(db.pool, { fieldKeys: next }).plaidItem.getOne(a.id))).toBe('unknown-key');
    const keyless = createOrm(db.pool);
    expect(await reason(() => keyless.plaidItem.getOne(a.id))).toBe('no-keys');
    expect(await reason(() => keyless.plaidItem.createOne({ itemId: 'x', institution: 'X', accessToken: 'x' }))).toBe('no-keys');
    expect(() => createOrm(db.pool, { fieldKeys: 'not base64!' })).toThrow(FieldEncryptionError);
    // An envelope copied to another row does not open there.
    await db.pool.query('UPDATE plaid_item SET access_token = (SELECT access_token FROM plaid_item WHERE id = $1) WHERE id = $2', [uuidToDb(a.id), uuidToDb(b.id)]);
    expect(await reason(() => orm.plaidItem.getOne(b.id))).toBe('decrypt');
    await orm.plaidItem.updateOne(b.id, { accessToken: 'token-b' });
    expect((await orm.plaidItem.getOne(b.id)).accessToken.reveal()).toBe('token-b');
  });

  test('updateMany cannot set an encrypted field; an upsert on any unique key sets one under the key of the row it updates, by a second statement when it differs', async () => {
    const item = await orm.plaidItem.createOne({ itemId: 'upsert-1', institution: 'Upsert Bank', accessToken: 'token-1' });
    await expect(orm.plaidItem.updateMany({ itemId: { eq: 'upsert-1' } }, { accessToken: 'token-2' })).rejects.toThrow(/updateMany of PlaidItem cannot set accessToken/u);
    expect(await orm.plaidItem.updateMany({ itemId: { eq: 'upsert-1' } }, { institution: 'Renamed Bank' })).toBe(1);
    // An upsert on another key that updates no encrypted field keeps the row's own envelope.
    const kept = await orm.plaidItem.upsertOne({ itemId: 'upsert-1', institution: 'U', accessToken: 'token-3' }, ['itemId'], { institution: 'Kept Bank' });
    expect([kept.institution, kept.accessToken.reveal()]).toEqual(['Kept Bank', 'token-1']);
    const byKey = await orm.plaidItem.upsertOne({ id: item.id, itemId: 'upsert-1', institution: 'U', accessToken: 'token-4' }, ['id'], { accessToken: 'token-4' });
    expect(byKey.accessToken.reveal()).toBe('token-4');
    expect((await orm.plaidItem.getOne(item.id)).accessToken.reveal()).toBe('token-4');
    // By itemId (D89): the envelope is sealed under the row's key, not the
    // one the insert generated, and opens there.
    const { pool, statements } = recording(db.pool);
    const recorded = createOrm(pool, { fieldKeys: current });
    const byItem = await recorded.plaidItem.upsertOneResult({ itemId: 'upsert-1', institution: 'U', accessToken: 'token-5' }, ['itemId'], { accessToken: 'token-5' });
    expect([byItem.status, byItem.row.id, byItem.row.accessToken.reveal()]).toEqual(['updated', item.id, 'token-5']);
    expect(statements.filter(s => s !== 'BEGIN' && s !== 'COMMIT').map(s => s.split(' ')[0])).toEqual(['INSERT', 'UPDATE']);
    expect((await orm.plaidItem.getOne(item.id)).accessToken.reveal()).toBe('token-5');
    // The same token again changes nothing, and writes no second statement.
    statements.length = 0;
    const same = await recorded.plaidItem.upsertOneResult({ itemId: 'upsert-1', institution: 'U', accessToken: 'token-5' }, ['itemId'], { accessToken: 'token-5' }, { onlyIfChanged: true });
    expect([same.status, same.row.accessToken.reveal()]).toEqual(['unchanged', 'token-5']);
    expect(statements.filter(s => s !== 'BEGIN' && s !== 'COMMIT').map(s => s.split(' ')[0])).toEqual(['INSERT', 'SELECT']);
    // A null clears an optional one in the statement itself.
    const cleared = await orm.plaidItem.upsertOne({ itemId: 'upsert-1', institution: 'U', accessToken: 'token-5' }, ['itemId'], { webhookSecret: null });
    expect(cleared.webhookSecret).toBeNull();
  });

  test('plaidItem.upsertOne by itemId inserts an item sealed under the key it generates, then updates its token; upsertMany seals each item it inserts or updates', async () => {
    const created = await orm.plaidItem.upsertOneResult({ itemId: 'sync-1', institution: 'Sync Bank', accessToken: 'access-1' }, ['itemId'], { accessToken: 'access-1' });
    expect([created.status, created.row.accessToken.reveal()]).toEqual(['inserted', 'access-1']);
    expect((await stored(created.row.id)).access_token.startsWith(`v1:${currentId}:`)).toBe(true);
    const relinked = await orm.plaidItem.upsertOne({ itemId: 'sync-1', institution: 'Sync Bank', accessToken: 'access-2' }, ['itemId'], { accessToken: 'access-2' });
    expect([relinked.id, relinked.accessToken.reveal()]).toEqual([created.row.id, 'access-2']);
    const items = await orm.plaidItem.upsertMany(
      [
        { itemId: 'sync-1', institution: 'Sync Bank', accessToken: 'access-3' },
        { itemId: 'sync-2', institution: 'Other Bank', accessToken: 'other-1', webhookSecret: 'whsec' },
      ],
      ['itemId'],
      ['institution', 'accessToken', 'webhookSecret'],
      { onlyIfChanged: true }
    );
    expect(items.map(r => [r.status, r.row.itemId, r.row.accessToken.reveal(), r.row.webhookSecret?.reveal() ?? null])).toEqual([
      ['updated', 'sync-1', 'access-3', null],
      ['inserted', 'sync-2', 'other-1', 'whsec'],
    ]);
    expect(items[0]!.row.id).toBe(created.row.id);
    expect((await orm.plaidItem.getOne(created.row.id)).accessToken.reveal()).toBe('access-3');
    const again = await orm.plaidItem.upsertMany([{ itemId: 'sync-1', institution: 'Sync Bank', accessToken: 'access-3' }], ['itemId'], ['institution', 'accessToken'], { onlyIfChanged: true });
    expect(again.map(r => r.status)).toEqual(['unchanged']);
  });

  test('no filter holds an encrypted field, and the DDL bounds the plaintext, not the envelope', async () => {
    const before = await orm.plaidItem.findMany(null);
    const ignored = await orm.plaidItem.findMany({ accessToken: { eq: 'token-4' } } as never);
    expect(ignored.total).toBe(before.total);
    // Validate's maxLength 200 bounds the plaintext; its envelope is longer, and no CHECK refuses it.
    const long = 'x'.repeat(190);
    const item = await orm.plaidItem.createOne({ itemId: 'long', institution: 'Long Bank', accessToken: long });
    expect((await stored(item.id)).access_token.length).toBeGreaterThan(200);
    expect((await orm.plaidItem.getOne(item.id)).accessToken.reveal()).toBe(long);
  });

  test('reencrypt reseals the envelopes of an older key under the current one, and the older key can then leave the secret', async () => {
    const rotated = createOrm(db.pool, { fieldKeys: `${next}, ${current}` });
    const before = await orm.plaidItem.findMany(null, { limit: 1000, includeDeleted: true });
    const resealed = await rotated.plaidItem.reencrypt({ batch: 2 });
    expect(resealed).toBe(before.total);
    expect(await rotated.plaidItem.reencrypt()).toBe(0);
    expect(await rotated.apiCredential.reencrypt()).toBe(2);
    const { rows } = await db.pool.query('SELECT access_token, webhook_secret FROM plaid_item');
    for (const row of rows) {
      expect(row.access_token.startsWith(`v1:${nextId}:`)).toBe(true);
      if (row.webhook_secret !== null) expect(row.webhook_secret.startsWith(`v1:${nextId}:`)).toBe(true);
    }
    const after = await createOrm(db.pool, { fieldKeys: next }).plaidItem.findMany(null, { limit: 1000, includeDeleted: true });
    expect(after.rows.map(r => r.accessToken.reveal()).sort()).toEqual(before.rows.map(r => r.accessToken.reveal()).sort());
  });
});
