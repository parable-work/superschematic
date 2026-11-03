/*
An engine's options (EngineOptions), and how the Rust engine reads them
(D58, runtime/engine/rust/engine/src/options.rs). Most are data, which
pass as JSON: allowAll is "allowAll", a permissionPolicy() made without a
matcher "permission", and a policy of rules passes as it is. Any other
policy, a permissionMatcher other than hasAnyPermission and an id source
written in TypeScript stay TypeScript: the addon asks them on this thread
during a synchronous call (hosted, src/native/engine.ts), so
openEngineAsync, whose calls run off it, refuses them. A clock function
sets a manual clock before each call. The addon registers the core's
behaviors, then the ones behaviors names that it is built with (the
core's addon: the work queue's, Lease, Assignment, Queue, Presence,
Blueprint, Budget and Retries; a project's addon, which the option addon
names: its extensions' too), in that order, then the ones written in
TypeScript. A behavior named by a string is the Rust one the addon is
built with; an implementation object runs its TypeScript code, even one
whose declaration names a behavior the addon has (a test's stand-in): the
test addon's bridge runs it on this thread (src/native/bridge.ts), and
the shipped addon refuses it. A value driver written in TypeScript is not
on the Rust engine, whose drivers are Rust ValueDrivers.
*/

import { hasAnyPermission, type PermissionMatcher } from '@superschematic/http-runtime';

import { allowAll, isEnginePermissionPolicy, type AccessPolicy } from '../access.js';
import type { AnyBehaviorImplementation } from '../testing/behavior.js';
import type { RetentionOptions } from '../types/events.js';
import type { NamespaceOptions } from '../types/namespaces.js';
import type { RunnerOptions } from '../types/runner.js';
import type { SignalOptions } from '../types/signals.js';
import type { ValueOptions } from '../types/values.js';
import { UnsupportedError } from './errors.js';
import type { ToolOptions } from './tool-options.js';

