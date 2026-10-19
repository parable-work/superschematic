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
and Retries), in that order, then the ones written in TypeScript. A
behavior named by a string is the Rust one the addon is built with; an
implementation object runs its TypeScript code, even one whose
declaration names a behavior the addon has (a test's stand-in, or a
wrapper of @superschematic/engine-workqueue's own): the test addon's
bridge runs it on this thread (src/native/bridge.ts), and the shipped
addon refuses it. A host that has the work queue's objects passes their
declarations' names (the work queue's test/helpers.ts maps its own by
identity). An id source or a value driver written in TypeScript is not
on the Rust engine.
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
}

/** The TypeScript engine's options, and the Rust engine's storage on Postgres in place of path. */
export type NativeEngineOptions = Omit<EngineOptions, 'path' | 'policy' | 'behaviors'> & {
  path?: string;
  /**
   * The behaviors after the core's: a name, one the addon is built with
   * (in Rust: the work queue's), or an implementation written in
   * TypeScript, which the test addon's bridge runs.
   */
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
  /** Behaviors written in TypeScript, which the test addon's bridge runs. */
  behaviors?: readonly AnyBehaviorImplementation[];
  /** An id source written in TypeScript, asked on this thread for each create that gives no id. */
  ids?: () => string;
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
        // A name is a behavior the addon is built with, in Rust; an
        // implementation object is TypeScript's, which the test addon's
        // bridge runs, whatever its declaration names.
        const names = (value as readonly unknown[]).filter((each): each is string => typeof each === 'string');
        const objects = (value as readonly unknown[]).filter((each) => typeof each !== 'string') as AnyBehaviorImplementation[];
        if (names.length > 0) {
          json.behaviors = names;
        }
        if (objects.length > 0) {
          hosted.behaviors = objects;
        }
        break;
      }
      case 'ids':
        if (typeof value === 'function') {
          hosted.ids = value as () => string;
        } else {
          json.ids = value;
        }
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
          throw new NativeUnsupportedError('a value driver written in TypeScript (values.driver)', 'driver');
        }
        json.values = rest;
        break;
      }
      default:
        json[key] = value;
    }
  }
  const names = Object.keys(hosted).filter((name) => name !== 'behaviors');
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
