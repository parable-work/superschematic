// fixture-merge-access-db's tables under per-record access, end to end
// (D147, D156): labels with an audience, the notes and boards that
// reference them, and brands whose name is Aliased. TestMergeAccessEndToEnd
// generates the package, type-checks it, applies the generated create.sql
// in a schema of its own and runs these cases through the generated
// repositories on Postgres. A merge finds only the two rows its caller may
// write, and writes no row of another table its caller may not write: one
// that would repoint a note or a board's pair beyond the caller is refused,
// MergeError out_of_reach naming the table and no key of the row, and
// writes nothing, where a merge outside any operation repoints them.
// Aliased's resolve answers no row for a brand the caller does not see, and
// a name another brand holds is refused naming neither its key nor its
// fields.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { MergeError, NotFoundError } from '@superschematic/http-runtime/postgres';
import { createOrm, type FixtureMergeAccessDbOrm } from './orm';
import { alice, bob, database, dsn, type TestDatabase } from './harness';

const party = '3cKSm7Pv2EM8Wl4z4XvMh1';

const BASE62 = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';

/** A UUID's hyphenated text from its base62 wire form, as its column holds it; a hyphenated one as it is. */
function uuidText(key: string): string {
  if (key.includes('-')) return key;
  let n = 0n;
  for (const c of key) n = n * 62n + BigInt(BASE62.indexOf(c));
  const hex = n.toString(16).padStart(32, '0');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

/** A grant of a party's on a row of type, reaching the rows the role's paths name. */
function grant(targetType: string, targetId: string, reaches: Record<string, readonly string[]> = {}) {
  return { id: crypto.randomUUID(), targetType, targetId, reaches };
}

describe.skipIf(dsn === '')('Postgres', () => {
  let db: TestDatabase;
  /** Outside any operation: it reads and writes every row, as a job's writes do. */
  let system: FixtureMergeAccessDbOrm;
  let asAlice: FixtureMergeAccessDbOrm;
  let asBob: FixtureMergeAccessDbOrm;
  const asParty = (...grants: ReturnType<typeof grant>[]) => system.as({ subject: party, kind: 'party', grants });

  beforeAll(async () => {
    db = await database('ts_orm_merge_access');
    system = createOrm(db.pool);
    asAlice = createOrm(db.pool, alice);
    asBob = createOrm(db.pool, bob);
  });

  afterAll(async () => {
    await db?.drop();
  });

  const count = async (text: string, key: string) => Number((await db.pool.query(text, [uuidText(key)])).rows[0].n);
  const notes = (label: string) => count('SELECT count(*) AS n FROM label_note WHERE label_id = $1::uuid', label);
  const pairs = (label: string) => count('SELECT count(*) AS n FROM board_label WHERE label_id = $1::uuid', label);
  const held = async (label: string) => (await count('SELECT count(*) AS n FROM label WHERE id = $1::uuid', label)) === 1;
  const label = async (name: string, owner: string, audience: string) => (await system.label.createOne({ name, owner, audience } as never)).id as string;
  const note = async (on: string, author: string, audience: string) =>
    (await system.labelNote.createOne({ labelId: on, author, text: 'a note', audience } as never)).id as string;
  /** A board of owner's, private to its owner, holding the labels. */
  const board = async (owner: string, ...labels: string[]) => {
    const key = (await system.board.createOne({ owner, audience: 'private' } as never)).id as string;
    for (const on of labels) await db.pool.query('INSERT INTO board_label (board_id, label_id) VALUES ($1::uuid, $2::uuid)', [uuidText(key), uuidText(on)]);
    return key;
  };
  /** A merge refused out_of_reach, naming table and no key of the row beyond the caller. */
  const beyond = async (as: FixtureMergeAccessDbOrm, from: string, into: string, table: string, hidden: string) => {
    let refused: unknown;
    try {
      await as.label.mergeInto({ from: from as never, into: into as never });
    } catch (error) {
      refused = error;
    }
    expect(refused).toBeInstanceOf(MergeError);
    expect((refused as MergeError).code).toBe('out_of_reach');
    expect((refused as MergeError).message).toContain(table);
    expect((refused as MergeError).message).not.toContain(hidden);
    expect((refused as MergeError).message).not.toContain(uuidText(hidden));
  };

  test('a user who may not write one of the two labels merges neither into the other, and writes nothing', async () => {
    const mine = await label('bug', alice, 'private');
    const theirs = await label('defect', bob, 'private');
    await note(theirs, bob, 'private');
    await expect(asAlice.label.mergeInto({ from: theirs as never, into: mine as never })).rejects.toBeInstanceOf(NotFoundError);
    await expect(asAlice.label.mergeInto({ from: mine as never, into: theirs as never })).rejects.toBeInstanceOf(NotFoundError);
    expect([await held(mine), await held(theirs), await notes(theirs), await notes(mine)]).toEqual([true, true, 1, 0]);
  });

  test('a user who may write both labels, and every row that references the one merged away, merges one into the other', async () => {
    const stays = await label('crash', alice, 'private');
    const goes = await label('fault', alice, 'private');
    await note(goes, alice, 'private');
    await note(goes, bob, 'everyone');
    await board(alice, goes);
    const merged = await asAlice.label.mergeInto({ from: goes as never, into: stays as never });
    expect(merged.repointed).toEqual({ 'Board.labels': 1, LabelNote: 2 });
    expect([await held(goes), await notes(stays), await pairs(stays)]).toEqual([false, 2, 1]);
  });

  test('a user who may not write a note on the label merged away merges neither, naming the notes, and writes nothing', async () => {
    const stays = await label('hang', alice, 'private');
    const goes = await label('freeze', alice, 'private');
    await note(goes, alice, 'private');
    const hidden = await note(goes, bob, 'private');
    await board(alice, goes);
    await beyond(asAlice, goes, stays, 'LabelNote', hidden);
    expect([await held(goes), await held(stays), await notes(goes), await notes(stays), await pairs(goes)]).toEqual([true, true, 2, 0, 1]);
  });

  test('a user who may not write a board that holds the label merged away merges neither, naming the boards, and writes nothing', async () => {
    const stays = await label('leak', alice, 'private');
    const goes = await label('drip', alice, 'private');
    await note(goes, alice, 'private');
    const hidden = await board(bob, goes);
    await beyond(asAlice, goes, stays, 'Board', hidden);
    expect([await held(goes), await notes(goes), await pairs(goes), await pairs(stays)]).toEqual([true, 1, 1, 0]);
  });

  test('a merge outside any operation repoints the rows no caller is held to', async () => {
    const stays = await label('slow', alice, 'private');
    const goes = await label('laggy', alice, 'private');
    await note(goes, bob, 'private');
    await board(bob, goes);
    const merged = await system.label.mergeInto({ from: goes as never, into: stays as never });
    expect(merged.repointed).toEqual({ 'Board.labels': 1, LabelNote: 1 });
    expect([await held(goes), await notes(stays), await pairs(stays)]).toEqual([false, 1, 1]);
  });

  test('a party merges two labels only when its grants reach the notes on the one merged away', async () => {
    const stays = await label('tidy', alice, 'everyone');
    const goes = await label('neat', alice, 'everyone');
    const hidden = await note(goes, alice, 'everyone');
    // Its grants reach both labels and no note.
    await beyond(asParty(grant('Label', stays), grant('Label', goes)), goes, stays, 'LabelNote', hidden);
    expect([await held(goes), await notes(goes)]).toEqual([true, 1]);
    // Its role's path from a note to its label reaches the notes on each.
    const notesOf = { LabelNote: ['label'] };
    const merged = await asParty(grant('Label', stays, notesOf), grant('Label', goes, notesOf)).label.mergeInto({ from: goes as never, into: stays as never });
    expect(merged.repointed).toEqual({ LabelNote: 1 });
    expect([await held(goes), await notes(stays)]).toEqual([false, 1]);
  });

  const brand = async (name: string, owner: string, audience: string) => (await system.brand.createOne({ name, owner, audience } as never)).id as string;
  const resolve = async (as: FixtureMergeAccessDbOrm, name: string) => ((await as.brand.name.resolve({ name })) as { row: { key: string; alias: boolean } | null }).row;

  test('resolve answers a caller nothing of a brand it may not read', async () => {
    const acme = await brand('Acme', alice, 'private');
    const globex = await brand('Globex', bob, 'everyone');
    expect(await resolve(asBob, 'acme')).toBeNull();
    expect(await resolve(asParty(grant('Brand', globex)), 'ACME')).toBeNull();
    expect((await resolve(asAlice, 'acme'))?.key).toBe(acme);
    expect((await resolve(system, 'acme'))?.key).toBe(acme);
    expect((await resolve(asBob, 'globex'))?.key).toBe(globex);
    expect(await resolve(asBob, 'nobody')).toBeNull();
  });

  test('a name another brand holds is refused naming nothing of that brand', async () => {
    const initech = await brand('Initech', alice, 'private');
    const hooli = await brand('Hooli', bob, 'private');
    for (const run of [asBob.brand.createOne({ name: 'INITECH', owner: bob, audience: 'private' } as never), asBob.brand.name.rename(hooli as never, { name: 'initech' })]) {
      let refused: any;
      try {
        await run;
      } catch (error) {
        refused = error;
      }
      const refusal = refused?.refusal ?? refused;
      expect(refusal?.details?.vetoCode).toBe('name_taken');
      expect(Object.keys(refusal.details.vetoDetails).sort()).toEqual(['name', 'normalized']);
      const text = `${refused.message} ${refusal.message} ${JSON.stringify(refusal.details)}`;
      expect(text).not.toContain(initech);
      expect(text).not.toContain(uuidText(initech));
    }
  });

  test('a user who may write both brands merges one into the other, its aliases and names with it', async () => {
    const stays = await brand('Pied Piper', alice, 'private');
    const goes = await brand('Piper Net', alice, 'private');
    await asAlice.brand.mergeInto({ from: goes as never, into: stays as never });
    const found = await resolve(asAlice, 'piper net');
    expect([found?.key, found?.alias]).toEqual([stays, true]);
    expect(await resolve(asBob, 'piper net')).toBeNull();
  });
});
