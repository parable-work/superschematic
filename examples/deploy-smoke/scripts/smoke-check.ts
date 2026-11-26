// The smoke's checks (D97), through the site's origin, as a browser on the
// site's page would make them: scripts/smoke.sh runs this once the stack
// is up, and it runs alone against a stack already deployed:
//
//   bun scripts/smoke-check.ts --base-url https://smoke.example.dev
//   bun scripts/smoke-check.ts --environment schemas/dist/stack/smoke-stack/Gcp/environment.json \
//     --outputs outputs.json
//
// --environment reads the site's public address from a resolved
// environment, its node outputs filled in from `stack outputs`.
//
// It needs nothing but Bun: fetch, with the session cookie kept by hand,
// since the gcp target without a domain serves the origin over HTTP, where
// a browser keeps no Secure cookie and the identity runtime sets one
// (D91). The administrator is SMOKE_ADMIN_LOGIN (admin@smoke.test unless
// set) with SMOKE_ADMIN_PASSWORD: registered as the database's first user
// on a fresh database, and signed in on one an earlier run left. Each run
// registers a member of its own, and names its line items with a prefix
// of its own, so a database earlier runs wrote to answers alike.
//
// It prints each check and how long it took, the sign-ins' and the
// drain's above all: on Cloudflare a sign-in hashes with the runtime's own
// argon2id, about 120 ms of CPU (D92), and the worker drains at the next
// Cron firing, within a minute. It exits 1 at the first check that fails.

import { parseArgs } from 'node:util';

const { values: flags } = parseArgs({
  options: {
    'base-url': { type: 'string' },
    environment: { type: 'string' },
    outputs: { type: 'string' },
    'ready-seconds': { type: 'string', default: '600' },
    'drain-seconds': { type: 'string', default: '240' },
  },
});

const start = performance.now();
const timings: [string, number][] = [];

function fail(message: string): never {
  console.error(`FAIL ${message}`);
  process.exit(1);
}

function ok(what: string, ms?: number) {
  console.log(ms === undefined ? `ok   ${what}` : `ok   ${what} (${Math.round(ms)} ms)`);
}

function sleep(ms: number) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

// The site's public address: given, or the resolved environment's site's,
// whose output references take their values from `stack outputs`.
async function baseURL(): Promise<string> {
  if (flags['base-url']) return flags['base-url'].replace(/\/+$/, '');
  if (!flags.environment) fail('give --base-url, or --environment with --outputs');
  const env = JSON.parse(await Bun.file(flags.environment).text());
  const site = env.deployables.find((d: { kind: string; name: string }) => d.kind === 'site' && d.name === 'smoke-web');
  if (!site?.publicAddress) fail(`${flags.environment} gives site smoke-web no public address`);
  const outputs = flags.outputs ? JSON.parse(await Bun.file(flags.outputs).text()).resources ?? {} : {};
  const value = (v: unknown): string => {
    if (typeof v === 'string') return v;
    if (Array.isArray(v)) return v.map(value).join('');
    const ref = v as { $concat?: unknown[]; $output?: { resource: string; name: string } };
    if (ref.$concat) return ref.$concat.map(value).join('');
    if (ref.$output) {
      const out = outputs[ref.$output.resource]?.[ref.$output.name];
      if (typeof out !== 'string') fail(`no output ${ref.$output.resource}.${ref.$output.name}; give --outputs, from stack outputs`);
      return out;
    }
    return fail(`site smoke-web's public address holds ${JSON.stringify(v)}`);
  };
  return value(site.publicAddress).replace(/\/+$/, '');
}

const base = await baseURL();
const origin = new URL(base).origin;
const run = `${Date.now().toString(36)}${Math.floor(Math.random() * 1296).toString(36)}`;
console.log(`smoke ${run} through ${base}`);

// A session: the cookies a sign-in set, sent back by hand. The identity
// runtime's is __Host-session, Secure, deployed, and session on stack
// dev's plain HTTP.
class Session {
  cookies = new Map<string, string>();
  set = '';

  get cookie(): string {
    return [...this.cookies].map(([name, value]) => `${name}=${value}`).join('; ');
  }

