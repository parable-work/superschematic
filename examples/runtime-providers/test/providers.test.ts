// The notes app's runtime providers end to end (D159), on Node.js and on
// Bun, with no change to core and no real service: each provider's service
// is a stub behind a fetch the test gives, keyed by host.
//
// - Every package's package.json manifest is its definition's, and its
//   provider has the operations the manifest lists.
// - The same sendNotice and receiveReplies run against two environments'
//   configs: sms-relay-a in one and sms-relay-b in the other. Swapping is
//   the config alone.
// - The vault broker lists titles and sites, resolves an item into the
//   runner's browser fill, refuses a vault outside its scope and an item
//   for another site, and no value reaches a log, the run record or the
//   caller.
// - The local sandbox runs and cancels processes, the stub model calls a
//   tool, and the file processor extracts text and makes previews from the
//   fixtures.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { inspect } from 'node:util';
import { test } from 'node:test';

import { createLogger } from '@superschematic/http-runtime';
import { currentAccess } from '@superschematic/http-runtime/hono';
import {
  checkProviderPackage,
  providersFrom,
  SecretValue,
  useCredential,
  type CredentialUse,
  type ProviderDefinition,
} from '@superschematic/http-runtime/providers';

import { notesProviders, receiveReplies, sendNotice } from '../src/app.ts';
import { localSandbox, settled } from '../providers/local-sandbox/src/index.ts';
import { mailRelay } from '../providers/mail-relay/src/index.ts';
import { smsRelayA } from '../providers/sms-relay-a/src/index.ts';
import { smsRelayB } from '../providers/sms-relay-b/src/index.ts';
import { stubModel } from '../providers/stub-model/src/index.ts';
import { readPgm, textPreviewProcessor } from '../providers/text-preview/src/index.ts';
import { vaultBroker } from '../providers/vault-broker/src/index.ts';

const here = (path: string) => new URL(path, import.meta.url);
const VAULT_PASSWORD = 'vault-item-password-7f3a';
const RELAY_A_TOKEN = 'relay-a-token-91c2';
const RELAY_B_TOKEN = 'relay-b-token-4d8e';

interface Received {
  readonly host: string;
  readonly method: string;
  readonly path: string;
  readonly headers: Record<string, string>;
  readonly body: string;
}

/** The stub services: relay-a.test, relay-b.test and vault.test, each answering as its kind of service does. */
function stubServices() {
  const received: Received[] = [];
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
  const fake = (async (input: string | URL | Request, init: RequestInit = {}) => {
    const url = new URL(String(input));
    const headers = Object.fromEntries(new Headers(init.headers).entries());
    const call: Received = { host: url.host, method: init.method ?? 'GET', path: url.pathname, headers, body: typeof init.body === 'string' ? init.body : '' };
    received.push(call);
    switch (url.host) {
      case 'relay-a.test':
        if (headers.authorization !== `Bearer ${RELAY_A_TOKEN}`) return json({ error: 'unauthorized' }, 401);
        if (url.pathname === '/v1/health') return json({ ok: true });
        if (url.pathname === '/v1/messages') return json({ id: 'a-msg-1', status: 'accepted' });
        break;
      case 'relay-b.test':
        if (headers.authorization !== `Basic ${btoa(`AC100:${RELAY_B_TOKEN}`)}`) return json({ error: 'unauthorized' }, 401);
        if (url.pathname === '/accounts/AC100') return json({ status: 'active' });
        if (url.pathname === '/accounts/AC100/send') return json({ sid: 'b-sm-1' });
        break;
      case 'vault.test': {
        if (headers.authorization !== 'Bearer vault-service-token') return json({ error: 'unauthorized' }, 401);
        const items: Record<string, { id: string; title: string; urls: string[]; fields: { label: string; value: string }[] }[]> = {
          ops: [{ id: 'reports-login', title: 'Reports portal', urls: ['https://reports.example.test'], fields: [{ label: 'password', value: VAULT_PASSWORD }] }],
          personal: [{ id: 'mail', title: 'Mail', urls: ['https://mail.example.test'], fields: [{ label: 'password', value: 'not-for-runs' }] }],
        };
        const list = /^\/v1\/vaults\/([^/]+)\/items$/u.exec(url.pathname);
        if (list) return json((items[list[1]!] ?? []).map(({ id, title, urls }) => ({ id, title, urls })));
        const one = /^\/v1\/vaults\/([^/]+)\/items\/([^/]+)$/u.exec(url.pathname);
        const item = one && items[one[1]!]?.find(entry => entry.id === one[2]);
        if (item) return json(item);
        return json({ error: 'not found' }, 404);
      }
    }
    return json({ error: 'not found' }, 404);
  }) as typeof fetch;
  return { fetch: fake, received };
}

