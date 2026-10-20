// wishlist.capnp's D1 stand-in: what workerd binds as the wishlist's
// SHOP_WISHLIST_DB_DATABASE, a wrapped binding with D1's interface, the
// runtime's SqlDatabase (prepare(sql).bind(...) then first, all, run or
// raw; batch; exec). workerd runs no D1, so it answers the statements the
// wishlist's implementation sends, and /readyz's SELECT 1, over
// shop-wishlist-db's wishlist_item table in memory, binding ?N by number
// as D1 does, and refuses any other statement, as D1 refuses SQL it cannot
// run. It keeps the unique index over (shopper, sku) that the SQLite plan
// creates.

/** The rows of wishlist_item, by shopper and SKU. */
function table() {
  const rows = new Map();
  let clock = 0;
  const key = (shopper, sku) => `${shopper}\u0000${sku}`;
  return {
    select: shopper =>
      [...rows.values()]
        .filter(row => row.shopper === shopper)
        .sort((a, b) => (a.added_at < b.added_at ? -1 : a.added_at > b.added_at ? 1 : a.sku < b.sku ? -1 : 1))
        .map(({ sku, note, added_at }) => ({ sku, note, added_at })),
    upsert: (shopper, sku, note) => {
      const existing = rows.get(key(shopper, sku));
      if (existing) {
        existing.note = note;
        return 1;
      }
      // added_at as the SQLite plan's default writes it, an instant a
      // millisecond apart per row, so the order is the order kept.
      rows.set(key(shopper, sku), { shopper, sku, note, added_at: new Date(Date.UTC(2026, 9, 20) + clock++).toISOString() });
      return 1;
    },
    delete: (shopper, sku) => (rows.delete(key(shopper, sku)) ? 1 : 0),
  };
}

/** The statements the stand-in answers, each with what it does over the table. */
function answers(items) {
  return {
    'SELECT 1 AS one': () => ({ rows: [{ one: 1 }], changes: 0 }),
    'SELECT sku, note, added_at FROM wishlist_item WHERE shopper = ?1 ORDER BY added_at, sku': ([shopper]) => ({ rows: items.select(shopper), changes: 0 }),
    'INSERT INTO wishlist_item (shopper, sku, note) VALUES (?1, ?2, ?3) ON CONFLICT (shopper, sku) DO UPDATE SET note = excluded.note': ([shopper, sku, note]) => ({
      rows: [],
      changes: items.upsert(shopper, sku, note),
    }),
    'DELETE FROM wishlist_item WHERE shopper = ?1 AND sku = ?2': ([shopper, sku]) => ({ rows: [], changes: items.delete(shopper, sku) }),
  };
}

const value = v => (typeof v === 'boolean' ? (v ? 1 : 0) : v);

/** The binding: D1's interface over the table. */
export function d1() {
  const known = answers(table());
  const execute = (sql, params) => {
    const answer = known[sql];
    if (!answer) throw new Error(`D1_ERROR: the stand-in runs no ${sql}`);
    const { rows, changes } = answer(params);
    return { results: rows, success: true, meta: { duration: 0, changes, last_row_id: 0, changed_db: changes > 0 } };
  };
  const statement = (sql, params = []) => ({
    sql,
    params,
    bind: (...values) => statement(sql, values.map(value)),
    first: async column => {
      const [row] = execute(sql, params).results;
      if (row === undefined) return null;
      if (column === undefined) return row;
      if (!(column in row)) throw new Error(`D1_ERROR: the row has no column ${column}`);
      return row[column];
    },
    all: async () => execute(sql, params),
    run: async () => execute(sql, params),
    raw: async options => {
      const rows = execute(sql, params).results;
      const columns = rows.length > 0 ? Object.keys(rows[0]) : [];
      const values = rows.map(row => Object.values(row));
      return options?.columnNames ? [columns, ...values] : values;
    },
  });
  return {
    prepare: sql => statement(sql),
    batch: async statements => statements.map(({ sql, params }) => execute(sql, params)),
    exec: async script => {
      const lines = script.split('\n').filter(line => line.trim() !== '');
      for (const line of lines) execute(line, []);
      return { count: lines.length, duration: 0 };
    },
  };
}
