import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { inspect } from 'node:util';
import { authorize } from '../auth';
import { currentAccess } from '../caller';
import { createLogger } from '../logger';
import type { RequestContext } from '../operation';
import { SecretValue } from '../secret';
import {
  checkProviderPackage,
  defineProvider,
  extensionAuthenticator,
  Injection,
  InjectionError,
  ProviderConfigError,
  ProviderContractError,
  ProviderNotConfiguredError,
  providersFrom,
  RUNTIME_CONTRACTS,
  useCredential,
  type ChannelAdapter,
  type CredentialProvider,
  type CredentialUse,
  type InjectionTarget,
  type OutboundMessage,
} from './index';

const CONTRACTS = fileURLToPath(new URL('../../../testdata/runtime_contracts.json', import.meta.url));

describe('the runtime contracts', () => {
  test('are the ones the build checks a provider package against', () => {
    const file = JSON.parse(readFileSync(CONTRACTS, 'utf8')) as { contracts: unknown };
    expect(JSON.parse(JSON.stringify(RUNTIME_CONTRACTS))).toEqual(file.contracts);
  });
});

/** A channel adapter that records what it sends, and the access caller each send ran within. */
function recordingChannel(name: string) {
  const sent: { message: OutboundMessage; url: string; caller: unknown }[] = [];
  const definition = defineProvider({
    name,
    implements: 'channel',
    config: { URL: {}, TOKEN: { secret: true }, SENDER: { optional: true } },
    role: ['messages.relay'],
    create(settings, context) {
      const url = settings.text('URL');
      const token = settings.secret('TOKEN');
      context.log.info('channel ready', { url, token });
      const adapter: ChannelAdapter = {
        kind: 'sms',
        async send(message) {
          sent.push({ message, url, caller: currentAccess() });
          return { providerMessageId: `${name}-${sent.length}`, state: 'queued' };
        },
        async health() {
          return { ok: token.reveal().length > 0 };
        },
      };
      return adapter;
    },
  });
  return { definition, sent };
}