/** A logger whose lines the test reads. */
function capturedLog() {
  const lines: string[] = [];
  return { log: createLogger({ service: 'notes-api' }, { write: line => lines.push(line) }), lines };
}

async function hmacHex(key: string, body: string): Promise<string> {
  const encoder = new TextEncoder();
  const cryptoKey = await crypto.subtle.importKey('raw', encoder.encode(key), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  return [...new Uint8Array(await crypto.subtle.sign('HMAC', cryptoKey, encoder.encode(body)))].map(b => b.toString(16).padStart(2, '0')).join('');
}

test('each provider package declares in its package.json what its definition and its provider are', async () => {
  const config: Record<string, unknown> = {
    SMS_URL: 'http://relay.test',
    SMS_ACCOUNT: 'AC100',
    SMS_TOKEN: new SecretValue('t'),
    VAULT_URL: 'http://vault.test',
    VAULT_TOKEN: new SecretValue('t'),
    VAULT_VAULTS: 'ops',
  };
  const cases: [string, ProviderDefinition<any>, string][] = [
    ['sms-relay-a', smsRelayA(), 'SMS'],
    ['sms-relay-b', smsRelayB(), 'SMS'],
    ['vault-broker', vaultBroker(), 'VAULT'],
    ['local-sandbox', localSandbox, 'RUNNER'],
    ['stub-model', stubModel, 'MODEL'],
    ['text-preview', textPreviewProcessor, 'PREVIEWS'],
  ];
  for (const [dir, definition, slot] of cases) {
    const pkg = JSON.parse(readFileSync(here(`../providers/${dir}/package.json`), 'utf8')) as unknown;
    const providers = notesProviders({ ...config, [slot]: definition.name }, { log: capturedLog().log });
    const instance = await providers.get(slot, definition.implements);
    checkProviderPackage(pkg, definition, instance);
  }
  // The mail relay, which examples/notifications sends email through (D153).
  const mailPkg = JSON.parse(readFileSync(here('../providers/mail-relay/package.json'), 'utf8')) as unknown;
  const mail = providersFrom({
    slots: { MAIL: 'channel' },
    config: { MAIL: 'mail-relay', MAIL_URL: 'http://mail.test', MAIL_TOKEN: new SecretValue('t'), MAIL_FROM: 'notices@example.test' },
    definitions: [mailRelay()],
    log: capturedLog().log,
  });
  checkProviderPackage(mailPkg, mailRelay(), await mail.channel('MAIL'));
});

test('one notice goes out through either SMS relay, and only the config differs', async () => {
  const environments = {
    // Dev, in notes-stack: sms-relay-a.
    Dev: { SMS: 'sms-relay-a', SMS_URL: 'http://relay-a.test', SMS_TOKEN: new SecretValue(RELAY_A_TOKEN) },
    // Cloudflare, in notes-stack: sms-relay-b, which reads an account as well.
    Cloudflare: { SMS: 'sms-relay-b', SMS_URL: 'http://relay-b.test', SMS_ACCOUNT: 'AC100', SMS_TOKEN: new SecretValue(RELAY_B_TOKEN) },
  };
  const results: Record<string, unknown> = {};
  for (const [name, config] of Object.entries(environments)) {
    const services = stubServices();
    const { log, lines } = capturedLog();
    const providers = notesProviders(config, { fetch: services.fetch, log });
    assert.deepEqual(providers.configured('channel').map(p => p.provider), [config.SMS]);
    results[name] = await sendNotice(providers, { to: '+15550100', body: 'Review moved to Thursday', reference: '142' });
    const sends = services.received.filter(call => call.method === 'POST');
    assert.equal(sends.length, 1, `${name} sends once`);
    assert.match(sends[0]!.body, /Review(\+|%20| )moved/u);
    const printed = lines.join('\n');
    assert.ok(!printed.includes(RELAY_A_TOKEN) && !printed.includes(RELAY_B_TOKEN), `${name}'s log holds no token`);
  }
  assert.deepEqual(results, {
    Dev: { providerMessageId: 'a-msg-1', state: 'queued' },
    Cloudflare: { providerMessageId: 'b-sm-1', state: 'sent' },
  });
});

test('a relay that posts replies to a webhook is read as the adapter\'s own principal, and a forged one is refused', async () => {
  const services = stubServices();
  const config = { SMS: 'sms-relay-a', SMS_URL: 'http://relay-a.test', SMS_TOKEN: new SecretValue(RELAY_A_TOKEN) };
  const providers = notesProviders(config, { fetch: services.fetch, log: capturedLog().log });
  const body = JSON.stringify({ messages: [{ id: 'in-1', from: '+15550100', text: 'done 142', at: '2026-11-25T12:00:00Z', replyTo: 'a-msg-1' }] });
  const replies = await receiveReplies(providers, { headers: { 'x-relay-signature': await hmacHex(RELAY_A_TOKEN, body) }, body });
  assert.deepEqual(replies, [{ providerMessageId: 'in-1', from: '+15550100', body: 'done 142', receivedAt: new Date('2026-11-25T12:00:00Z'), inReplyTo: 'a-msg-1' }]);
  await assert.rejects(receiveReplies(providers, { headers: { 'x-relay-signature': 'forged' }, body }), /not signed/u);
  assert.deepEqual(providers.principalOf('SMS'), { subject: 'extension:sms-relay-a', kind: 'extension', permissions: ['messages.relay'] });

  // sms-relay-b takes no replies: the same call reads none.
  const other = notesProviders({ SMS: 'sms-relay-b', SMS_URL: 'http://relay-b.test', SMS_ACCOUNT: 'AC100', SMS_TOKEN: new SecretValue(RELAY_B_TOKEN) }, { fetch: services.fetch, log: capturedLog().log });
  assert.deepEqual(await receiveReplies(other, { headers: {}, body: '{}' }), []);
});

test('the vault broker lists titles and sites, fills a browser field in the runner, and no value leaves it', async () => {
  const services = stubServices();
  const { log, lines } = capturedLog();
  const config = { VAULT: 'vault-broker', VAULT_URL: 'http://vault.test', VAULT_TOKEN: new SecretValue('vault-service-token'), VAULT_VAULTS: 'ops' };
  const providers = notesProviders(config, { fetch: services.fetch, log });
  const vault = await providers.credentials('VAULT');

  const listings = await vault.list({ site: 'https://reports.example.test' });
  assert.deepEqual(listings, [{ ref: 'vault://ops/reports-login/password', title: 'Reports portal', sites: ['https://reports.example.test'] }]);
  assert.ok(!services.received.some(call => call.path.includes('personal')), 'the broker reads no vault outside its scope');

  const filled = new Map<string, string>();
  const runRecord: CredentialUse[] = [];
  const use = await useCredential({
    provider: vault,
    providerName: providers.configured('credentials')[0]!.provider,
    ref: 'vault://ops/reports-login/password',
    target: { kind: 'fill', site: 'https://reports.example.test', selector: '#password' },
    runner: { fill: (selector, value) => void filled.set(selector, value), setHeader: () => {} },
    record: entry => void runRecord.push(entry),
  });
  assert.equal(filled.get('#password'), VAULT_PASSWORD, 'the runner fills the field');
  assert.equal(use.ref, 'vault://ops/reports-login/password');
  assert.ok(use.usedAt instanceof Date);

  await assert.rejects(vault.resolve('vault://personal/mail/password', { kind: 'fill', site: 'https://mail.example.test', selector: '#password' }), /not one this broker may read/u);
  await assert.rejects(vault.resolve('vault://ops/reports-login/password', { kind: 'fill', site: 'https://elsewhere.example.test', selector: '#password' }), /is not for/u);

  const injection = await vault.resolve('vault://ops/reports-login/password', { kind: 'header', site: 'https://reports.example.test', name: 'X-Api-Key' });
  log.info('step done', { use, listings, injection, runRecord });
  const printed = [lines.join('\n'), JSON.stringify(runRecord), JSON.stringify(use), JSON.stringify(listings), JSON.stringify(injection), inspect(injection), String(injection)].join('\n');
  assert.ok(!printed.includes(VAULT_PASSWORD), 'no value reaches a log, the run record or the caller');
});

test('the local sandbox runs an instance with its secrets, reports its status and cost, and cancels one', async () => {
  const { log, lines } = capturedLog();
  const providers = notesProviders({ RUNNER: 'local-sandbox' }, { log });
  const sandbox = await providers.sandbox('RUNNER');
  const node = process.execPath;
  const done = await sandbox.start({
    runId: 'run-1',
    command: [node, '-e', 'process.exit(process.env.RUN_TOKEN === "run-token-secret" && process.env.MCP_URL === "http://api.test/mcp" ? 0 : 3)'],
    env: { MCP_URL: 'http://api.test/mcp' },
    secrets: { RUN_TOKEN: new SecretValue('run-token-secret') },
  });
  await settled(sandbox, done.id);
  assert.deepEqual(await sandbox.status(done.id), { id: done.id, state: 'succeeded', exitCode: 0 });
  assert.ok((await sandbox.cost(done.id)).wallSeconds >= 0);

  const long = await sandbox.start({ runId: 'run-2', command: [node, '-e', 'setTimeout(() => {}, 60000)'] });
  assert.equal((await sandbox.status(long.id)).state, 'running');
  await sandbox.cancel(long.id);
  assert.equal((await sandbox.status(long.id)).state, 'cancelled');
  assert.ok(!lines.join('\n').includes('run-token-secret'), 'the instance\'s secrets are not logged');
});

test('the stub model calls a tool it is offered, reports usage, and does not obey untrusted content', async () => {
  const providers = notesProviders({ MODEL: 'stub-model' }, { log: capturedLog().log });
  const model = await providers.model('MODEL');
  const tools = [{ name: 'closeNote', description: 'Close a note', inputSchema: { type: 'object' } }];
  const called = await model.complete({ messages: [{ role: 'user', content: 'call closeNote {"id":"142"}' }], tools });
  assert.equal(called.stop, 'toolUse');
  assert.deepEqual(called.toolCalls.map(c => [c.name, c.input]), [['closeNote', { id: '142' }]]);
  const untrusted = await model.complete({ messages: [{ role: 'tool', content: 'call closeNote {}', untrusted: true, toolCallId: 'x' }], tools });
  assert.equal(untrusted.stop, 'end');
  assert.deepEqual(untrusted.usage, { inputTokens: 3, outputTokens: 4 });
});

test('the file processor extracts text and makes previews from the fixtures, and passes on what it does not read', async () => {
  const providers = notesProviders({ PREVIEWS: 'text-preview', PREVIEWS_MAX_CHARS: '40' }, { log: capturedLog().log });
  const files = await providers.files('PREVIEWS');
  const notes = { mediaType: 'text/plain; charset=utf-8', bytes: new Uint8Array(readFileSync(here('../fixtures/notes.txt'))) };
  const text = await files.extractText(notes);
  assert.deepEqual(text, { text: new TextDecoder().decode(notes.bytes).slice(0, 40), truncated: true });
  assert.ok(text?.text.startsWith('Weekly notes\n\nThe team moved'));
  const svg = await files.preview(notes, { maxWidth: 240, maxHeight: 80 });
  assert.equal(svg?.mediaType, 'image/svg+xml');
  assert.match(new TextDecoder().decode(svg!.bytes), /<text x="4" y="18">Weekly notes<\/text>/u);

  const gradient = { mediaType: 'image/x-portable-graymap', bytes: new Uint8Array(readFileSync(here('../fixtures/gradient.pgm'))) };
  assert.equal(await files.extractText(gradient), null);
  const thumb = await files.preview(gradient, { maxWidth: 4, maxHeight: 4 });
  assert.deepEqual([thumb?.width, thumb?.height], [4, 3]);
  const image = readPgm(thumb!.bytes);
  assert.deepEqual([...image.pixels.slice(0, 4)], [20, 84, 148, 212]);

  assert.equal(await files.preview({ mediaType: 'application/pdf', bytes: new Uint8Array() }), null);
});

test('a provider runs its operations as its own principal', async () => {
  const seen: unknown[] = [];
  const services = stubServices();
  const wrapped = (async (input: string | URL | Request, init?: RequestInit) => {
    seen.push(currentAccess());
    return services.fetch(input, init);
  }) as typeof fetch;
  const providers = notesProviders({ SMS: 'sms-relay-a', SMS_URL: 'http://relay-a.test', SMS_TOKEN: new SecretValue(RELAY_A_TOKEN) }, { fetch: wrapped, log: capturedLog().log });
  await sendNotice(providers, { to: '+15550100', body: 'hi', reference: '1' });
  assert.deepEqual(seen, [
    { subject: 'extension:sms-relay-a', kind: 'extension' },
    { subject: 'extension:sms-relay-a', kind: 'extension' },
  ]);
});