  // A request as the site's page sends it: on the page's origin, an unsafe
  // one with Origin alone, so the identity runtime admits it only when the
  // Host it sees is the site's, which proves the front passed it on (D91).
  async call(method: string, url: string, body?: unknown, headers: Record<string, string> = {}) {
    const init: RequestInit = { method, redirect: 'manual', headers: { Accept: 'application/json', ...headers } };
    const h = init.headers as Record<string, string>;
    if (this.cookie) h.Cookie = this.cookie;
    if (method !== 'GET' && method !== 'HEAD') h.Origin ??= origin;
    if (body !== undefined) {
      h['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    const began = performance.now();
    const response = await fetch(url, init);
    const ms = performance.now() - began;
    for (const set of response.headers.getSetCookie()) {
      const pair = set.split(';')[0] ?? '';
      const at = pair.indexOf('=');
      const name = pair.slice(0, at).trim();
      const value = pair.slice(at + 1).trim();
      if (at <= 0) continue;
      if (value === '' || /max-age=0/i.test(set)) this.cookies.delete(name);
      else {
        this.cookies.set(name, value);
        this.set = set.replace(value, '...');
      }
    }
    const text = await response.text();
    let json: any;
    try {
      json = text ? JSON.parse(text) : undefined;
    } catch {
      json = undefined;
    }
    return { status: response.status, json, text, ms, headers: response.headers };
  }
}

function expect(what: string, status: number, want: number | number[], text: string) {
  const wants = Array.isArray(want) ? want : [want];
  if (!wants.includes(status)) fail(`${what}: ${status}, want ${wants.join(' or ')}: ${text.slice(0, 400)}`);
}

// The site: its page, its fallback and its config, until the front serves
// them. A new load balancer answers 404 or 502 for minutes.
const anon = new Session();
const ready = Date.now() + Number(flags['ready-seconds']) * 1000;
let page = await anon.call('GET', `${base}/`).catch(error => ({ status: 0, text: String(error), ms: 0 }) as const);
while (page.status !== 200 || !page.text.includes('<main id="app">')) {
  if (Date.now() > ready) fail(`the site's page: ${page.status}: ${page.text.slice(0, 200)}`);
  await sleep(5000);
  page = await anon.call('GET', `${base}/`).catch(error => ({ status: 0, text: String(error), ms: 0 }) as const);
}
ok("the site's page");
const fallback = await anon.call('GET', `${base}/no/such/page`);
if (fallback.status !== 200 || !fallback.text.includes('<main id="app">')) fail(`a path of no file: ${fallback.status}, want the fallback page`);
ok("a path of no file serves the site's fallback");
const config = await anon.call('GET', `${base}/__superschematic/config.json`);
expect("the site's config", config.status, 200, config.text);
const apiURL: string | undefined = config.json?.apis?.['smoke-api']?.url;
if (apiURL !== `${base}/api/smoke-api`) fail(`the site's config puts smoke-api at ${apiURL}, want ${base}/api/smoke-api, on the site's origin`);
ok(`the site's config puts smoke-api on its origin, at ${apiURL}`);
const api = `${apiURL}/api`;

// smoke-api behind the site, once its backend answers: a call with no
// session is the API's 401, not the site's page.
let missing = await anon.call('GET', `${api}/auth/me`);
while (missing.status !== 401) {
  if (Date.now() > ready) fail(`smoke-api on the site's origin: ${missing.status}, want 401: ${missing.text.slice(0, 200)}`);
  await sleep(5000);
  missing = await anon.call('GET', `${api}/auth/me`);
}
if (missing.text.includes('<main id="app">')) fail("an API path answered with the site's page");
ok("smoke-api answers on the site's origin");

// The Host the API sees: a sign-in that sends Origin alone is a bad
// password (401) only when the identity runtime takes it as same-origin,
// which it does when the Host it sees is the site's.
const probe = { login: `nobody-${run}@smoke.test`, password: 'not-the-password', session: 'cookie' };
const hostCheck = await anon.call('POST', `${api}/auth/login`, probe);
if (hostCheck.status === 403) {
  const fetchSite = await anon.call('POST', `${api}/auth/login`, probe, { 'Sec-Fetch-Site': 'same-origin' });
  fail(
    `a sign-in that sends Origin ${origin} alone is refused as cross-origin (${hostCheck.text.slice(0, 200)}), so smoke-api sees another Host than the site's; ` +
      `with Sec-Fetch-Site: same-origin it answers ${fetchSite.status}`,
  );
}
expect('a sign-in with a bad password', hostCheck.status, 401, hostCheck.text);
ok("smoke-api sees the site's Host: a sign-in that sends Origin alone is same-origin");

// The administrator: the first registrant of a fresh database, or signed in.
const adminLogin = process.env.SMOKE_ADMIN_LOGIN || 'admin@smoke.test';
const adminPassword = process.env.SMOKE_ADMIN_PASSWORD || fail('set SMOKE_ADMIN_PASSWORD, the smoke administrator\'s password');
const admin = new Session();
const first = await admin.call('POST', `${api}/auth/register`, { login: adminLogin, password: adminPassword, name: 'Smoke Admin', session: 'cookie' });
if (first.status === 200) {
  ok(`registered ${adminLogin}, the first user and the administrator`, first.ms);
  timings.push(['register (first user)', first.ms]);
} else {
  expect(`register ${adminLogin}`, first.status, 409, first.text);
  const login = await admin.call('POST', `${api}/auth/login`, { login: adminLogin, password: adminPassword, session: 'cookie' });
  expect(`sign in ${adminLogin}`, login.status, 200, login.text);
  ok(`signed in ${adminLogin}, whom an earlier run registered`, login.ms);
  timings.push(['sign in (administrator)', login.ms]);
}
if (!admin.cookie) fail("the administrator's sign-in set no session cookie");

// A member of this run: registered, waiting, approved, signed in.
const memberLogin = `member-${run}@smoke.test`;
const memberPassword = `smoke-${run}-${crypto.randomUUID()}`;
const member = new Session();
const registered = await member.call('POST', `${api}/auth/register`, { login: memberLogin, password: memberPassword, name: `Member ${run}`, session: 'cookie' });
expect(`register ${memberLogin}`, registered.status, 202, registered.text);
const memberId: string = registered.json?.data?.user?.id;
if (!memberId || registered.json?.data?.pending !== true) fail(`register ${memberLogin} answered ${registered.text.slice(0, 200)}`);
ok(`registered ${memberLogin}, who waits for approval`, registered.ms);
timings.push(['register (member)', registered.ms]);
const waiting = await member.call('POST', `${api}/auth/login`, { login: memberLogin, password: memberPassword, session: 'cookie' });
expect('sign in before approval', waiting.status, 403, waiting.text);
ok('a sign-in before approval is refused');
const approved = await admin.call('POST', `${api}/auth/admin/users/${memberId}/approve`);
expect(`approve ${memberLogin}`, approved.status, 200, approved.text);
ok(`the administrator approved ${memberLogin}`, approved.ms);

const elsewhere = await member.call('POST', `${api}/auth/login`, { login: memberLogin, password: memberPassword, session: 'cookie' }, { Origin: 'https://elsewhere.example' });
expect('a sign-in from another origin', elsewhere.status, 403, elsewhere.text);
ok('a sign-in from another origin is refused');
for (let i = 1; i <= 3; i++) {
  const login = await member.call('POST', `${api}/auth/login`, { login: memberLogin, password: memberPassword, session: 'cookie' });
  expect(`sign in ${memberLogin}`, login.status, 200, login.text);
  if (!member.cookie) fail('the sign-in set no session cookie');
  ok(`signed in ${memberLogin} with a cookie on the site's origin, try ${i}`, login.ms);
  timings.push([`sign in (member, try ${i})`, login.ms]);
}
console.log(`     the cookie: ${member.set}`);
if (base.startsWith('https:') && !member.cookie.startsWith('__Host-session=')) fail('the sign-in over HTTPS set no __Host-session cookie');
const me = await member.call('GET', `${api}/auth/me`);
expect('auth/me with the cookie', me.status, 200, me.text);
if (me.json?.data?.user?.login !== memberLogin) fail(`auth/me answered ${me.text.slice(0, 200)}`);
ok('auth/me knows the member by the cookie', me.ms);
const unknown = await member.call('GET', `${api}/line-items/0000000000000000000000`);
expect('a line item that does not exist', unknown.status, 404, unknown.text);
if (unknown.text.includes('<main id="app">')) fail("the API's 404 answered with the site's page");
ok("the API's 404 reaches the page as the API's");

// Line items, the declared query and nearest.
const prefix = `smoke-${run}-`;
const items: Record<string, string> = {};
for (const [name, amount] of [
  ['coffee', -450],
  ['tea', -300],
  ['salary', 250000],
] as const) {
  const created = await member.call('POST', `${api}/line-items`, { name: prefix + name, amount });
  expect(`create ${prefix + name}`, created.status, [200, 201], created.text);
  items[name] = created.json?.data?.id ?? fail(`create ${prefix + name} answered ${created.text.slice(0, 200)}`);
}
ok(`created three line items under ${prefix}`);
const totals = await member.call('GET', `${api}/totals?prefix=${encodeURIComponent(prefix)}`);
expect('totals', totals.status, 200, totals.text);
const t = totals.json?.data;
if (t?.lines !== 3 || t?.spent !== -750 || t?.earned !== 250000) fail(`totals answered ${totals.text.slice(0, 200)}, want 3 lines, -750 spent and 250000 earned`);
ok("the declared query lineItemTotals adds this run's line items", totals.ms);
timings.push(['declared query', totals.ms]);
const near = await member.call('GET', `${api}/nearest?name=${encodeURIComponent(prefix + 'tea')}&k=3`);
expect('nearest', near.status, 200, near.text);
const [nearest] = near.json?.data ?? [];
if (nearest?.id !== items.tea || Math.abs(nearest?.distance) > 1e-6) fail(`nearest answered ${near.text.slice(0, 300)}, want ${prefix}tea first, at distance 0`);
ok(`nearest finds ${prefix}tea nearest its own name`, near.ms);
timings.push(['nearest', near.ms]);

// A message the worker drains.
const drain = await member.call('POST', `${api}/line-items/${items.tea}/drain`);
expect('request a drain', drain.status, [200, 202], drain.text);
const enqueued = performance.now();
const drainBy = Date.now() + Number(flags['drain-seconds']) * 1000;
for (;;) {
  const item = await member.call('GET', `${api}/line-items/${items.tea}`);
  expect(`read ${prefix}tea`, item.status, 200, item.text);
  if (item.json?.data?.drainedAt) break;
  if (Date.now() > drainBy) fail(`the worker did not drain ${prefix}tea within ${flags['drain-seconds']}s`);
  await sleep(1000);
}
const drained = performance.now() - enqueued;
ok(`the worker drained ${prefix}tea`, drained);
timings.push(['enqueue to drained', drained]);

// What a behavior worker moves (D151), and a message enqueued five seconds
// on. Each is polled until it moves, and the time from its write is
// printed: under stack dev a worker polls each second; on Cloudflare the
// write wakes the worker's Workflow, which runs at the message's time, so
// each moves within seconds of it, not at the next Cron firing. The change
// a Reaction hears wakes nothing and moves at the next firing.
async function until(what: string, path: string, moved: (data: Record<string, unknown>) => boolean): Promise<number> {
  const from = performance.now();
  const by = Date.now() + Number(flags['drain-seconds']) * 1000;
  for (;;) {
    const read = await member.call('GET', `${api}/${path}`);
    expect(`read ${what}`, read.status, 200, read.text);
    if (moved(read.json?.data ?? {})) return performance.now() - from;
    if (Date.now() > by) fail(`${what} did not move within ${flags['drain-seconds']}s: ${read.text.slice(0, 300)}`);
    await sleep(500);
  }
}

const later = await member.call('POST', `${api}/line-items/${items.coffee}/drain-later`);
expect('request a drain five seconds on', later.status, [200, 202], later.text);
const drainedLater = await until(`${prefix}coffee`, `line-items/${items.coffee}`, data => Boolean(data.drainedAt));
if (drainedLater < 4_000) fail(`the worker drained ${prefix}coffee ${Math.round(drainedLater)} ms after its delayed enqueue, before it was due`);
ok(`the worker drained ${prefix}coffee once its delay had passed`, drainedLater);
timings.push(['delayed enqueue to drained', drainedLater]);

const followUp = await member.call('POST', `${api}/follow-ups`, { title: `${prefix}follow-up` });
expect('create a follow-up', followUp.status, [200, 201], followUp.text);
const followUpDue = await until('the follow-up', `follow-ups/${followUp.json?.data?.id}`, data => data.status === 'due');
ok("the follow-up's Timers deadline fired", followUpDue);
timings.push(['deadline to fired', followUpDue]);

const checkIn = await member.call('POST', `${api}/check-ins`, { title: `${prefix}check-in` });
expect('create a check-in', checkIn.status, [200, 201], checkIn.text);
const checkedIn = await until('the check-in', `check-ins/${checkIn.json?.data?.id}`, data => Boolean(data.ranAt));
ok("the check-in's Schedules run reached its handler", checkedIn);
timings.push(['schedule to run handled', checkedIn]);

const alert = await member.call('POST', `${api}/alerts`);
expect('create an alert', alert.status, [200, 201], alert.text);
const high = await member.call('POST', `${api}/alerts/${alert.json?.data?.id}/high`);
expect('raise the alert', high.status, 200, high.text);
const raised = await until('the alert', `alerts/${alert.json?.data?.id}`, data => data.status === 'raised');
ok("the alert's Reactions rule ran on its change", raised);
timings.push(['change to reaction', raised]);

// A file uploaded straight to the bucket through the URL smoke-api signs,
// as a browser would (D154): on Cloudflare a PUT to R2's S3 API, under stack
// dev to the emulator. Its completion reads the bytes back and holds them
// to the SHA-256 the upload declared; filed onto another line item, the
// first line item is that one's backlink.
const fileBytes = new TextEncoder().encode(`smoke ${run}: a receipt\n`.repeat(64));
const fileSha = Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', fileBytes)), b => b.toString(16).padStart(2, '0')).join('');
const begun = await member.call('POST', `${api}/line-items/${items.tea}/files`, { name: 'receipt.txt', mediaType: 'text/plain', size: fileBytes.length, sha256: fileSha });
expect('begin a file upload', begun.status, [200, 201], begun.text);
const upload = begun.json?.data ?? fail(`begin a file upload answered ${begun.text.slice(0, 200)}`);
const putStart = performance.now();
const put = await fetch(upload.url, { method: 'PUT', headers: { 'content-type': upload.contentType }, body: fileBytes });
if (!put.ok) fail(`the PUT to the bucket's signed URL answered ${put.status}: ${(await put.text()).slice(0, 300)}`);
const putMs = performance.now() - putStart;
ok(`uploaded ${fileBytes.length} bytes straight to the bucket`, putMs);
timings.push(['direct upload', putMs]);
const completed = await member.call('POST', `${api}/line-items/${items.tea}/files/${upload.fileId}/complete`);
expect('complete the upload', completed.status, 200, completed.text);
if (completed.json?.data?.state !== 'ready' || completed.json?.data?.contentHash !== fileSha) fail(`complete the upload answered ${completed.text.slice(0, 300)}`);
ok("the upload's bytes are the ones it declared", completed.ms);
timings.push(['complete (read back, hash)', completed.ms]);
const filed = await member.call('POST', `${api}/line-items/${items.tea}/files/${upload.fileId}/file`, { lineItemId: items.coffee });
expect('file it onto another line item', filed.status, [200, 201], filed.text);
if (filed.json?.data?.objectKey !== completed.json?.data?.objectKey) fail(`the filed file names ${filed.json?.data?.objectKey}, want the same object`);
const backlinks = await member.call('GET', `${api}/line-items/${items.coffee}/backlinks`);
expect('backlinks', backlinks.status, 200, backlinks.text);
if (!(backlinks.json?.data ?? []).some((b: { lineItemId: string }) => b.lineItemId === items.tea)) fail(`${prefix}coffee's backlinks are ${backlinks.text.slice(0, 300)}, want ${prefix}tea`);
ok(`the file filed onto ${prefix}coffee with no second upload, which lists ${prefix}tea among its backlinks`);

const out = await member.call('POST', `${api}/auth/logout`);
expect('sign out', out.status, [200, 204], out.text);
const after = await member.call('GET', `${api}/auth/me`);
expect('auth/me after signing out', after.status, 401, after.text);
ok('signed out');

console.log('\ntimings');
for (const [what, ms] of timings) console.log(`  ${what.padEnd(28)} ${String(Math.round(ms)).padStart(7)} ms`);
console.log(`  ${'the whole check'.padEnd(28)} ${String(Math.round(performance.now() - start)).padStart(7)} ms`);
console.log(`smoke ${run}: ok`);
