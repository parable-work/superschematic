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
//   - Members sign in with the identity runtime over the database's user
//     model, through Hyperdrive, its passwords hashed by the runtime's
//     portable argon2id (D92): carol, whom a server on Bun registered with
//     node:crypto's argon2, signs in with a cookie; the first member who
//     registers here, ada, is the administrator; bob waits for approval,
//     which ada gives; bob signs in with a cookie, and closes the books only
//     once ada grants him the administrator's role.
//   - The server sits on its site's origin (D91 with D92): the site's
//     Worker, the script the cloudflare extension embeds, forwards
//     /api/ts-ledger through its service binding to it, and the server's
//     identity config is the runtime's defaults, no trusted origin and no
//     cookie domain, and it answers no CORS. The page's config names the
//     server on the site's address; ada and carol sign in with a cookie
//     there, with Sec-Fetch-Site and with Origin alone, and get the
//     host-only __Host-session cookie; ada closes the books with it and
//     carol, who lacks ledger.close, may not; a sign-in and a cookie call
//     from another origin are refused, and so is the page's sign-in sent to
//     the server's own host.

/** A call with a session cookie, which answers its status, its envelope's data or its problem (RFC 9457), and the session cookie it sets. */
async function as(env, cookie, method, path, body) {
  const headers = {};
  if (cookie) headers.cookie = cookie;
  if (body !== undefined) headers['content-type'] = 'application/json';
  const response = await env.LEDGER.fetch(`https://ts-ledger/${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await response.text();
  const parsed = text === '' ? {} : JSON.parse(text);
  const set = response.headers.get('set-cookie');
  return { status: response.status, data: parsed.data, error: response.ok ? undefined : parsed, cookie: set ? set.split(';')[0] : undefined, setCookie: set };
}

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

    await members(env);
    await onTheSitesOrigin(env);

    console.log('the ledger under workerd: Postgres through Hyperdrive, nearest, an enqueue that commits with its transaction, and both workers drained at a firing');
  },
};

/** The identity runtime on Workers, over the user model through Hyperdrive (D92). */
async function members(env) {
  const expect = (r, status, what) => check(r.status === status, `${what} answered ${r.status} ${JSON.stringify(r.error ?? r.data)}, want ${status}`);

  // carol's hash is node:crypto's, which the server on Bun wrote: the
  // portable argon2id verifies it.
  const carol = await as(env, undefined, 'POST', 'api/auth/login', { login: 'carol@example.com', password: 'carol registered on bun', session: 'cookie' });
  expect(carol, 200, "carol's sign-in");
  check(carol.cookie?.startsWith('__Host-session=') && /; Secure/u.test(carol.setCookie) && /HttpOnly/iu.test(carol.setCookie), `carol's cookie is ${carol.setCookie}`);
  expect(await as(env, carol.cookie, 'POST', 'api/books/close'), 403, "carol's closing of the books");

  // The first member to register here is the administrator, signed in at once.
  const ada = await as(env, undefined, 'POST', 'api/auth/register', { login: 'ada@example.com', name: 'Ada', password: 'ada the administrator', session: 'cookie' });
  expect(ada, 200, "ada's registration");
  check(ada.data.pending === false && ada.cookie?.startsWith('__Host-session='), `ada registered as ${JSON.stringify(ada.data)} with cookie ${ada.setCookie}`);
  const me = await as(env, ada.cookie, 'GET', 'api/auth/me');
  expect(me, 200, "ada's me");
  check(me.data.user.login === 'ada@example.com', `ada's me is ${JSON.stringify(me.data)}`);

  // bob waits for approval, with no session, and cannot sign in yet.
  const bob = await as(env, undefined, 'POST', 'api/auth/register', { login: 'bob@example.com', name: 'Bob', password: 'bob waits his turn', session: 'cookie' });
  expect(bob, 202, "bob's registration");
  check(bob.data.pending === true && bob.cookie === undefined, `bob registered as ${JSON.stringify(bob.data)} with cookie ${bob.setCookie}`);
  const early = await as(env, undefined, 'POST', 'api/auth/login', { login: 'bob@example.com', password: 'bob waits his turn', session: 'cookie' });
  expect(early, 403, "bob's sign-in before approval");
  check(early.error?.code === 'pending_approval', `bob's early sign-in failed with ${JSON.stringify(early.error)}`);

  // ada, the administrator, closes the books and approves bob.
  const closed = await as(env, ada.cookie, 'POST', 'api/books/close');
  expect(closed, 200, "ada's closing of the books");
  check(closed.data.closed === true, `ada closed the books with ${JSON.stringify(closed.data)}`);
  expect(await as(env, undefined, 'POST', 'api/books/close'), 401, 'closing the books with no session');
  const waiting = await as(env, ada.cookie, 'GET', 'api/auth/admin/users?pending=true');
  expect(waiting, 200, 'the users waiting for approval');
  check(JSON.stringify(waiting.data).includes(bob.data.user.id), `the users waiting are ${JSON.stringify(waiting.data)}, want bob`);
  expect(await as(env, ada.cookie, 'POST', `api/auth/admin/users/${bob.data.user.id}/approve`), 200, "ada's approval of bob");

  // bob signs in with a cookie; a wrong password does not.
  expect(await as(env, undefined, 'POST', 'api/auth/login', { login: 'bob@example.com', password: 'not bobs password', session: 'cookie' }), 401, "bob's wrong password");
  const signedIn = await as(env, undefined, 'POST', 'api/auth/login', { login: 'bob@example.com', password: 'bob waits his turn', session: 'cookie' });
  expect(signedIn, 200, "bob's sign-in");
  check(signedIn.cookie?.startsWith('__Host-session='), `bob's cookie is ${signedIn.setCookie}`);
  const bobMe = await as(env, signedIn.cookie, 'GET', 'api/auth/me');
  expect(bobMe, 200, "bob's me");
  check(bobMe.data.user.login === 'bob@example.com', `bob's me is ${JSON.stringify(bobMe.data)}`);

  // A member holds no ledger.close until ada grants him the administrator's role.
  expect(await as(env, signedIn.cookie, 'POST', 'api/books/close'), 403, "bob's closing of the books as a member");
  const roles = await as(env, ada.cookie, 'GET', 'api/auth/admin/roles');
  expect(roles, 200, 'the roles');
  const admin = (Array.isArray(roles.data) ? roles.data : (roles.data.roles ?? roles.data.items ?? [])).find(r => r.name === 'admin');
  check(admin, `the roles are ${JSON.stringify(roles.data)}, want admin`);
  expect(await as(env, ada.cookie, 'PUT', `api/auth/admin/users/${bob.data.user.id}/roles/${admin.id}`), 200, "ada's grant of admin to bob");
  const bobCloses = await as(env, signedIn.cookie, 'POST', 'api/books/close');
  expect(bobCloses, 200, "bob's closing of the books as an administrator");

  // bob signs out, and his cookie signs nothing in.
  expect(await as(env, signedIn.cookie, 'POST', 'api/auth/logout'), 200, "bob's sign-out");
  expect(await as(env, signedIn.cookie, 'GET', 'api/auth/me'), 401, "bob's me after signing out");

  console.log("the ledger's members under workerd: a sign-in with a hash node:crypto wrote, the first member the administrator, an approval, cookie sessions and a permission-gated route");
}

/**
 * ts-ledger on its site's origin (D91), through the site's Worker, as a
 * browser on the site's page calls it: the cookie session needs no CORS,
 * no trusted origin and no cookie domain.
 */
async function onTheSitesOrigin(env) {
  const site = env.SITE_URL;
  const expect = (r, status, what) => check(r.status === status, `${what} answered ${r.status} ${JSON.stringify(r.error ?? r.data)}, want ${status}`);
  const noCORS = (r, what) =>
    check(
      r.headers.get('access-control-allow-origin') === null && r.headers.get('access-control-allow-credentials') === null,
      `${what} answered CORS for ${r.headers.get('access-control-allow-origin')}`
    );
  const sameOrigin = { origin: site, 'sec-fetch-site': 'same-origin' };
  const foreign = { origin: 'https://evil.example', 'sec-fetch-site': 'cross-site' };
  const viaSite = async (method, path, { cookie, headers = {}, body } = {}) => {
    const h = { ...headers };
    if (cookie) h.cookie = cookie;
    if (body !== undefined) h['content-type'] = 'application/json';
    const response = await env.SITE.fetch(
      new Request(`${site}${path}`, { method, headers: h, body: body === undefined ? undefined : JSON.stringify(body), redirect: 'manual' })
    );
    const text = await response.text();
    const parsed = text === '' ? {} : JSON.parse(text);
    const set = response.headers.get('set-cookie');
    return { status: response.status, headers: response.headers, parsed, data: parsed.data, error: response.ok ? undefined : parsed, cookie: set ? set.split(';')[0] : undefined, setCookie: set };
  };
  const hostOnlySession = (r, who) => {
    const attributes = (r.setCookie ?? '').split(';').map(a => a.trim().toLowerCase());
    check(
      r.cookie?.startsWith('__Host-session=') &&
        attributes.includes('secure') &&
        attributes.includes('path=/') &&
        attributes.includes('httponly') &&
        !attributes.some(a => a.startsWith('domain=')),
      `${who}'s session cookie is ${r.setCookie}, want __Host-session, Secure, HttpOnly, Path=/ and no Domain`
    );
  };

  // The page's config names ts-ledger on the site's own origin.
  const config = await viaSite('GET', '/__superschematic/config.json');
  expect(config, 200, "the site's config");
  check(config.parsed.apis?.['ts-ledger']?.url === `${site}/api/ts-ledger`, `the site's config is ${JSON.stringify(config.parsed)}, want ts-ledger at ${site}/api/ts-ledger`);

  // ada signs in with a cookie on the site's origin, and closes the books with it.
  const ada = await viaSite('POST', '/api/ts-ledger/api/auth/login', {
    headers: sameOrigin,
    body: { login: 'ada@example.com', password: 'ada the administrator', session: 'cookie' },
  });
  expect(ada, 200, "ada's sign-in on the site's origin");
  hostOnlySession(ada, 'ada');
  noCORS(ada, "ada's sign-in");
  const me = await viaSite('GET', '/api/ts-ledger/api/auth/me', { cookie: ada.cookie, headers: sameOrigin });
  expect(me, 200, "ada's me on the site's origin");
  check(me.data.user.login === 'ada@example.com', `ada's me on the site's origin is ${JSON.stringify(me.data)}`);
  const closed = await viaSite('POST', '/api/ts-ledger/api/books/close', { cookie: ada.cookie, headers: sameOrigin });
  expect(closed, 200, "ada's closing of the books on the site's origin");
  check(closed.data.closed === true, `ada closed the books on the site's origin with ${JSON.stringify(closed.data)}`);
  noCORS(closed, "ada's closing of the books");

  // carol signs in with the site's Origin alone, and holds no ledger.close.
  const carol = await viaSite('POST', '/api/ts-ledger/api/auth/login', {
    headers: { origin: site },
    body: { login: 'carol@example.com', password: 'carol registered on bun', session: 'cookie' },
  });
  expect(carol, 200, "carol's sign-in on the site's origin with Origin alone");
  hostOnlySession(carol, 'carol');
  expect(await viaSite('POST', '/api/ts-ledger/api/books/close', { cookie: carol.cookie, headers: { origin: site } }), 403, "carol's closing of the books on the site's origin");

  // Another origin's sign-in, and its call with ada's cookie, are refused.
  const evil = await viaSite('POST', '/api/ts-ledger/api/auth/login', {
    headers: foreign,
    body: { login: 'ada@example.com', password: 'ada the administrator', session: 'cookie' },
  });
  expect(evil, 403, 'a sign-in from another origin');
  check(evil.error?.code === 'cross_origin' && evil.cookie === undefined, `a sign-in from another origin failed with ${JSON.stringify(evil.error)} and cookie ${evil.setCookie}`);
  const evilClose = await viaSite('POST', '/api/ts-ledger/api/books/close', { cookie: ada.cookie, headers: foreign });
  expect(evilClose, 403, "another origin's closing of the books with ada's cookie");
  check(evilClose.error?.code === 'cross_origin', `another origin's call failed with ${JSON.stringify(evilClose.error)}`);

  // The page's sign-in sent to the server's own host is from another origin
  // there, which no trusted origin admits: the site's origin is what lets the
  // cookie session in.
  const direct = await env.LEDGER.fetch('https://ts-ledger/api/auth/login', {
    method: 'POST',
    headers: { 'content-type': 'application/json', origin: site, 'sec-fetch-site': 'cross-site' },
    body: JSON.stringify({ login: 'ada@example.com', password: 'ada the administrator', session: 'cookie' }),
  });
  check(direct.status === 403, `the page's sign-in at the server's own host answered ${direct.status} ${await direct.text()}, want 403`);

  console.log("ts-ledger on its site's origin under workerd: a cookie sign-in and a permission-gated call through the site's Worker, a host-only __Host-session, no CORS, and another origin refused");
}
