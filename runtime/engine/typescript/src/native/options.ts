/*
The TypeScript engine's options as the Rust engine reads them (D58,
runtime/engine/rust/engine/src/options.rs). What the TypeScript engine
takes as a function is data there: allowAll is "allowAll", a
permissionPolicy() made without a matcher "permission", and a policy of
rules passes as it is. Any other policy, and a permissionMatcher other
than hasAnyPermission, stays TypeScript: the addon asks it on this thread
during a synchronous call (hosted, src/native/engine.ts). A clock
function sets a manual clock before each call. The addon registers the
core's behaviors, then the ones behaviors names that it is built with
(the work queue's: Lease, Assignment, Queue, Presence, Blueprint, Budget
and Retries), in that order. A behavior passes by its name, never as an
object: an implementation written in TypeScript, even one whose
declaration names a behavior the addon has (a test's stand-in, or a
wrapper of @superschematic/engine-workqueue's own), runs its TypeScript
code, so it needs the bridge milestone 7b builds, as does an id source or
a value driver written in TypeScript. A host that has the work queue's
objects passes their declarations' names (the work queue's
test/helpers.ts maps its own by identity).
*/

import { hasAnyPermission, type PermissionMatcher } from '@superschematic/http-runtime';

import { allowAll, isEnginePermissionPolicy, type AccessPolicy } from '../access.js';
import type { AnyBehaviorImplementation } from '../behaviors/behavior.js';
import type { EngineOptions } from '../engine.js';
import { NativeUnsupportedError } from './errors.js';

/** A schema of a Postgres database the Rust engine keeps its tables in. */
export interface NativePostgresOptions {
  /** The database's URL (postgres://...) or a key-value string. */
  url: string;
  /** The schema the tables live in, created if absent; `engine` by default. */
  schema?: string;
  /** The most connections the pool opens; 4 by default. */
  poolMax?: number;
  /** Whether to listen for other processes' commits; true by default. */
  listen?: boolean;
  connectTimeoutMs?: number;
  migrationTimeoutMs?: number;
  /** How long the server lets a session sit idle inside a transaction before it ends it; 60000 by default, 0 for the server's own setting. */
  idleInTransactionTimeoutMs?: number;
}

/** The TypeScript engine's options, and the Rust engine's storage on Postgres in place of path. */
export type NativeEngineOptions = Omit<EngineOptions, 'path' | 'policy' | 'behaviors'> & {
  path?: string;
  /** The behaviors after the core's: a name the addon is built with (the work queue's), or one written in TypeScript (the bridge). */
  behaviors?: readonly (string | AnyBehaviorImplementation)[];
  /** A schema of a Postgres database, in place of path; the engine is then called through callAsync. */
  postgres?: NativePostgresOptions;
  /** The TypeScript engine's policies, or the Rust engine's own: "allowAll", "permission", { rules }. */
  policy: EngineOptions['policy'] | 'allowAll' | 'permission' | { rules: unknown[] };
};

/** What the facade answers in TypeScript for the Rust engine, on this thread. */
export interface HostedOptions {
  policy?: AccessPolicy;
  permissionMatcher?: PermissionMatcher;
}

/** The options the addon opens with, the clock the facade sets before each call, and what it hosts. */
export interface ResolvedNativeOptions {
  json: Record<string, unknown>;
  clock?: () => number;
  hosted: HostedOptions;
}

/** nativeOptions writes the options as the Rust engine reads them. */
export function nativeOptions(options: NativeEngineOptions): ResolvedNativeOptions {
  const json: Record<string, unknown> = {};
  const hosted: HostedOptions = {};
  let clock: (() => number) | undefined;
  for (const [key, value] of Object.entries(options)) {
    if (value === undefined) {
      continue;
    }
    switch (key) {
      case 'driver':
        // The Rust engine has one SQLite of its own.
        break;
      case 'policy':
        if (typeof value === 'function' && value !== allowAll && !isEnginePermissionPolicy(value as AccessPolicy)) {
          hosted.policy = value as AccessPolicy;
        } else {
          json.policy = policyOf(value as NativeEngineOptions['policy']);
        }
        break;
      case 'permissionMatcher':
        if (typeof value !== 'function') {
          throw new TypeError('permissionMatcher is a function (held, required) => boolean');
        }
        if (value !== hasAnyPermission) {
          hosted.permissionMatcher = value as PermissionMatcher;
        }
        break;
      case 'behaviors': {
        const names: string[] = [];
        for (const behavior of value as readonly unknown[]) {
          if (typeof behavior !== 'string') {
            throw new NativeUnsupportedError('a behavior written in TypeScript (defineBehavior)', 'bridge');
          }
          names.push(behavior);
        }
        if (names.length > 0) {
          json.behaviors = names;
        }
        break;
      }
      case 'ids':
        if (typeof value === 'function') {
          throw new NativeUnsupportedError('an id source written in TypeScript (ids)', 'bridge');
        }
        json.ids = value;
        break;
      case 'clock':
        if (typeof value === 'function') {
          clock = value as () => number;
          json.clock = { manual: clock() };
        } else {
          json.clock = value;
        }
        break;
      case 'values': {
        const { driver, ...rest } = value as Record<string, unknown>;
        if (driver !== undefined) {
          throw new NativeUnsupportedError('a value driver written in TypeScript (values.driver)', 'bridge');
        }
        json.values = rest;
        break;
      }
      default:
        json[key] = value;
    }
  }
  const names = Object.keys(hosted);
  if (names.length > 0) {
    json.hosted = names;
  }
  return clock === undefined ? { json, hosted } : { json, clock, hosted };
}

function policyOf(policy: NativeEngineOptions['policy']): unknown {
  if (policy === allowAll) {
    return 'allowAll';
  }
  if (typeof policy === 'function') {
    // permissionPolicy() without a matcher of its own.
    return 'permission';
  }
  return policy;
}
