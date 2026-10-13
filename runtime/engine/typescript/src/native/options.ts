/*
The TypeScript engine's options as the Rust engine reads them (D58,
runtime/engine/rust/engine/src/options.rs). What the TypeScript engine
takes as a function is data there: allowAll is "allowAll", a
permissionPolicy() made without a matcher "permission", and a policy of
rules passes as it is. A clock function sets a manual clock before each
call; a behavior, a policy, a matcher, an id source or a value driver
written in TypeScript needs the bridge milestone 7b builds.
*/

import { hasAnyPermission } from '@superschematic/http-runtime';

import { allowAll, isEnginePermissionPolicy } from '../access.js';
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
export type NativeEngineOptions = Omit<EngineOptions, 'path' | 'policy'> & {
  path?: string;
  /** A schema of a Postgres database, in place of path; the engine is then called through callAsync. */
  postgres?: NativePostgresOptions;
  /** The TypeScript engine's policies the Rust engine reads, or its own: "allowAll", "permission", { rules }. */
  policy: EngineOptions['policy'] | 'allowAll' | 'permission' | { rules: unknown[] };
};

/** The options the addon opens with, and the clock the facade sets before each call. */
export interface ResolvedNativeOptions {
  json: Record<string, unknown>;
  clock?: () => number;
}

/** nativeOptions writes the options as the Rust engine reads them. */
export function nativeOptions(options: NativeEngineOptions): ResolvedNativeOptions {
  const json: Record<string, unknown> = {};
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
        json.policy = policyOf(value as NativeEngineOptions['policy']);
        break;
      case 'permissionMatcher':
        if (typeof value !== 'function') {
          throw new TypeError('permissionMatcher is a function (held, required) => boolean');
        }
        if (value !== hasAnyPermission) {
          throw new NativeUnsupportedError('a permissionMatcher written in TypeScript', 'bridge');
        }
        break;
      case 'behaviors':
        if ((value as readonly unknown[]).length > 0) {
          throw new NativeUnsupportedError('a behavior written in TypeScript (defineBehavior)', 'bridge');
        }
        break;
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
  return clock === undefined ? { json } : { json, clock };
}

function policyOf(policy: NativeEngineOptions['policy']): unknown {
  if (typeof policy !== 'function') {
    return policy;
  }
  if (policy === allowAll) {
    return 'allowAll';
  }
  if (isEnginePermissionPolicy(policy)) {
    return 'permission';
  }
  throw new NativeUnsupportedError('an access policy written in TypeScript', 'bridge');
}