describe('providersFrom', () => {
  test('gives the channel adapter a slot names, made from its settings, running as its own principal', async () => {
    const a = recordingChannel('relay-a');
    const b = recordingChannel('relay-b');
    const lines: string[] = [];
    const providers = providersFrom({
      slots: { SMS: 'channel', VAULT: 'credentials' },
      config: { SMS: 'relay-b', SMS_URL: 'http://relay.test', SMS_TOKEN: new SecretValue('token-value-1') },
      definitions: [a.definition, b.definition],
      log: createLogger({}, { write: line => lines.push(line) }),
    });
    expect(providers.configured()).toEqual([{ slot: 'SMS', interface: 'channel', provider: 'relay-b' }]);
    expect(providers.configured('credentials')).toEqual([]);
    const sms = await providers.channel('SMS');
    expect(await sms.send({ to: '+15550100', body: 'hello' })).toEqual({ providerMessageId: 'relay-b-1', state: 'queued' });
    expect(await sms.health()).toEqual({ ok: true });
    expect(a.sent).toHaveLength(0);
    expect(b.sent[0]!.url).toBe('http://relay.test');
    expect(b.sent[0]!.caller).toEqual({ subject: 'extension:relay-b', kind: 'extension' });
    expect(providers.principalOf('SMS')).toEqual({ subject: 'extension:relay-b', kind: 'extension', permissions: ['messages.relay'] });
    expect(await providers.channel('SMS')).toBe(sms);
    expect(lines.join('\n')).not.toContain('token-value-1');
    expect(JSON.parse(lines[0]!)).toMatchObject({ provider: 'relay-b', slot: 'SMS', url: 'http://relay.test', token: '[secret]' });
    await expect(providers.credentials('VAULT')).rejects.toBeInstanceOf(ProviderNotConfiguredError);
    await expect(providers.get('SMS', 'model')).rejects.toBeInstanceOf(ProviderNotConfiguredError);
  });

  test('refuses a config that names an unknown provider, one of another interface, or leaves a setting unset, naming no value', () => {
    const a = recordingChannel('relay-a');
    const refused = (config: Record<string, unknown>, slots: Record<string, 'channel' | 'model'> = { SMS: 'channel' }) => {
      try {
        providersFrom({ slots, config, definitions: [a.definition] });
      } catch (err) {
        expect(err).toBeInstanceOf(ProviderConfigError);
        return (err as ProviderConfigError).problems;
      }
      throw new Error('not refused');
    };
    expect(refused({ SMS: 'relay-x' })).toEqual(['slot SMS names provider relay-x, which the server does not link (it links relay-a)']);
    expect(refused({ BOT: 'relay-a' }, { BOT: 'model' })).toEqual(['slot BOT takes a model provider, and provider relay-a implements channel']);
    expect(refused({ SMS: 'relay-a', SMS_TOKEN: new SecretValue('token-value-2') })).toEqual(['provider relay-a in slot SMS needs SMS_URL, which is not set']);
    const problems = refused({ SMS: 'relay-a', SMS_URL: 'http://relay.test', SMS_TOKEN: 'token-value-3' });
    expect(problems).toEqual(['provider relay-a in slot SMS reads SMS_TOKEN as a secret, and the config holds it as no secret']);
    expect(problems.join()).not.toContain('token-value-3');
  });

  test('refuses a provider that leaves out an operation its interface requires', async () => {
    const broken = defineProvider({ name: 'half', implements: 'channel', create: () => ({ kind: 'sms', send: async () => ({ providerMessageId: '1', state: 'sent' }) }) as unknown as ChannelAdapter });
    const providers = providersFrom({ slots: { SMS: 'channel' }, config: { SMS: 'half' }, definitions: [broken] });
    await expect(providers.channel('SMS')).rejects.toThrow('implements no health, which every channel adapter implements');
  });
});

describe('defineProvider', () => {
  test('holds a definition to the rules the build holds a manifest to', () => {
    const base = { implements: 'channel' as const, create: () => ({}) as ChannelAdapter };
    expect(() => defineProvider({ ...base, name: 'Relay' })).toThrow(ProviderContractError);
    expect(() => defineProvider({ ...base, name: 'relay', implements: 'payments' as 'channel' })).toThrow('which is no runtime interface');
    expect(() => defineProvider({ ...base, name: 'relay', role: ['messages'] })).toThrow('names an area');
    expect(() => defineProvider({ ...base, name: 'relay', config: { url: {} } })).toThrow('upper-case');
  });

  test('checkProviderPackage holds a package.json manifest to the definition and its provider', async () => {
    const { definition } = recordingChannel('relay-a');
    const manifest = { name: 'relay-a', implements: 'channel', config: { URL: {}, TOKEN: { secret: true }, SENDER: { optional: true } }, role: ['messages.relay'] };
    const providers = providersFrom({ slots: { SMS: 'channel' }, config: { SMS: 'relay-a', SMS_URL: 'u', SMS_TOKEN: new SecretValue('t') }, definitions: [definition] });
    const instance = await providers.channel('SMS');
    checkProviderPackage({ superschematic: { provider: manifest } }, definition, instance);
    expect(() => checkProviderPackage({ superschematic: { provider: { ...manifest, operations: ['send', 'health', 'inbound'] } } }, definition, instance)).toThrow('lists operations');
    expect(() => checkProviderPackage({ superschematic: { provider: { ...manifest, role: ['messages.send'] } } }, definition)).toThrow("manifest's role");
    expect(() => checkProviderPackage({ name: 'x' }, definition)).toThrow('declares no');
  });
});

