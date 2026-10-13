/*
Loads the engine's Node-API addon (D58), the Rust engine built as
runtime/engine/rust/napi. Two layouts, as superscalar's loader has them:

- a checkout: scripts/engine-napi.sh (`napi build --platform`) leaves
  native/superschematic-engine.<triple>.node beside the package, which is
  loaded first;
- a published package: the addon ships in the platform package
  @superschematic/engine-<triple>, which a later milestone publishes.

The addon is loaded on first use, never when this module is imported, so
the TypeScript engine runs without it.
*/

import { existsSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

/** package.json "napi.binaryName": the basename of the .node file the napi CLI writes. */
export const NATIVE_BINARY_NAME = 'superschematic-engine';

/**
 * The version of the contract between the addon and this facade: the
 * request and error shapes of runtime/engine/rust/napi/src/lib.rs
 * (ABI_VERSION). An addon of another version is refused.
 */
export const NATIVE_ABI_VERSION = 1;

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
  /** Stops the runner and ends every event stream. */
  shutdown(): void;
  /** Shuts down and lets the engine go; its storage closes once a call in flight returns. */
  close(): void;
}

/** What the addon exports. */
export interface Addon {
  abiVersion(): number;
  NativeEngine: { open(options: string): AddonEngine };
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

let loaded: Addon | undefined;

/** loadAddon loads the addon once: the checkout's native/ first, then the platform package. */
export function loadAddon(): Addon {
  if (loaded !== undefined) {
    return loaded;
  }
  const require = createRequire(import.meta.url);
  const triple = nativeTriple();
  const local = join(nativeDir, `${NATIVE_BINARY_NAME}.${triple}.node`);
  let addon: Addon | undefined;
  // A local file that exists but does not load (stale, truncated, wrong
  // ABI) must not hide an installed platform package; its error rides along.
  let localDetail = '';
  if (existsSync(local)) {
    try {
      addon = require(local) as Addon;
    } catch (error) {
      localDetail = `; the checkout's ${local} failed to load: ${message(error)}`;
    }
  }
  if (addon === undefined) {
    const pkg = nativePackageName(triple);
    try {
      addon = require(pkg) as Addon;
    } catch (error) {
      throw new Error(
        `@superschematic/engine: cannot load the Rust engine's Node-API addon: no ${local} (scripts/engine-napi.sh builds it) and no package ${pkg} (${message(error)})${localDetail}`
      );
    }
  }
  const version = addon.abiVersion();
  if (version !== NATIVE_ABI_VERSION) {
    throw new Error(
      `@superschematic/engine: the Rust engine's addon speaks contract ${version}, this package ${NATIVE_ABI_VERSION}; rebuild it (scripts/engine-napi.sh)`
    );
  }
  loaded = addon;
  return addon;
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
