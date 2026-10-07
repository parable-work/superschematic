// Who the callers are, and what each may do.
//
// authenticate is the HTTP runtime's Authenticator: it turns a request's
// bearer token into a principal, or null (401). This example looks the
// token up in a fixed table; a deployment verifies a signed token or a
// session instead.
//
// policy is the engine's access policy, asked before every read and write.
// Permissions are dotted names, and a permission covers itself and every
// permission nested under it (the HTTP runtime's hasAnyPermission), so
// bob's `notes` covers `notes.read`, `notes.write`, and the `notes.publish`
// and `notes.review` that the schema's behaviors name.
import type { AccessPolicy, Principal } from '@superschematic/engine';
import { hasAnyPermission, type Authenticator } from '@superschematic/http-runtime';

/** The callers this example knows, by bearer token. */
export const callers: ReadonlyMap<string, Principal> = new Map([
  // An author: reads and writes notes.
  ['alice-token', { subject: 'alice', permissions: ['notes.read', 'notes.write'] }],
  // An editor: everything under notes, publishing and reviewing included.
  ['bob-token', { subject: 'bob', permissions: ['notes'] }],
  // A reader: reads notes, comments on them and proposes changes.
  ['carol-token', { subject: 'carol', permissions: ['notes.read'] }],
  // A schema owner: defines and publishes schemas, reads no notes.
  ['dana-token', { subject: 'dana', permissions: ['schemas'] }],
]);

/** The caller a request's bearer token names, or null, which answers 401. */
export const authenticate: Authenticator = async (ctx) => {
  const token = ctx.bearerToken;
  return token === undefined ? null : callers.get(token) ?? null;
};

/** What each caller may do: the engine asks before every read and write. */
export const policy: AccessPolicy = ({ principal, action, schema, operation }) => {
  // Making, archiving and listing namespaces: this example makes none
  // while it runs.
  if (action === 'manage') {
    return false;
  }
  if (action === 'define' || action === 'publish') {
    return hasAnyPermission(principal.permissions, [`schemas.${action}`]);
  }
  // A reader may comment on a note and propose a change to it, but not
  // change it.
  if (action === 'write' && (operation === 'comment' || operation === 'propose')) {
    return hasAnyPermission(principal.permissions, [`${schema}.read`]);
  }
  return hasAnyPermission(principal.permissions, [`${schema}.${action}`]);
};
