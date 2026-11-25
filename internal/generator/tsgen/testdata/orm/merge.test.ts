// fixture-merge-db's merges end to end (D147): labels and customers that
// turn out to be one. TestMergeIntoOnPostgres generates the package,
// type-checks it, applies the generated DDL and runs these cases on
// Postgres over the generated repository (createOrm). So the statements
// sqlutil writes from the schema's relations and unique indexes, the
// definition orm.ts embeds them in, and the runtime's mergeInto are held to
// one another: a merge repoints every relation that references the row
// merged away, a relation of the table to itself among them, stamping
// updatedAt and updatedBy where the referencing table keeps them; deletes
// the referencing row a unique index would refuse once repointed, a key
// over columns, a @unique relation and an index over an expression each,
// and stamps it deleted on a soft-deletable table whose index holds live
// rows alone; deletes the row merged away, softly on a table with
// deletedAt and through the behaviors' core on a table whose field is a
// Workflow; answers how many rows of each table it repointed and deleted;
// and refuses, writing nothing, a merge of a row into itself, of a row that
// is gone and into a row that references the one merged away.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { MergeError, NotFoundError } from '@superschematic/http-runtime/postgres';
import { createOrm, type FixtureMergeDbOrm } from './orm';
import { alice, aliceHyphenated, database, dsn, type TestDatabase } from './harness';

