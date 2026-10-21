// cron-d1.capnp's D1 stand-in: what workerd binds as ts-notes'
// TS_NOTES_DB_DATABASE, a wrapped binding with D1's interface, the
// runtime's SqlDatabase. workerd runs no D1, so it answers the one
// statement PruneNotes sends, deleting as many empty notes as its inner
// binding says the table holds, and refuses any other, as D1 refuses SQL
// it cannot run.

const PRUNE = "DELETE FROM note WHERE text = ''";

/** The binding: D1's interface over a table holding env.empty empty notes. */
export function d1(env) {
  let empty = Number(env.empty);
  const execute = sql => {
    if (sql !== PRUNE) throw new Error(`D1_ERROR: the stand-in runs no ${sql}`);
    const changes = empty;
    empty = 0;
    return { results: [], success: true, meta: { duration: 0, changes, last_row_id: 0, changed_db: changes > 0 } };
  };
  const statement = sql => ({
    bind: () => statement(sql),
    first: async () => execute(sql).results[0] ?? null,
    all: async () => execute(sql),
    run: async () => execute(sql),
    raw: async () => [],
  });
  return {
    prepare: sql => statement(sql),
    batch: async statements => statements.map(s => s.run()),
    exec: async () => {
      throw new Error('D1_ERROR: the stand-in runs no script');
    },
  };
}
