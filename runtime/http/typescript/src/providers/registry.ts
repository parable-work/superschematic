import type { Principal } from '../auth.js';
import { withCaller } from '../caller.js';
import { createLogger, type Logger } from '../logger.js';
import type { Revealable } from '../secret.js';
import type { ChannelAdapter } from './channel.js';
import { contractOf, providerConfigField, type RuntimeInterface } from './contracts.js';
import type { CredentialProvider } from './credentials.js';
import {
  checkProviderInstance,
  extensionPrincipal,
  ProviderSettings,
  type ProviderDefinition,
  type ProviderInterfaces,
} from './define.js';
import type { FileProcessor } from './files.js';
import type { ModelProvider } from './model.js';
import type { SandboxProvider } from './sandbox.js';

/*
How core obtains the provider an environment configures (D159). The API's
@envVars class names its slots with @providers, and the stack binds each
slot, a config field, to a provider's name in each environment, with the
provider's settings in the fields named after the slot. providersFrom reads
the loaded config with the slots and the definitions the server links, and
answers Providers: channel("SMS") gives the channel adapter slot SMS names,
made on first use from its settings. Each call core makes into a provider
runs within the provider's own principal (withCaller), so what it writes
through an ORM opened with no user is recorded as extension:<name>.

The build refuses an environment whose slot names an unknown provider or
leaves a needed setting unbound; providersFrom refuses the same at start,
with every problem and never a value, for a config no build checked.
*/

/** What providersFrom reads. */
export interface ProvidersOptions {
  /** The API's provider slots, as @providers declares them: SMS: "channel". */
  readonly slots: Readonly<Record<string, RuntimeInterface>>;
  /** The API's loaded config: each slot's field names a provider, and the fields after it hold its settings. */
  readonly config: Readonly<Record<string, unknown>>;
  /** The definitions of the provider packages the server links, the ones the API's config lists. */
  readonly definitions: readonly ProviderDefinition<any>[];
  /** The logger each provider's context derives its own from. */
  readonly log?: Logger;
}

/** A slot an environment fills. */
export interface ConfiguredProvider {
  readonly slot: string;
  readonly interface: RuntimeInterface;
  readonly provider: string;
}

/** The config names an unknown provider, one of another interface, or leaves a setting unset. */
export class ProviderConfigError extends Error {
  readonly problems: readonly string[];
  constructor(problems: readonly string[]) {
    super(`runtime providers: ${problems.join('; ')}`);
    this.name = 'ProviderConfigError';
    this.problems = problems;
  }
}

/** Asked for a slot the environment leaves unset, or one of another interface. */
export class ProviderNotConfiguredError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ProviderNotConfiguredError';
  }
}

/** The providers an environment configures. */
export interface Providers {
  /** The slots the environment fills, of one interface or of every one, sorted by slot. */
  configured(iface?: RuntimeInterface): readonly ConfiguredProvider[];
  /** The provider of slot, which takes iface; it throws ProviderNotConfiguredError for a slot unset. */
  get<I extends RuntimeInterface>(slot: string, iface: I): Promise<ProviderInterfaces[I]>;
  credentials(slot: string): Promise<CredentialProvider>;
  channel(slot: string): Promise<ChannelAdapter>;
  sandbox(slot: string): Promise<SandboxProvider>;
  model(slot: string): Promise<ModelProvider>;
  files(slot: string): Promise<FileProcessor>;
  /** The principal of the provider slot names. */
  principalOf(slot: string): Principal;
}

interface Entry {
  readonly slot: string;
  readonly iface: RuntimeInterface;
  readonly definition: ProviderDefinition<any>;
  readonly settings: ProviderSettings;
  readonly principal: Principal;
  made?: Promise<unknown>;
}

function isRevealable(value: unknown): value is Revealable<string> {
  return typeof value === 'object' && value !== null && typeof (value as Revealable<string>).reveal === 'function';
}

