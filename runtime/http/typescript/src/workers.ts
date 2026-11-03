import { isSqlDatabase, type SqlDatabase } from './d1.js';
import { isR2BucketBinding, type R2BucketBinding } from './r2.js';
import type { StackEnv } from './stackconfig.js';

/*
What a generated Workers entry, fetch.ts and scheduled.ts, reads its config
and its bindings with (D64, section 18.5 of docs/stack-model.md):

- stackEnvOf turns a Worker's env into the string map the config loaders
  read (loadEnvConfig, loadDatabase, loadService, loadBucket, loadCallers,
  loadCors): every plain_text and secret_text binding, a string, by its
  name. A literal, a parameter and a derived variable are each a binding
  named as the variable, and a derived member flagged secret a secret_text
  binding, so the loaders read on Workers what they read from a process's
  environment.
- loadSecrets awaits each Secrets Store secret binding a config names (a
  Secret<T> field's, which the platform binds as secrets_store_secret)
  once per isolate, and stackEnvWithSecrets adds them to the map.
- binding takes a D1, Hyperdrive (D92), R2, service or Secrets Store
  binding by the name the config's value gives, and refuses one that is
  missing or of another kind, naming it; serviceFetch makes a service
  binding the fetch of an SDK client.
- requestIP reads CF-Connecting-IP, the client's address, where the Hono
  adapter reads Bun.serve's server.requestIP: app.fetch(request, {
  requestIP }), so rate limits key by the client.
- perIsolate builds a value once per isolate, on first use, and again after
  a failure: Deps, the clients and the Hono app.

This is the package's ./workers entry. It imports nothing from node: and
declares the few Workers types it uses, so it needs no
@cloudflare/workers-types and no nodejs_compat.
*/

/** A Worker's env: strings for plain_text and secret_text bindings, objects for every other binding. */
export type WorkersEnv = Readonly<Record<string, unknown>>;

/** A Secrets Store secret binding (secrets_store_secret). */
export interface SecretsStoreSecret {
  get(): Promise<string>;
}

/** A service binding's Fetcher: the callee Worker's fetch. */
export interface ServiceFetcher {
  fetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response>;
}

/**
 * A Hyperdrive binding (D92): the connection string of a Postgres database
 * outside Cloudflare, reached through the Hyperdrive config's pool, which
 * ./hyperdrive opens node-postgres over.
 */
export interface HyperdriveBinding {
  readonly connectionString: string;
}

/** Whether value is a Hyperdrive binding: it holds a connection string. */
export function isHyperdriveBinding(value: unknown): value is HyperdriveBinding {
  return typeof value === 'object' && value !== null && typeof (value as Record<string, unknown>).connectionString === 'string';
}

/** The kinds of binding a config names. */
export type BindingKind = 'd1' | 'hyperdrive' | 'r2' | 'service' | 'secret';

/** The binding of each kind. */
export interface Bindings {
  readonly d1: SqlDatabase;
  readonly hyperdrive: HyperdriveBinding;
  readonly r2: R2BucketBinding;
  readonly service: ServiceFetcher;
  readonly secret: SecretsStoreSecret;
}

const KINDS: { readonly [K in BindingKind]: { readonly what: string; readonly is: (value: unknown) => value is Bindings[K] } } = {
  d1: { what: 'a D1 database', is: isSqlDatabase },
  hyperdrive: { what: 'a Hyperdrive config', is: isHyperdriveBinding },
  r2: { what: 'an R2 bucket', is: isR2BucketBinding },
  service: { what: 'a service binding', is: hasMethods<ServiceFetcher>('fetch') },
  secret: { what: 'a Secrets Store secret', is: hasMethods<SecretsStoreSecret>('get') },
};

function hasMethods<T>(...methods: string[]): (value: unknown) => value is T {
  return (value): value is T =>
    typeof value === 'object' && value !== null && methods.every(method => typeof (value as Record<string, unknown>)[method] === 'function');
}

/** Refused by binding and the readers here: the message names the binding, never a value. */
export class WorkersBindingError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'WorkersBindingError';
  }
}

/**
 * The env's string bindings, by name: what the config loaders read on
 * Workers. Every other binding is left out.
 */
