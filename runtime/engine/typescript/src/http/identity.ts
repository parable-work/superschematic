/*
The core user model in the engine (D50). When the project's schemas
declare it, the deployment passes engineApp and engineMcp an `identity`,
the HTTP runtime's IdentityService over the project's identity descriptor
and a store, in place of `authenticate`. It authenticates every route, so
the engine's principal is the user, with their roles' permissions, and
engineApp serves the session routes under /auth.

engineIdentity builds the service with the engine's capabilities: for
each live schema of each namespace that the caller may read, whether the
access policy allows each action (engineCapabilities). Its store is the
project's: the authDb's tables in the engine's own SQLite file
(engineIdentityStore), in another SQLite file, or in the project's Postgres
database, shared with its APIs.
*/

import {
  IdentityService,
  principalOf as identityPrincipal,
  sqliteIdentityStore,
  type IdentityServiceOptions,
  type SqlIdentityStore,
} from '@superschematic/http-runtime/identity';

import type { Principal } from '../access.js';
import type { Engine } from '../engine.js';

/** The actions a capability names, in the order a schema's keys list them. */
const ACTIONS = ['read', 'write', 'define', 'publish'] as const;

/**
 * engineCapabilities is the engine's answer to capabilities: for each
 * schema with a live version that a namespace reaches and the principal
 * may read, `<namespace>/<schema>.<action>` for read, write, define and
 * publish, true when the access policy allows the action, keys in
 * code-point order. A schema the principal may not read has no key, as the
 * schema list leaves it out.
 */
export function engineCapabilities(engine: Engine, principal: Principal): Record<string, boolean> {
  const keys: Array<[string, boolean]> = [];
  for (const namespace of engine.namespaces.names) {
    for (const schema of engine.schemas.capabilities(principal, { namespace })) {
      for (const action of ACTIONS) {
        keys.push([`${namespace}/${schema.name}.${action}`, schema[action]]);
      }
    }
  }
  keys.sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
  return Object.fromEntries(keys);
}

/**
 * engineIdentity builds the identity service an engine takes: options are
 * the service's but routes and capabilities, and capabilities answers with
 * engineCapabilities. The administration routes match permissions with
 * the engine's permissionMatcher unless options name another.
 */
export function engineIdentity(engine: Engine, options: Omit<IdentityServiceOptions, 'routes' | 'capabilities'>): IdentityService {
  return new IdentityService({
    permissionMatcher: engine.permissionMatcher,
    ...options,
    capabilities: (principal) => engineCapabilities(engine, identityPrincipal(principal)),
  });
}

/**
 * engineIdentityStore is the identity store over the engine's own SQLite
 * file, which holds the authDb's tables beside the engine's: the
 * deployment applies the authDb's SQLite DDL to the file before the store
 * reads it. descriptor is the authDb's identity descriptor, its JSON text
 * or its value. Each store operation runs synchronously on the engine's
 * connection, its transaction included, as the engine's own writes do.
 */
export function engineIdentityStore(engine: Engine, descriptor: string | unknown): SqlIdentityStore {
  return sqliteIdentityStore(engine.storage, descriptor);
}
