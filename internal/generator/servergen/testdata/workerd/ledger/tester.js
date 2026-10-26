// The tester of ledger.capnp (D92): ts-ledger's server, on Workers, reaches
// Postgres through its Hyperdrive binding, and each of its workers drains
// its queue from Postgres at a firing of its Cron Trigger.
//
//   - The server answers /readyz once Postgres answers through Hyperdrive,
//     and its repositories create categories and line items; LineItem's
//     trigger enqueues an embedding for each line item with a category,
//     one waiting message per line item, the latest.
//   - At a firing, EmbedLineItems drains that queue: each line item gets
//     its embedding, and nearest finds the line items of a category nearest
//     its vector, through the HNSW index.
//   - The server enqueues a sync in its transaction, and one in a
//     transaction that rolls back, which leaves no message; a second sync of
//     an item that waits adds none.
//   - At a firing, SyncItems handles two items at a time and one claim per
//     item: an item's second sync, which its first enqueued, starts after
//     the first ended, while another item's ran beside the first; a sync
//     that fails once is done on its retry, after the backoff, and one that
//     fails every time is dead once its retries are spent.

async function call(env, method, path, body) {
  const response = await env.LEDGER.fetch(`https://ts-ledger/${path}`, {
    method,
    headers: body === undefined ? {} : { 'content-type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  if (!response.ok) throw new Error(`${method} /${path}: ${response.status} ${text}`);
  const parsed = JSON.parse(text);
  return path === 'readyz' ? parsed : parsed.data;
}

async function state(env) {
  return JSON.parse((await call(env, 'GET', 'api/state')).json);
}

function check(condition, message) {
  if (!condition) throw new Error(message);
}

/** Fires a worker's scheduled handler, as its Cron Trigger fires it every minute. */
async function fire(worker) {
  await worker.scheduled({ cron: '* * * * *', scheduledTime: Date.now() });
}

export default {
  async test(_controller, env) {
    const ready = await call(env, 'GET', 'readyz');
    check(ready.status === 'ready', `readyz answered ${JSON.stringify(ready)}`);

    // The repositories, through Hyperdrive; the trigger enqueues.
    await call(env, 'POST', 'api/categories', { slug: 'groceries', name: 'Groceries' });
    await call(env, 'POST', 'api/categories', { slug: 'travel', name: 'Travel' });
    const items = {};
    for (const [name, category] of [
      ['apples', 'groceries'],
      ['bananas', 'groceries'],
      ['flight', 'travel'],
      ['pen', undefined],
    ]) {
      const item = await call(env, 'POST', 'api/line-items', category === undefined ? { name } : { name, category });
      check(item.name === name && item.category === category, `line item ${name} is ${JSON.stringify(item)}`);
      items[name] = item.id;
    }
    let s = await state(env);
    check(s.embedQueue.length === 3 && s.embedQueue.every(m => m.state === 'ready'), `the trigger enqueued ${JSON.stringify(s.embedQueue)}, want three ready`);
    await call(env, 'PUT', `api/line-items/${items.pen}/category`, { category: 'travel' });
    await call(env, 'PUT', `api/line-items/${items.flight}/category`, { category: 'groceries' });
    await call(env, 'PUT', `api/line-items/${items.flight}/category`, { category: 'travel' });
    s = await state(env);
    check(s.embedQueue.length === 4 && s.embedQueue.every(m => m.state === 'ready'), `after the changes the queue holds ${JSON.stringify(s.embedQueue)}, want one ready per line item`);

    // The embedding worker drains its queue at a firing.
    await fire(env.EMBED);
    s = await state(env);
    check(s.embeddings === 4, `the embedding worker wrote ${s.embeddings} embeddings, want 4`);
    check(s.embedQueue.every(m => m.state === 'done'), `the embedding queue holds ${JSON.stringify(s.embedQueue)} after the firing, want each done`);
    const near = await call(env, 'POST', 'api/neighbours', { vector: '1,0.1,0' });
    check(
      near.names.length === 4 && [...near.names.slice(0, 2)].sort().join() === 'apples,bananas' && [...near.names.slice(2)].sort().join() === 'flight,pen',
      `nearest found ${JSON.stringify(near.names)}, want the groceries first`
    );

    // Syncs enqueued in a transaction; one that rolls back leaves none.
    for (const [itemId, reason] of [
      ['a', 'chain'],
      ['b', 'ok'],
      ['c', 'flaky'],
      ['d', 'poison'],
      ['a', 'again'],
    ]) {
      const sync = await call(env, 'POST', 'api/syncs', { itemId, reason });
      check(sync.enqueued === true, `the sync of ${itemId} was not enqueued`);
    }
    const rolledBack = await call(env, 'POST', 'api/syncs', { itemId: 'e', reason: 'ok', rollback: true });
    check(rolledBack.enqueued === false, 'the rolled back sync says it was enqueued');
    s = await state(env);
    check(
      s.syncQueue.map(m => `${m.key}:${m.reason}:${m.state}`).join() === 'a:chain:ready,b:ok:ready,c:flaky:ready,d:poison:ready',
      `the sync queue holds ${JSON.stringify(s.syncQueue)}, want a, b, c and d ready, a's second skipped and e rolled back`
    );

    // The sync worker drains its queue at a firing.
    await fire(env.SYNC);
    s = await state(env);
    const byKey = Object.fromEntries(s.syncQueue.map(m => [`${m.key}:${m.reason}`, m]));
    check(byKey['a:chain']?.state === 'done' && byKey['a:chained']?.state === 'done' && byKey['b:ok']?.state === 'done', `the syncs of a and b are ${JSON.stringify(s.syncQueue)}, want done`);
    check(byKey['c:flaky']?.state === 'done' && byKey['c:flaky'].attempts === 2, `the flaky sync is ${JSON.stringify(byKey['c:flaky'])}, want done on its second attempt`);
    check(
      byKey['d:poison']?.state === 'dead' && byKey['d:poison'].attempts === 2 && byKey['d:poison'].last_error === 'poison every time',
      `the poison sync is ${JSON.stringify(byKey['d:poison'])}, want dead after two attempts with its error`
    );
    const run = (item, reason, attempt = 1) => s.runs.find(r => r.item_id === item && r.reason === reason && Number(r.attempt) === attempt);
    const chain = run('a', 'chain');
    const chained = run('a', 'chained');
    const b = run('b', 'ok');
    check(chain && chained && b && chain.ended !== null && chained.ended !== null && b.ended !== null, `the runs are ${JSON.stringify(s.runs)}`);
    check(Number(chained.started) >= Number(chain.ended), `a's second sync started at ${chained.started}, before its first ended at ${chain.ended}: two claims of one item`);
    check(Number(b.started) < Number(chain.ended) && Number(chain.started) < Number(b.ended), `b's sync ran ${b.started} to ${b.ended}, apart from a's ${chain.started} to ${chain.ended}: two items did not sync at once`);
    check(run('c', 'flaky', 2) && run('d', 'poison', 2), `the retries ran ${JSON.stringify(s.runs)}`);

    console.log('the ledger under workerd: Postgres through Hyperdrive, nearest, an enqueue that commits with its transaction, and both workers drained at a firing');
  },
};