export function stackEnvOf(env: WorkersEnv): StackEnv {
  const out: Record<string, string> = {};
  for (const [name, value] of Object.entries(env)) {
    if (typeof value === 'string') out[name] = value;
  }
  return out;
}

/**
 * The binding named name, of kind. Throws WorkersBindingError when the env
 * has none, or one of another kind.
 */
export function binding<K extends BindingKind>(env: WorkersEnv, name: string, kind: K): Bindings[K] {
  const { what, is } = KINDS[kind];
  if (!(name in env) || env[name] === undefined) {
    throw new WorkersBindingError(`the Worker has no binding ${name}, which its config names as ${what}`);
  }
  const value = env[name];
  if (!is(value)) {
    throw new WorkersBindingError(`the Worker's binding ${name} is not ${what}, which its config names it as`);
  }
  return value;
}

/**
 * A fetch that sends each request through the service binding: what an
 * SDK client's fetch option takes, so its calls reach the callee's Worker
 * whatever host its base URL names (D64, section 18.10).
 */
export function serviceFetch(fetcher: ServiceFetcher): typeof fetch {
  return ((input: RequestInfo | URL, init?: RequestInit) => fetcher.fetch(input, init)) as typeof fetch;
}

/** The secrets of each env, by name: each awaited once per isolate, and again after a failure. */
const secrets = new WeakMap<object, Map<string, Promise<string>>>();

/**
 * The value of each Secrets Store secret binding named in names, awaited
 * once per isolate: a later call returns what the first read, and a get
 * that failed is tried again on the next call. Rejects with
 * WorkersBindingError naming a binding that is missing or no secret, and
 * with the get's own error, after the binding's name, when it fails.
 */
export async function loadSecrets(env: WorkersEnv, names: readonly string[]): Promise<Record<string, string>> {
  let cache = secrets.get(env);
  if (cache === undefined) {
    cache = new Map();
    secrets.set(env, cache);
  }
  const values = names.map(name => {
    let value = cache.get(name);
    if (value === undefined) {
      const secret = binding(env, name, 'secret');
      value = Promise.resolve()
        .then(() => secret.get())
        .then(
          read => {
            if (typeof read !== 'string') throw new WorkersBindingError(`the Secrets Store secret ${name} answered no string`);
            return read;
          },
          (error: unknown) => {
            throw new WorkersBindingError(`the Secrets Store secret ${name}: ${error instanceof Error ? error.message : String(error)}`);
          }
        );
      value.catch(() => cache.delete(name));
      cache.set(name, value);
    }
    return value;
  });
  const read = await Promise.all(values);
  return Object.fromEntries(names.map((name, i) => [name, read[i]!]));
}

/**
 * The env's strings, as stackEnvOf gives them, with the value of each
 * Secrets Store secret named in names under its name: what a config whose
 * fields include Secret<T> ones loads from on Workers.
 */
export async function stackEnvWithSecrets(env: WorkersEnv, names: readonly string[]): Promise<StackEnv> {
  return { ...stackEnvOf(env), ...(await loadSecrets(env, names)) };
}

/** The header Cloudflare sets to the client's address. */
export const CONNECTING_IP_HEADER = 'CF-Connecting-IP';

/**
 * The client's address, from CF-Connecting-IP, in the shape of Bun.serve's
 * server.requestIP, which the Hono adapter reads from the env app.fetch
 * takes: app.fetch(request, { requestIP }). Null when the header is absent.
 */
export function requestIP(request: Request): { address: string } | null {
  const address = request.headers.get(CONNECTING_IP_HEADER)?.trim();
  return address ? { address } : null;
}

/**
 * A value built once per isolate: the first call builds it, every later
 * call shares it, and a build that rejects is forgotten, so the next call
 * builds again. Calls while a build runs share that build.
 */
export function perIsolate<T>(build: () => T | Promise<T>): () => Promise<T> {
  let built: Promise<T> | undefined;
  return () => {
    if (built === undefined) {
      const attempt = Promise.resolve().then(build);
      built = attempt;
      attempt.catch(() => {
        if (built === attempt) built = undefined;
      });
    }
    return built;
  };
}