/** Reads the providers options.config configures; it throws a ProviderConfigError naming every problem. */
export function providersFrom(options: ProvidersOptions): Providers {
  const byName = new Map<string, ProviderDefinition<any>>();
  const problems: string[] = [];
  for (const definition of options.definitions) {
    if (byName.has(definition.name)) problems.push(`two linked providers are named ${definition.name}`);
    byName.set(definition.name, definition);
  }
  const log = options.log ?? createLogger();
  const entries = new Map<string, Entry>();
  for (const slot of Object.keys(options.slots).sort()) {
    const iface = options.slots[slot]!;
    const contract = contractOf(iface);
    if (!contract) {
      problems.push(`slot ${slot} takes ${JSON.stringify(iface)}, which is no runtime interface`);
      continue;
    }
    const name = options.config[slot];
    if (name === undefined || name === null || name === '') continue;
    if (typeof name !== 'string') {
      problems.push(`slot ${slot} holds no provider's name`);
      continue;
    }
    const definition = byName.get(name);
    if (!definition) {
      problems.push(`slot ${slot} names provider ${name}, which the server does not link (it links ${[...byName.keys()].join(', ') || 'none'})`);
      continue;
    }
    if (definition.implements !== iface) {
      problems.push(`slot ${slot} takes a ${contract.title}, and provider ${name} implements ${definition.implements}`);
      continue;
    }
    const values = new Map<string, string | Revealable<string>>();
    for (const [key, spec] of Object.entries(definition.config ?? {})) {
      const field = providerConfigField(slot, key);
      const value = options.config[field];
      if (value === undefined || value === null || value === '') {
        if (!spec.optional) problems.push(`provider ${name} in slot ${slot} needs ${field}, which is not set`);
        continue;
      }
      if (Boolean(spec.secret) !== isRevealable(value)) {
        problems.push(`provider ${name} in slot ${slot} reads ${field} as ${spec.secret ? 'a secret' : 'no secret'}, and the config holds it as ${isRevealable(value) ? 'a secret' : 'no secret'}`);
        continue;
      }
      values.set(key, isRevealable(value) ? value : String(value));
    }
    entries.set(slot, { slot, iface, definition, settings: new ProviderSettings(slot, values), principal: extensionPrincipal(definition) });
  }
  if (problems.length > 0) throw new ProviderConfigError(problems);

  const entryOf = (slot: string, iface?: RuntimeInterface): Entry => {
    const entry = entries.get(slot);
    if (!entry) {
      const declared = options.slots[slot];
      throw new ProviderNotConfiguredError(declared ? `slot ${slot} names no provider in this environment` : `${slot} is no provider slot of the API`);
    }
    if (iface && entry.iface !== iface) throw new ProviderNotConfiguredError(`slot ${slot} takes a ${contractOf(entry.iface)!.title}, not a ${contractOf(iface)!.title}`);
    return entry;
  };

  const make = (entry: Entry): Promise<unknown> => {
    entry.made ??= (async () => {
      const context = {
        slot: entry.slot,
        provider: entry.definition.name,
        principal: entry.principal,
        log: log.child({ provider: entry.definition.name, slot: entry.slot }),
      };
      const instance = await withCaller(entry.principal, () => entry.definition.create(entry.settings, context));
      checkProviderInstance(entry.definition, instance);
      return asPrincipal(instance as object, entry.principal);
    })();
    return entry.made;
  };

  const get = <I extends RuntimeInterface>(slot: string, iface: I): Promise<ProviderInterfaces[I]> => {
    try {
      return make(entryOf(slot, iface)) as Promise<ProviderInterfaces[I]>;
    } catch (err) {
      return Promise.reject(err);
    }
  };
  return {
    configured(iface?: RuntimeInterface) {
      return [...entries.values()]
        .filter(entry => !iface || entry.iface === iface)
        .map(entry => Object.freeze({ slot: entry.slot, interface: entry.iface, provider: entry.definition.name }));
    },
    get,
    credentials: slot => get(slot, 'credentials'),
    channel: slot => get(slot, 'channel'),
    sandbox: slot => get(slot, 'sandbox'),
    model: slot => get(slot, 'model'),
    files: slot => get(slot, 'files'),
    principalOf: slot => entryOf(slot).principal,
  };
}

/** The provider with each of its methods run within principal, the provider's own. */
function asPrincipal<T extends object>(instance: T, principal: Principal): T {
  return new Proxy(instance, {
    get(target, property, receiver) {
      const value = Reflect.get(target, property, receiver);
      if (typeof value !== 'function') return value;
      return (...args: unknown[]) => withCaller(principal, () => value.apply(target, args));
    },
  });
}
