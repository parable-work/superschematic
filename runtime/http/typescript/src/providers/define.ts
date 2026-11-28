import type { Authenticator, Principal } from '../auth.js';
import type { Logger } from '../logger.js';
import { SECRET_MASK, SecretValue, type Revealable } from '../secret.js';
import type { ChannelAdapter } from './channel.js';
import { CALLER_KEY, contractOf, extensionSubject, RUNTIME_CONTRACTS, type RuntimeInterface } from './contracts.js';
import type { CredentialProvider } from './credentials.js';
import type { FileProcessor } from './files.js';
import type { ModelProvider } from './model.js';
import type { SandboxProvider } from './sandbox.js';

/*
A provider package (D159) exports the definition defineProvider checks: its
name, the interface it implements, the settings it reads and its role, and
create, which makes the provider from its settings. Its package.json
declares the same under "superschematic": {"provider": ...}, which the build
reads and checks against the interface's contract; checkProviderPackage
holds the two to each other in the package's own test.
*/

/** The TypeScript interface of each runtime interface. */
export interface ProviderInterfaces {
  credentials: CredentialProvider;
  channel: ChannelAdapter;
  sandbox: SandboxProvider;
  model: ModelProvider;
  files: FileProcessor;
}

/** One setting a provider reads, from the config field named after its slot and the key. */
export interface ProviderSettingSpec {
  /** A Secret<string> field: the provider gets a SecretValue, never a string. */
  readonly secret?: boolean;
  /** An environment may leave it unset. */
  readonly optional?: boolean;
  readonly description?: string;
}

/** What a provider runs with. */
export interface ProviderContext {
  /** The config field that names it. */
  readonly slot: string;
  /** Its name. */
  readonly provider: string;
  /** Its own principal (kind extension), whom core runs each of its operations as. */
  readonly principal: Principal;
  /** A logger whose lines carry the provider and the slot. */
  readonly log: Logger;
}

/** A provider package's definition. */
export interface ProviderDefinition<I extends RuntimeInterface = RuntimeInterface> {
  /** Lower-case words joined by hyphens; an environment's slot holds it. */
  readonly name: string;
  readonly implements: I;
  /** The settings it reads, by key: slot SMS and key URL read SMS_URL. */
  readonly config?: Readonly<Record<string, ProviderSettingSpec>>;
  /** The permissions its principal holds, each one capability, two dotted segments or more. */
  readonly role?: readonly string[];
  create(settings: ProviderSettings, context: ProviderContext): ProviderInterfaces[I] | Promise<ProviderInterfaces[I]>;
}

/** A definition, a package or a provider that breaks its interface's contract. */
export class ProviderContractError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ProviderContractError';
  }
}

const NAME = /^[a-z][a-z0-9]*(-[a-z0-9]+)*$/u;
const KEY = /^[A-Z][A-Z0-9_]*$/u;
const SEGMENT = /^[A-Za-z][A-Za-z0-9_]*$/u;

/** Checks a definition as the build checks a manifest (ir.RuntimeProvider.Check) and returns it. */
export function defineProvider<I extends RuntimeInterface>(definition: ProviderDefinition<I>): ProviderDefinition<I> {
  const where = `provider ${JSON.stringify(definition.name)}`;
  if (!NAME.test(definition.name)) throw new ProviderContractError(`${where}: a name is lower-case letters and digits in words joined by hyphens`);
  if (!contractOf(definition.implements)) {
    throw new ProviderContractError(`${where}: implements ${JSON.stringify(definition.implements)}, which is no runtime interface; it is one of ${RUNTIME_CONTRACTS.map(c => c.interface).join(', ')}`);
  }
  for (const key of Object.keys(definition.config ?? {})) {
    if (!KEY.test(key)) throw new ProviderContractError(`${where}: config key ${JSON.stringify(key)} is upper-case letters, digits and underscores`);
    if (key === CALLER_KEY && !definition.config![key]!.secret) {
      throw new ProviderContractError(`${where}: config key ${CALLER_KEY} is the key the provider calls the app with, which an environment sets as a secret; declare it { secret: true } (#744)`);
    }
  }
  for (const permission of definition.role ?? []) {
    const segments = permission.split('.');
    if (!segments.every(segment => SEGMENT.test(segment))) throw new ProviderContractError(`${where}: role permission ${JSON.stringify(permission)} is a dotted path of words`);
    if (segments.length < 2) throw new ProviderContractError(`${where}: role permission ${JSON.stringify(permission)} names an area; a provider's role names one capability in it`);
  }
  if (typeof definition.create !== 'function') throw new ProviderContractError(`${where}: create is not a function`);
  return Object.freeze({ ...definition });
}

