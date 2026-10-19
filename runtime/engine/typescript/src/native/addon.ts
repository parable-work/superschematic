/*
Loads the engine's Node-API addon (D58), the Rust engine built as
runtime/engine/rust/addon over runtime/engine/rust/napi
(export_engine_napi!). Where it comes from, first match wins:

- a file the host names: the engine option `addon`, or for every engine
  the process opens the environment variable SUPERSCHEMATIC_ENGINE_ADDON,
  a .node file a project's engine crate built over its own behaviors
  (scripts/engine-napi.sh --crate <crate>/napi). Nothing else is tried
  when it does not load;
- a checkout: scripts/engine-napi.sh (`napi build --platform`) leaves
  native/superschematic-engine.<triple>.node beside the package, which is
  loaded first, as superscalar's loader does;
- a published package: the addon ships in the platform package
  @superschematic/engine-<triple>, an optional dependency of this package
  at its version, of which npm installs the one for its host.

The addon is loaded on first use, never when this module is imported, so
the TypeScript engine runs without it; each file loads once.
*/

import { existsSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

/** package.json "napi.binaryName": the basename of the .node file the napi CLI writes. */
export const NATIVE_BINARY_NAME = 'superschematic-engine';

/** The environment variable that names an addon to load in the core's place: a project's. */
export const NATIVE_ADDON_ENV = 'SUPERSCHEMATIC_ENGINE_ADDON';

/**
 * The version of the contract between the addon and this facade: the
 * request and error shapes of runtime/engine/rust/napi/src/lib.rs
 * (ABI_VERSION). An addon of another version is refused.
 */
export const NATIVE_ABI_VERSION = 2;

// `${process.platform}-${process.arch}` -> napi's platform triple: the
// targets in package.json "napi.targets"; Linux is glibc only.
const TRIPLES: Readonly<Record<string, string>> = {
  'darwin-arm64': 'darwin-arm64',
  'darwin-x64': 'darwin-x64',
  'linux-x64': 'linux-x64-gnu',
  'linux-arm64': 'linux-arm64-gnu',
};

/** An engine the addon opened: calls by name with JSON in and out. */
export interface AddonEngine {
  readonly dialect: 'sqlite' | 'postgres';
  /** A call on this thread: its result as JSON text, '' for undefined. SQLite only. */
  callSync(request: string): string;
  /** A call off this thread: AsyncTask on SQLite, tokio's blocking pool on Postgres. */
  call(request: string): Promise<string>;
  /**
   * Whether the facade answers something of the engine on this thread
   * alone (a policy, a matcher, a behavior written in TypeScript): its
   * asynchronous call and the runner's thread are refused.
   */
  readonly hosted: boolean;
  /** Whether the addon has the bridge to behaviors written in TypeScript (the test addon). */
  readonly bridged: boolean;
  /** Registers a behavior written in TypeScript as the bridge describes it (src/native/bridge.ts). */
  registerBehavior(descriptor: string): void;
  /** Raw SQL on the engine's SQLite file: get, all, run or exec, parameters as JSON (src/native/bridge.ts's encoding); the result as JSON. */
  storageQuery(mode: 'get' | 'all' | 'run' | 'exec', sql: string, params: string): string;
  /** Runs callback in a transaction this thread holds: true commits, false rolls back. Returns whether it committed. */
  storageTransaction(callback: () => boolean): boolean;
  /** Whether this thread holds a transaction on the engine's storage. */
  readonly storageInTransaction: boolean;
  /** Serves one HTTP request: its head as JSON and its body's bytes (SQLite). */
  handleHttp(head: string, body?: Buffer): HttpAnswer;
  /** Serves one HTTP request off this thread (Postgres, or SQLite). */
  handleHttpAsync(head: string, body?: Buffer): Promise<HttpAnswer>;
  /** Pulls an event stream handleHttp holds open, without waiting. */
  streamPull(id: number): StreamPull;
  /** Pulls an event stream off this thread, waiting up to timeoutMs. */
  streamPullAsync(id: number, timeoutMs: number): Promise<StreamPull>;
  /** Ends an event stream handleHttp holds open. */
  streamClose(id: number): void;
  /** How many commits that notified the engine's notifier has counted. */
  readonly epoch: number;
  /** Calls back on this thread each time the notifier moves, through a weak threadsafe function; the last call follows a shutdown. */
  watch(callback: (epoch: number) => void): void;
  /** Ends the watch. */
  unwatch(): void;
  /** Stops the runner and ends every event stream. */
  shutdown(): void;
  /** Shuts down and lets the engine go; its storage closes once a call in flight returns. */
  close(): void;
}

/**
 * What the facade answers for the engine in TypeScript (a policy, a
 * permission matcher), asked on this thread during a synchronous call:
 * the question's kind and JSON, answered as JSON. It must not throw.
 */
export type AddonHost = (kind: string, payload: string) => string;

/** One HTTP answer of the addon: status, headers as pairs, and body bytes or an event stream's id. */
export interface HttpAnswer {
  status: number;
  headers: string[][];
  body?: Buffer | null;
  stream?: number | null;
}

/** One pull of an event stream: its bytes, or its end, or neither (nothing yet). */
export interface StreamPull {
  data?: Buffer | null;
  end: boolean;
}

/** What the addon exports. */
export interface Addon {
  abiVersion(): number;
  /** The crate that built it: superschematic-engine-addon, the core's, or a project's. */
  project?(): string;
  /** The routes the engine serves (api::routes), as JSON. */
  engineRoutes(): string;
  /** A behavior context's method, on the test addon alone (src/native/bridge.ts). */
  bridgeCall?(generation: number, method: string, args: string): string;
  /** Opens an engine; host answers what the options' `hosted` lists. */
  NativeEngine: { open(options: string, host?: AddonHost): AddonEngine };
}

/** The platform triple of this host, or a throw naming the host. */
export function nativeTriple(): string {
  const key = `${process.platform}-${process.arch}`;
  const triple = TRIPLES[key];
  if (triple === undefined) {
    throw new Error(`@superschematic/engine: no Node-API addon is built for ${key}; supported: ${Object.values(TRIPLES).join(', ')}`);
  }
  return triple;
}

/** The platform package that carries the addon for a triple. */
export function nativePackageName(triple: string): string {
  return `@superschematic/engine-${triple}`;
}

// dist/native/addon.js -> ../../native, the checkout's native/.
const nativeDir = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'native');