/** A schema of a Postgres database the engine keeps its tables in. */
export interface PostgresOptions {
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

/** What an engine opens with: openEngine's and openEngineAsync's options. */
export interface EngineOptions {
  /** The SQLite file. One process writes it. */
  path?: string;
  /**
   * A schema of a Postgres database, in place of path: an engine on it is
   * openEngineAsync's, since a synchronous call there is refused.
   */
  postgres?: PostgresOptions;
  /**
   * The addon to open the engine on, a .node file, in place of the core's:
   * a project's, built over its own behaviors by its engine crate's napi/
   * member (scripts/engine-napi.sh --crate). SUPERSCHEMATIC_ENGINE_ADDON
   * names one for every engine that names none.
   */
  addon?: string;
  /** How long a write waits for another connection's lock before it is refused busy, in milliseconds; 5000 by default. */
  busyTimeoutMs?: number;
  /**
   * Who may read, write, define and publish. There is no default: pass
   * the deployment's policy, permissionPolicy() to allow an action to a
   * caller holding `<schema>.<action>`, or allowAll for tests and local
   * use. As data: "allowAll", "permission" (permissionPolicy()'s rule) or
   * { rules }, the policy read as rules (D58). A function other than
   * allowAll and permissionPolicy() is asked on the JavaScript thread
   * during a synchronous call, so openEngineAsync refuses one.
   */
  policy: AccessPolicy | 'allowAll' | 'permission' | { rules: unknown[] };
  /**
   * The meta-schema schema documents load against: the `superschematic
   * json-schema` output of the deployment's binary, parsed or as JSON
   * text. The default is the core registry's.
   */
  metaSchema?: Record<string, unknown> | string;
  /**
   * The configured namespaces besides `default`, and the shared one. A
   * create makes more while the engine runs (engine.namespaces), which
   * the storage keeps.
   */
  namespaces?: NamespaceOptions;
  /**
   * Makes the id of an instance created without one: random UUIDs by
   * default ("uuid"), { sequence: prefix } or { seeded: prefix } as data,
   * or a function, asked on the JavaScript thread during a synchronous
   * call (openEngine alone).
   */
  ids?: (() => string) | 'uuid' | { sequence: string } | { seeded: string };
  /**
   * The time in epoch milliseconds: the system's ("system", the default),
   * "strict", { manual: ms }, or a function, read once for each call that
   * may use the time.
   */
  clock?: (() => number) | 'system' | 'strict' | { manual: number };
  /**
   * The behaviors this engine runs besides the core's (Workflow, Comments,
   * Revisions, Dependencies, Links, Rollups, Search, Reactions, Constants,
   * Variants, Branches, StateRules, Approvals, Numbering, Timers, Derived
   * and Provenance, which
   * it registers first), by name: one the addon is built with, the work
   * queue's (Lease, Assignment, Queue, Presence, Blueprint, Budget and
   * Retries) in the core's addon, a project's own in the addon its engine
   * crate builds (addon). A schema that composes a behavior the engine
   * does not run is refused. An implementation written in TypeScript
   * (@superschematic/engine/testing's defineBehavior) runs on the test
   * addon alone, through its bridge.
   */
  behaviors?: readonly (string | AnyBehaviorImplementation)[];
  /**
   * The invocation policy and the vendor-extension keys the describe and
   * tools documents and the MCP tools are written with: the deployment's
   * binary's registrations, the core's by default (runtime/engine/README.md,
   * "Tools").
   */
  tools?: ToolOptions;
  /**
   * Decides whether a principal holds a permission a behavior's config
   * names (a behavior's can()). The default is the HTTP runtime's
   * hasAnyPermission: dotted paths, where a granted permission covers
   * itself and every permission nested under it, and no root permission.
   * Another matcher is asked on the JavaScript thread during a
   * synchronous call (openEngine alone).
   */
  permissionMatcher?: PermissionMatcher;
  /**
   * The runner of behaviors' reactions and schedules (D16, amended): the
   * principal they act as, which the access policy is asked about like
   * any caller, and its retry and depth limits. Without it the runner
   * refuses to start; engine.runner.start() starts it.
   */
  runner?: RunnerOptions;
  /**
   * The value store (runtime/engine/README.md, "The value store"): the
   * threshold past which a top-level member of an instance, an event or a
   * behavior's row is stored once by hash (64 KiB by default) and how many
   * bytes of them the engine keeps parsed in memory.
   */
  values?: ValueOptions;
  /**
   * How long the event log keeps events (runtime/engine/README.md, "Retention"):
   * by age, by count or both, pruned on the runner every everyMs, never
   * past an event a subscription has yet to handle. Without it the log
   * keeps every event.
   */
  retention?: RetentionOptions;
  /**
   * The signals of instances (D77): how many a principal may send on one
   * instance each second, 20 by default (runtime/engine/README.md, "The
   * event stream").
   */
  signals?: SignalOptions;
}

/** What the engine's TypeScript side answers for the Rust engine, on this thread. */
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
export function nativeOptions(options: EngineOptions): ResolvedNativeOptions {
  const json: Record<string, unknown> = {};
  const hosted: HostedOptions = {};
  let clock: (() => number) | undefined;
  for (const [key, value] of Object.entries(options)) {
    if (value === undefined) {
      continue;
    }
    switch (key) {
      case 'driver':
        // The TypeScript engine's choice of SQLite module: the engine has
        // one SQLite of its own.
        throw new TypeError('driver is not an engine option: the engine has a SQLite of its own');
      case 'addon':
        // Which addon opens the engine (src/native/addon.ts), not an option of its.
        if (typeof value !== 'string' || value === '') {
          throw new TypeError('addon is the path of a Node-API addon (.node)');
        }
        break;
      case 'policy':
        if (typeof value === 'function' && value !== allowAll && !isEnginePermissionPolicy(value as AccessPolicy)) {
          hosted.policy = value as AccessPolicy;
        } else {
          json.policy = policyOf(value as EngineOptions['policy']);
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
          throw new UnsupportedError('a value driver written in TypeScript (values.driver)', 'driver');
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

function policyOf(policy: EngineOptions['policy']): unknown {
  if (policy === allowAll) {
    return 'allowAll';
  }
  if (typeof policy === 'function') {
    // permissionPolicy() without a matcher of its own.
    return 'permission';
  }
  return policy;
}