describe('a credential provider', () => {
  const SECRET = 'correct-horse-battery-staple';
  const target: InjectionTarget = { kind: 'header', site: 'https://reports.test', name: 'Authorization' };
  const vault: CredentialProvider = {
    async list() {
      return [{ ref: 'ops/reports-api/token', title: 'Reports API', sites: ['https://reports.test'] }];
    },
    async resolve(ref, into) {
      if (into.kind !== 'header') throw new Error('header only');
      return Injection.header(ref, into, new SecretValue(`Bearer ${SECRET}`));
    },
  };

  test('hands the value to the runner alone; the caller, the record and every log get the reference and the time', async () => {
    const headers = new Map<string, string>();
    const records: CredentialUse[] = [];
    const lines: string[] = [];
    const log = createLogger({}, { write: line => lines.push(line) });
    const use = await useCredential({
      provider: vault,
      providerName: 'vault-broker',
      ref: 'ops/reports-api/token',
      target,
      runner: { fill: () => {}, setHeader: (name, value) => void headers.set(name, value) },
      record: entry => void records.push(entry),
      now: () => new Date('2026-11-25T12:00:00Z'),
    });
    expect(headers.get('Authorization')).toBe(`Bearer ${SECRET}`);
    expect(use).toEqual({ ref: 'ops/reports-api/token', provider: 'vault-broker', target, usedAt: new Date('2026-11-25T12:00:00Z') });
    expect(records).toEqual([use]);

    const injection = await vault.resolve('ops/reports-api/token', target);
    log.info('credential used', { use, injection, listing: await vault.list() });
    log.error('step failed', { err: new Error(String(injection)) });
    const printed = [lines.join('\n'), JSON.stringify(injection), inspect(injection), String(injection), `${injection}`, inspect(use), JSON.stringify(records)].join('\n');
    expect(printed).not.toContain(SECRET);
    expect(JSON.parse(JSON.stringify(injection))).toEqual({ ref: 'ops/reports-api/token', target, value: '[secret]' });
    expect(Object.values(injection)).not.toContain(`Bearer ${SECRET}`);
  });

  test('refuses an injection it did not seal, or one for another reference or target', async () => {
    const runner = { fill: () => {}, setHeader: () => { throw new Error('the runner must not be reached'); } };
    const forged = { async list() { return []; }, async resolve(ref: string) { return { ref, target } as unknown as Injection; } };
    await expect(useCredential({ provider: forged, providerName: 'forged', ref: 'a', target, runner })).rejects.toBeInstanceOf(InjectionError);
    const swapped: CredentialProvider = { ...vault, resolve: async (_ref, into) => Injection.header('ops/other', into, new SecretValue(SECRET)) };
    await expect(useCredential({ provider: swapped, providerName: 'swapped', ref: 'ops/reports-api/token', target, runner })).rejects.toThrow('an injection for ops/other');
    const redirected: CredentialProvider = { ...vault, resolve: async ref => Injection.header(ref, { site: 'https://elsewhere.test', name: 'Authorization' }, new SecretValue(SECRET)) };
    await expect(useCredential({ provider: redirected, providerName: 'redirected', ref: 'ops/reports-api/token', target, runner })).rejects.toThrow('into another target');
  });
});

describe('an extension principal', () => {
  test('authenticates a provider by its key with its role alone', async () => {
    const authenticate = extensionAuthenticator([{ key: new SecretValue('bridge-key'), provider: { name: 'chat-bridge', role: ['messages.relay'] } }]);
    const ctx = (bearerToken?: string) => ({ bearerToken }) as RequestContext;
    const principal = await authenticate(ctx('bridge-key'));
    expect(principal).toEqual({ subject: 'extension:chat-bridge', kind: 'extension', permissions: ['messages.relay'] });
    expect(await authenticate(ctx('other'))).toBeNull();
    expect(() => authorize(principal, { public: false, required: true, permissions: ['messages.relay'] })).not.toThrow();
    expect(() => authorize(principal, { public: false, required: true, permissions: ['notes.write'] })).toThrow();
  });
});