describe.skipIf(dsn === '')('Postgres', () => {
  let db: TestDatabase;
  let orm: FixtureMergeDbOrm;

  beforeAll(async () => {
    db = await database('ts_orm_merge');
    orm = createOrm(db.pool, alice);
  });

  afterAll(async () => {
    await db?.drop();
  });

  const count = async (text: string, values: unknown[] = []): Promise<number> => Number((await db.pool.query(text, values)).rows[0].n);
  const label = async (name: string): Promise<string> => (await orm.label.createOne({ name } as never)).id;
  const document = async (title: string): Promise<string> => (await orm.document.createOne({ title } as never)).id;
  const customer = async (name: string): Promise<string> => (await orm.customer.createOne({ name } as never)).id;
  const tag = async (documentId: string, labelId: string): Promise<void> => {
    await orm.documentLabel.createOne({ documentId, labelId } as never);
  };
  test('a merge repoints every relation that references the row and deletes it softly, on Postgres', async () => {
    const bug = await label('bug');
    const defect = await label('defect');
    const crash = await label('crash');
    await orm.label.updateOne(crash, { parentId: defect } as never);
    const one = await document('one');
    const two = await document('two');
    await tag(one, defect);
    await tag(two, defect);
    const first = await orm.labelNote.createOne({ labelId: defect, text: 'first' } as never);
    await orm.labelNote.createOne({ labelId: defect, text: 'second' } as never);
    await db.pool.query(`UPDATE label_note SET updated_at = now() - interval '1 hour', updated_by = NULL`);

    const result = await orm.label.mergeInto({ from: defect, into: bug });
    expect(result).toEqual({ repointed: { DocumentLabel: 2, Label: 1, LabelNote: 2 }, deleted: {} });
    expect((await orm.documentLabel.findMany({ labelId: { eq: bug } } as never)).rows.length).toBe(2);
    expect((await orm.label.getOne(crash)) as { parentId: string }).toMatchObject({ parentId: bug });
    // The notes say who repointed them and when.
    const note = (await orm.labelNote.getOne(first.id)) as { labelId: string; updatedBy: string; updatedAt: Date };
    expect(note.labelId).toBe(bug);
    expect(note.updatedBy).toBe(alice);
    expect(Date.now() - note.updatedAt.getTime()).toBeLessThan(10 * 60 * 1000);
    // defect is deleted softly, by alice, and no live row.
    await expect(orm.label.getOne(defect)).rejects.toThrow(NotFoundError);
    expect(await count(`SELECT count(*) AS n FROM label WHERE name = 'defect' AND deleted_at IS NOT NULL AND deleted_by = $1`, [aliceHyphenated])).toBe(1);
    expect((await orm.label.getOne(bug)) as { name: string }).toMatchObject({ name: 'bug' });
  });

  test('a row a unique index would refuse is deleted, and the rest repointed, on Postgres', async () => {
    const ui = await label('ui');
    const frontend = await label('frontend');
    const both = await document('both');
    const only = await document('only');
    await tag(both, ui);
    await tag(both, frontend);
    await tag(only, frontend);
    expect(await orm.label.mergeInto({ from: frontend, into: ui })).toEqual({ repointed: { DocumentLabel: 1 }, deleted: { DocumentLabel: 1 } });
    expect((await orm.documentLabel.findMany({ documentId: { eq: both } } as never)).rows.length).toBe(1);
    expect((await orm.documentLabel.findMany({ labelId: { eq: ui } } as never)).rows.length).toBe(2);
  });

  test('on a soft-deletable table the refused row is stamped deleted where the index holds live rows alone, on Postgres', async () => {
    const api = await label('api');
    const rest = await label('rest');
    const follow = (subscriber: string, labelId: string) => orm.subscription.createOne({ subscriber, labelId } as never);
    await follow('grace', api);
    const gracesOld = await follow('grace', rest);
    await follow('linus', rest);
    // alan left rest long ago and follows api: his old row is no live row,
    // so the index does not hold it.
    const alansOld = await follow('alan', rest);
    await orm.subscription.deleteOne(alansOld.id);
    await follow('alan', api);
    // grace's row of rest is stamped, then repointed with linus's and alan's old one.
    expect(await orm.label.mergeInto({ from: rest, into: api })).toEqual({ repointed: { Subscription: 3 }, deleted: { Subscription: 1 } });
    const live = (await orm.subscription.findMany({ labelId: { eq: api } } as never)).rows as { subscriber: string }[];
    expect(live.map(row => row.subscriber).sort()).toEqual(['alan', 'grace', 'linus']);
    await expect(orm.subscription.getOne(gracesOld.id)).rejects.toThrow(NotFoundError);
    expect(await count(`SELECT count(*) AS n FROM subscription WHERE subscriber = 'grace' AND deleted_at IS NOT NULL AND deleted_by = $1`, [aliceHyphenated])).toBe(1);
  });

  test("a unique index over an expression that reads the relation's column finds its collisions, on Postgres", async () => {
    const kept = await label('kept');
    const gone = await label('gone');
    await orm.labelCode.createOne({ labelId: kept, code: 'x1' } as never);
    await orm.labelCode.createOne({ labelId: gone, code: 'X1' } as never);
    await orm.labelCode.createOne({ labelId: gone, code: 'y2' } as never);
    expect(await orm.label.mergeInto({ from: gone, into: kept })).toEqual({ repointed: { LabelCode: 1 }, deleted: { LabelCode: 1 } });
    const codes = (await orm.labelCode.findMany({ labelId: { eq: kept } } as never)).rows as { code: string }[];
    expect(codes.map(row => row.code).sort()).toEqual(['x1', 'y2']);
  });

  test('a table that deletes for good loses the row merged away, through the core, on Postgres', async () => {
    const acme = await customer('Acme');
    const acmeInc = await customer('Acme Inc');
    await orm.customerProfile.createOne({ customerId: acme, bio: 'kept' } as never);
    await orm.customerProfile.createOne({ customerId: acmeInc, bio: 'dropped' } as never);
    await orm.purchase.createOne({ customerId: acmeInc, total: 10 } as never);
    await orm.purchase.createOne({ customerId: acmeInc, total: 20 } as never);
    expect(await orm.customer.mergeInto({ from: acmeInc, into: acme })).toEqual({ repointed: { Purchase: 2 }, deleted: { CustomerProfile: 1 } });
    await expect(orm.customer.getOne(acmeInc)).rejects.toThrow(NotFoundError);
    expect(await count(`SELECT count(*) AS n FROM customer WHERE name = 'Acme Inc'`)).toBe(0);
    const profiles = (await orm.customerProfile.findMany({ customerId: { eq: acme } } as never)).rows as { bio: string }[];
    expect(profiles.map(row => row.bio)).toEqual(['kept']);
    expect((await orm.purchase.findMany({ customerId: { eq: acme } } as never)).rows.length).toBe(2);
  });

  test('a merge of a row into itself, of a row that is gone, and into a row that references it is refused and writes nothing, on Postgres', async () => {
    const parent = await label('parent');
    const child = await label('child');
    await orm.label.updateOne(child, { parentId: parent } as never);
    const doc = await document('tagged');
    await tag(doc, parent);
    const refusal = async (merge: Promise<unknown>): Promise<unknown> => merge.then(() => undefined, (error: unknown) => error);
    expect(await refusal(orm.label.mergeInto({ from: parent, into: parent }))).toMatchObject({ name: 'MergeError', code: 'self' });
    // child sits under parent: merged into child, parent's rows would make child its own parent.
    const selfReference = await refusal(orm.label.mergeInto({ from: parent, into: child }));
    expect(selfReference).toBeInstanceOf(MergeError);
    expect(selfReference).toMatchObject({ code: 'self_reference' });
    const missing = await label('missing');
    await orm.label.hardDeleteOne(missing);
    await expect(orm.label.mergeInto({ from: missing, into: parent })).rejects.toThrow(NotFoundError);
    await expect(orm.label.mergeInto({ from: parent, into: missing })).rejects.toThrow(NotFoundError);
    // A label deleted softly is no live row to merge.
    const deleted = await label('deleted');
    await orm.label.deleteOne(deleted);
    await expect(orm.label.mergeInto({ from: deleted, into: parent })).rejects.toThrow(NotFoundError);
    expect((await orm.documentLabel.findMany({ labelId: { eq: parent } } as never)).rows.length).toBe(1);
    expect((await orm.label.getOne(parent)) as { name: string }).toMatchObject({ name: 'parent' });
  });

  test('a merge in a transaction rolls back with it, on Postgres', async () => {
    const stays = await label('stays');
    const goes = await label('goes');
    const doc = await document('undone');
    await tag(doc, goes);
    const abort = new Error('abort');
    await expect(
      orm.transaction(async tx => {
        expect(await tx.label.mergeInto({ from: goes, into: stays })).toEqual({ repointed: { DocumentLabel: 1 }, deleted: {} });
        throw abort;
      })
    ).rejects.toBe(abort);
    expect((await orm.documentLabel.findMany({ labelId: { eq: goes } } as never)).rows.length).toBe(1);
    expect((await orm.label.getOne(goes)) as { name: string }).toMatchObject({ name: 'goes' });

    // A merge refused in a transaction writes nothing there, and the
    // transaction goes on: goes sits under stays, so stays cannot fold
    // into it, though the document's label is repointed before the
    // relation of the table to itself is read.
    await tag(doc, stays).catch(() => undefined);
    const other = await document('kept');
    await tag(other, stays);
    await orm.label.updateOne(goes, { parentId: stays } as never);
    await orm.transaction(async tx => {
      await expect(tx.label.mergeInto({ from: stays, into: goes })).rejects.toThrow(MergeError);
      await tx.document.updateOne(other, { title: 'still kept' } as never);
    });
    expect((await orm.documentLabel.findMany({ documentId: { eq: other }, labelId: { eq: stays } } as never)).rows.length).toBe(1);
    expect((await orm.document.getOne(other)) as { title: string }).toMatchObject({ title: 'still kept' });
  });
});