// Each addon loaded, by its file (resolved), or '' for the core's.
const loaded = new Map<string, Addon>();

/**
 * loadAddon loads an addon once: the file `path` names, else the one
 * SUPERSCHEMATIC_ENGINE_ADDON names, else the core's (the checkout's
 * native/ first, then the platform package).
 */
export function loadAddon(path?: string): Addon {
  const named = path ?? (process.env[NATIVE_ADDON_ENV] || undefined);
  const key = named === undefined ? '' : resolve(named);
  const cached = loaded.get(key);
  if (cached !== undefined) {
    return cached;
  }
  const require = createRequire(import.meta.url);
  let addon: Addon;
  let rebuild = 'scripts/engine-napi.sh';
  if (named !== undefined) {
    const source = path === undefined ? NATIVE_ADDON_ENV : 'the engine option addon';
    rebuild = 'scripts/engine-napi.sh --crate <its crate>';
    try {
      addon = require(key) as Addon;
    } catch (error) {
      throw new Error(`@superschematic/engine: cannot load the Node-API addon ${key}, which ${source} names: ${message(error)}`);
    }
  } else {
    addon = loadCoreAddon(require);
  }
  const version = addon.abiVersion();
  if (version !== NATIVE_ABI_VERSION) {
    throw new Error(
      `@superschematic/engine: the Rust engine's addon${key === '' ? '' : ` ${key}`} speaks contract ${version}, this package ${NATIVE_ABI_VERSION}; rebuild it (${rebuild})`
    );
  }
  loaded.set(key, addon);
  return addon;
}

// loadCoreAddon is the core's addon: the checkout's native/ first, then the
// platform package.
function loadCoreAddon(require: NodeJS.Require): Addon {
  const triple = nativeTriple();
  const local = join(nativeDir, `${NATIVE_BINARY_NAME}.${triple}.node`);
  // A local file that exists but does not load (stale, truncated, wrong
  // ABI) must not hide an installed platform package; its error rides along.
  let localDetail = '';
  if (existsSync(local)) {
    try {
      return require(local) as Addon;
    } catch (error) {
      localDetail = `; the checkout's ${local} failed to load: ${message(error)}`;
    }
  }
  const pkg = nativePackageName(triple);
  try {
    return require(pkg) as Addon;
  } catch (error) {
    throw new Error(
      `@superschematic/engine: cannot load the Rust engine's Node-API addon: no ${local} (scripts/engine-napi.sh builds it) and no package ${pkg} (${message(error)})${localDetail}`
    );
  }
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
