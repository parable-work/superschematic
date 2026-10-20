// Stand-ins for the bindings workerd does not provide on its own, which
// the storefront fixture's config.capnp binds as wrapped bindings: workerd
// calls each entrypoint with the binding's inner bindings and binds what it
// returns. They hold what the fixture reads, in memory, with the methods of
// D1, R2 and a Secrets Store secret that the runtime calls, and no more.

/** A D1 database answering the fixture's statements over one cart. */
export function d1() {
  const carts = new Map([['ada@example.com', 1250]]);
  const answers = {
    'SELECT 1 AS one': () => [{ one: 1 }],
    'SELECT cents FROM cart WHERE email = ?1': ([email]) => (carts.has(email) ? [{ cents: carts.get(email) }] : []),
  };
  const statement = (sql, values = []) => {
    const rows = () => {
      const answer = answers[sql];
      if (!answer) throw new Error(`D1_ERROR: the stand-in answers no ${sql}`);
      return answer(values);
    };
    return {
      bind: (...bound) => statement(sql, bound),
      first: async column => {
        const row = rows()[0];
        if (row === undefined) return null;
        return column === undefined ? row : row[column];
      },
      all: async () => ({ results: rows(), success: true, meta: { duration: 0, changes: 0, last_row_id: 0, changed_db: false } }),
      run: async () => ({ results: rows(), success: true, meta: { duration: 0, changes: 0, last_row_id: 0, changed_db: false } }),
      raw: async () => rows().map(row => Object.values(row)),
    };
  };
  return {
    prepare: sql => statement(sql),
    batch: async statements => Promise.all(statements.map(s => s.all())),
    exec: async () => ({ count: 0, duration: 0 }),
  };
}

/** An R2 bucket over a map. */
export function r2() {
  const objects = new Map();
  const meta = key => ({ key, size: objects.get(key).length, uploaded: new Date(0) });
  return {
    put: async (key, value) => {
      objects.set(key, typeof value === 'string' ? new TextEncoder().encode(value) : new Uint8Array(value));
      return meta(key);
    },
    get: async key => (objects.has(key) ? { ...meta(key), body: new Response(objects.get(key)).body } : null),
    head: async key => (objects.has(key) ? meta(key) : null),
    delete: async key => {
      objects.delete(key);
    },
    list: async () => ({ objects: [...objects.keys()].sort().map(meta), truncated: false }),
  };
}

/** A Secrets Store secret whose value is its inner binding value. */
export function secret(env) {
  return { get: async () => env.value };
}