/** The operations of an interface a provider has: each one it requires, which it must have, and the optional ones it has. */
export function checkProviderInstance(definition: ProviderDefinition, instance: unknown): readonly string[] {
  const contract = contractOf(definition.implements)!;
  const where = `${contract.title} ${definition.name}`;
  if (typeof instance !== 'object' || instance === null) throw new ProviderContractError(`${where}: create made no object`);
  const has = (op: string) => typeof (instance as Record<string, unknown>)[op] === 'function';
  const missing = contract.required.filter(op => !has(op));
  if (missing.length > 0) throw new ProviderContractError(`${where}: implements no ${missing.join(', ')}, which every ${contract.title} implements`);
  return [...contract.required, ...(contract.optional ?? []).filter(has)];
}

/**
 * Holds a package's package.json manifest to its definition and, given one,
 * to a provider it made: the name, the interface, the settings, the role,
 * and the operations, which the manifest lists or leaves to the required
 * ones. A package's own test calls it, so the build and the runtime read
 * one provider.
 */
export function checkProviderPackage(packageJson: unknown, definition: ProviderDefinition, instance?: unknown): void {
  const manifest = ((packageJson as { superschematic?: { provider?: Record<string, unknown> } })?.superschematic?.provider ?? undefined) as
    | { name?: unknown; implements?: unknown; operations?: unknown; config?: Record<string, { secret?: boolean; optional?: boolean }>; role?: unknown }
    | undefined;
  const where = `package of provider ${definition.name}`;
  if (!manifest) throw new ProviderContractError(`${where}: its package.json declares no "superschematic": {"provider": ...}`);
  if (manifest.name !== definition.name) throw new ProviderContractError(`${where}: its manifest names ${JSON.stringify(manifest.name)}`);
  if (manifest.implements !== definition.implements) throw new ProviderContractError(`${where}: its manifest implements ${JSON.stringify(manifest.implements)}, the definition ${definition.implements}`);
  const settings = (spec: Readonly<Record<string, { secret?: boolean; optional?: boolean }>> | undefined) =>
    JSON.stringify(Object.keys(spec ?? {}).sort().map(key => [key, Boolean(spec![key]!.secret), Boolean(spec![key]!.optional)]));
  if (settings(manifest.config) !== settings(definition.config)) throw new ProviderContractError(`${where}: its manifest's config ${settings(manifest.config)} is not the definition's ${settings(definition.config)}`);
  const role = (list: unknown) => JSON.stringify([...((list as string[] | undefined) ?? [])].sort());
  if (role(manifest.role) !== role(definition.role)) throw new ProviderContractError(`${where}: its manifest's role ${role(manifest.role)} is not the definition's ${role(definition.role)}`);
  if (instance !== undefined) {
    const has = checkProviderInstance(definition, instance);
    const contract = contractOf(definition.implements)!;
    const listed = Array.isArray(manifest.operations) ? (manifest.operations as string[]) : contract.required;
    const sorted = (ops: readonly string[]) => JSON.stringify([...ops].sort());
    if (sorted(listed) !== sorted(has)) throw new ProviderContractError(`${where}: its manifest lists operations ${sorted(listed)}, and the provider has ${sorted(has)}`);
  }
}

