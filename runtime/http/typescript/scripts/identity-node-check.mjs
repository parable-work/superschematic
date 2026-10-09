// Under Node: import the built identity entry point and check that it runs
// there, with no Bun API. The vectors run under bun (src/identity); this
// runs the sections that reach Node's own modules (argon2 in node:crypto,
// superscalar's binding) again under Node, then signs a user in and out
// through the Hono handlers over a node:sqlite store. A resolve hook
// refuses pg and the SQLite modules, so an import of one anywhere in the
// package's graph fails the script; the main entry point must not load the
// identity code at all.
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { registerHooks } from 'node:module';
import { fileURLToPath } from 'node:url';

const sqliteModules = new Set(['node:sqlite', 'bun:sqlite']);
let sqliteAllowed = false;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === 'pg' || specifier.startsWith('pg/')) throw new Error(`the package imported ${specifier}`);
    if (sqliteModules.has(specifier) && !sqliteAllowed) throw new Error(`the package imported ${specifier}`);
    return nextResolve(specifier, context);
  },
});

const dist = new URL('../dist/', import.meta.url);
for (const file of readdirSync(dist).filter(name => name.endsWith('.js'))) {
  assert.doesNotMatch(readFileSync(new URL(file, dist), 'utf8'), /from '\.\/identity\//u, `dist/${file} imports the identity entry point`);
}
await import('../dist/index.js');
await import('../dist/hono.js');

const identity = await import('../dist/identity/index.js');
const { Hono } = await import('hono');

const corpus = JSON.parse(readFileSync(fileURLToPath(new URL('../../testdata/identity_parity.json', import.meta.url)), 'utf8'));
for (const c of corpus.hashes) {
  assert.equal(await identity.hashPasswordWithSalt(c.password, new Uint8Array(Buffer.from(c.salt, 'base64')), c.params), c.phc, c.name);
}
for (const c of corpus.verify) {
  assert.deepEqual(await identity.verifyPassword(c.phc, c.password, c.current), c.want, c.name);
}
for (const c of corpus.passwordRule) assert.equal(identity.isPassword(c.password), c.valid, c.name);
for (const c of corpus.tokens) {
  assert.equal(identity.newToken(() => new Uint8Array(Buffer.from(c.bytes, 'hex'))), c.token, c.name);
  assert.equal(identity.hashToken(c.token), c.hash, c.name);
}
for (const c of corpus.config) {
  if (c.want === null) assert.throws(() => identity.parseIdentityConfig(c.input), c.name);
  else assert.deepEqual(identity.parseIdentityConfig(c.input), c.want, c.name);
}

sqliteAllowed = true;
const { DatabaseSync } = await import('node:sqlite');
const db = new DatabaseSync(':memory:');
const client = identity.nodeSqlite(db);
const fixture = new URL('../../testdata/identity/', import.meta.url);
client.exec('PRAGMA foreign_keys = ON');
client.exec(readFileSync(new URL('sqlite/create.sql', fixture), 'utf8'));
const descriptor = JSON.parse(readFileSync(new URL('fixture-user-model-db.json', fixture), 'utf8'));
// The members the descriptor gained after a fixture that lacks them.
descriptor.user.nameScalar ??= 'Identity.Name';
descriptor.role.keyScalar ??= 'Identity.UUID';
const store = identity.sqliteIdentityStore(client, descriptor);
const service = new identity.IdentityService({ store, config: { password: { argon2: { memoryKiB: 64, iterations: 1, parallelism: 1 } } } });
const app = new Hono();
identity.mountIdentityRoutes(app, service, { sessions: { register: true } });

const json = { 'content-type': 'application/json' };
const registered = await app.request('/auth/register', { method: 'POST', headers: json, body: JSON.stringify({ login: 'Ada@Example.com', password: 'a long password' }) });
assert.equal(registered.status, 200);
const login = await app.request('/auth/login', { method: 'POST', headers: json, body: JSON.stringify({ login: 'ada@example.com', password: 'a long password', session: 'cookie' }) });
assert.equal(login.status, 200);
const cookie = login.headers.getSetCookie()[0].split(';')[0];
const me = await app.request('/auth/me', { headers: { cookie } });
assert.equal(me.status, 200);
assert.equal((await me.json()).data.user.login, 'ada@example.com');
const out = await app.request('/auth/logout', { method: 'POST', headers: { cookie } });
assert.equal(out.status, 200);
assert.equal((await out.json()).data, true);
assert.equal((await app.request('/auth/me', { headers: { cookie } })).status, 401);
db.close();
console.log('identity: the built entry point runs under Node', process.version);
