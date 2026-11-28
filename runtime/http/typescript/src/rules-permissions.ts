import type { RulesPermissions, RulesStore } from './rules.js';
import { quoteIdentifier as quote } from './sql-text.js';
import { base62UUID, hyphenateUUID, uuidValue } from './uuid.js';

/*
The core's permissions read (D200): what an Approvals requirement with
recheckPermission reads of the subjects who signed earlier, answered from
the layout's identity tables as the identity runtime's store reads a
principal's roles.
*/

const UUID_TYPES: ReadonlySet<string> = new Set(['Identity.UUID', 'Identity.UserID']);

/**
 * The permissions each subject a user row holds has now: those of every
 * role a grant gives the user, each once, and none while the user's
 * credential is disabled or pending. A subject no user row holds, or that
 * is no key of the user table, is left out, and the core holds it to no
 * permission; so is every subject of a layout that names no identity
 * tables (from undefined), whose database the adapter cannot answer for.
 */
export async function readPermissions(store: RulesStore, from: RulesPermissions | undefined, subjects: readonly string[]): Promise<Record<string, string[]>> {
  const out: Record<string, string[]> = {};
  if (from === undefined || subjects.length === 0) return out;
  const uuid = UUID_TYPES.has(from.user.type);
  const p = (n: number) => (store.dialect === 'postgres' ? `$${n}` : `?${n}`);
  // Each subject by the key its user row would hold, as the read matches
  // a row's key: a UUID's base62 wire form, any other key's text.
  const bySubject: [string, string][] = [];
  const values: string[] = [];
  for (const subject of subjects) {
    if (uuid) {
      const n = uuidValue(subject);
      if (n === undefined) continue;
      bySubject.push([subject, base62UUID(n)]);
      values.push(hyphenateUUID(n));
    } else {
      bySubject.push([subject, subject]);
      values.push(subject);
    }
  }
  if (values.length === 0) return out;
  const user = quote(from.user.column);
  const text =
    `SELECT u.${user} AS "user", r.${quote(from.rolePermissions)} AS "held" FROM ${quote(from.users)} AS u` +
    ` LEFT JOIN ${quote(from.credentials)} AS c ON c.${quote(from.credentialUser)} = u.${user}` +
    ` LEFT JOIN ${quote(from.grants)} AS g ON g.${quote(from.grantUser)} = u.${user} AND c.${quote(from.disabledAt)} IS NULL AND c.${quote(from.pendingAt)} IS NULL` +
    ` LEFT JOIN ${quote(from.roles)} AS r ON r.${quote(from.roleKey)} = g.${quote(from.grantRole)}` +
    ` WHERE u.${user} IN (${values.map((_, i) => p(i + 1)).join(', ')}) ORDER BY u.${user}, r.${quote(from.roleKey)}`;
  const held = new Map<string, string[]>();
  for (const row of await store.query(text, values)) {
    const key = keyOf(row['user'], uuid);
    if (key === undefined) throw new Error(`rules: ${from.users}'s key in a permissions read is no key`);
    const permissions = held.get(key) ?? [];
    held.set(key, permissions);
    for (const permission of listOf(row['held'], from)) {
      if (!permissions.includes(permission)) permissions.push(permission);
    }
  }
  for (const [subject, matched] of bySubject) {
    const permissions = held.get(matched);
    if (permissions !== undefined) out[subject] = permissions;
  }
  return out;
}

/** A user key a row holds, as subjects are matched by it. */
function keyOf(value: unknown, uuid: boolean): string | undefined {
  if (value === null || value === undefined) return undefined;
  const text = typeof value === 'string' ? value : String(value);
  if (!uuid) return text;
  const n = uuidValue(text);
  return n === undefined ? undefined : base62UUID(n);
}

/** A role's permissions a row holds: pg's array of a TEXT[], SQLite's JSON text of one; none for a user who holds no role. */
function listOf(value: unknown, from: RulesPermissions): string[] {
  if (value === null || value === undefined) return [];
  let list: unknown = value;
  if (typeof value === 'string') {
    try {
      list = JSON.parse(value);
    } catch {
      list = undefined;
    }
  }
  if (!Array.isArray(list)) throw new Error(`rules: ${from.roles}.${from.rolePermissions} is no list of permissions`);
  return list.filter((item): item is string => typeof item === 'string');
}