/** The principal a provider acts as (D159): kind extension, subject extension:<name>, its role's permissions. */
export function extensionPrincipal(definition: Pick<ProviderDefinition, 'name' | 'role'>): Principal {
  return Object.freeze({ subject: extensionSubject(definition.name), kind: 'extension' as const, permissions: Object.freeze([...(definition.role ?? [])]) });
}

function revealable(value: unknown): value is Revealable<string> {
  return typeof value === 'object' && value !== null && typeof (value as Revealable<string>).reveal === 'function';
}

/** A setting missing, or read as what it is not. */
export class ProviderSettingError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ProviderSettingError';
  }
}

/**
 * A provider's settings: the config fields named after its slot, by key. A
 * plain setting reads as a string and a secret one as a SecretValue, so a
 * provider never holds a secret as a plain string. JSON and string
 * conversion print the keys and masked secrets.
 */
export class ProviderSettings {
  readonly #slot: string;
  readonly #values: ReadonlyMap<string, string | Revealable<string>>;

  constructor(slot: string, values: ReadonlyMap<string, string | Revealable<string>>) {
    this.#slot = slot;
    this.#values = values;
  }

  /** Whether the environment set key. */
  has(key: string): boolean {
    return this.#values.has(key);
  }

  /** The plain setting key; it throws for one unset or secret. */
  text(key: string): string {
    const value = this.optionalText(key);
    if (value === undefined) throw new ProviderSettingError(`setting ${key} of slot ${this.#slot} is not set`);
    return value;
  }

  /** The plain setting key, or undefined when unset; it throws for a secret one. */
  optionalText(key: string): string | undefined {
    const value = this.#values.get(key);
    if (value === undefined) return undefined;
    if (revealable(value)) throw new ProviderSettingError(`setting ${key} of slot ${this.#slot} is a secret; read it with secret()`);
    return value;
  }

  /** The secret setting key; it throws for one unset or plain. */
  secret(key: string): SecretValue<string> {
    const value = this.#values.get(key);
    if (value === undefined) throw new ProviderSettingError(`setting ${key} of slot ${this.#slot} is not set`);
    if (!revealable(value)) throw new ProviderSettingError(`setting ${key} of slot ${this.#slot} is no secret; read it with text()`);
    return value instanceof SecretValue ? value : new SecretValue(value.reveal());
  }

  toJSON(): Record<string, string> {
    return Object.fromEntries([...this.#values].map(([key, value]) => [key, revealable(value) ? SECRET_MASK : value]));
  }

  toString(): string {
    return `ProviderSettings(${this.#slot}: ${[...this.#values.keys()].join(', ')})`;
  }
}

/** A key a provider calls the app with, and the provider it is (#744). */
export interface ExtensionKey {
  readonly key: Revealable<string>;
  readonly provider: Pick<ProviderDefinition, 'name' | 'role'>;
}

/**
 * An Authenticator for the providers that call the app over HTTP, a
 * messaging bridge that connects outward and relays replies, for one: a
 * Bearer token equal to one of keys is that provider's principal, with its
 * role's permissions alone. Any other request is left to next.
 */
export function extensionAuthenticator(keys: readonly ExtensionKey[], next?: Authenticator): Authenticator {
  const encoder = new TextEncoder();
  const equal = (a: string, b: string): boolean => {
    const x = encoder.encode(a);
    const y = encoder.encode(b);
    let diff = x.length ^ y.length;
    for (let i = 0; i < Math.max(x.length, y.length); i++) diff |= (x[i] ?? 0) ^ (y[i] ?? 0);
    return diff === 0;
  };
  return async ctx => {
    const token = ctx.bearerToken;
    if (token) {
      for (const entry of keys) {
        if (equal(token, entry.key.reveal())) return extensionPrincipal(entry.provider);
      }
    }
    return next ? next(ctx) : null;
  };
}
