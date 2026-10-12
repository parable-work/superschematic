import { covers, hasAnyPermission, type PermissionMatcher } from '../auth.js';
import type { OperationSpec } from '../operation.js';

/*
Roles and permissions (D50): the form a role's permissions are checked for
when it is written, a principal's permissions from its roles, the rule that
no one grants what they do not hold, and capabilities, whether each route
of the API admits a caller, by the router's own rule: the operation table's
auth requirements and the permission matcher (hasAnyPermission unless the
project passes another).
*/

/**
 * Whether p has a permission's form: dotted segments of ASCII letters,
 * digits, '_' and '-', none empty ("orders.read", "identity.users.write").
 */
export function validPermission(p: string): boolean {
  return /^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$/u.test(p);
}

/** A role as a principal holds it: its name and permissions. */
export interface PermissionsOf {
  readonly permissions: readonly string[];
}

/**
 * The permissions roles carry, without repeats: each role's in its order,
 * the roles in the order given (a store lists a user's roles by name), and
 * a permission kept where it first appears. A permission another covers is
 * still listed.
 */
export function effectivePermissions(roles: readonly PermissionsOf[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const role of roles) {
    for (const p of role.permissions) {
      if (!seen.has(p)) {
        seen.add(p);
        out.push(p);
      }
    }
  }
  return out;
}

/**
 * The permissions of given that no permission of held covers, in given's
 * order and without repeats. A caller may write a role with given, or grant
 * a role carrying it, only when it is empty: no one grants what they do not
 * hold. A held permission covers a required one when they are equal or the
 * required one continues it after a dot.
 */
export function uncovered(held: readonly string[], given: readonly string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const p of given) {
    if (seen.has(p)) continue;
    seen.add(p);
    if (!held.some(h => covers(h, p))) out.push(p);
  }
  return out;
}

/** What capabilities needs to know of one operation of the API. */
export interface IdentityRoute {
  /** The operation's OpenAPI operation id, its key in Capabilities. */
  readonly operationId: string;
  /** The route needs a caller (@auth). */
  readonly requiresAuth: boolean;
  /** Its @requirePermission list. A route with any needs a caller. */
  readonly permissions: readonly string[];
  /** @requireOwnership: the route needs a caller; whether they own a resource is the implementation's. */
  readonly requireOwnership: boolean;
  /** @requireService: only a service calls it, so capabilities leaves it out. */
  readonly serviceOnly: boolean;
}

/**
 * Whether a route admits an authenticated caller holding permissions: one
 * with no permissions admits every authenticated caller, one with
 * permissions a caller the matcher accepts.
 */
export function routeAdmits(route: IdentityRoute, permissions: readonly string[], matcher: PermissionMatcher = hasAnyPermission): boolean {
  if (route.permissions.length === 0) return true;
  return matcher(permissions, route.permissions);
}

/**
 * For each route an end user may call (every route but a service-only
 * one), whether it admits an authenticated caller holding permissions,
 * keyed by operation id, the keys in order.
 */
export function capabilitiesOf(
  routes: readonly IdentityRoute[],
  permissions: readonly string[],
  matcher: PermissionMatcher = hasAnyPermission
): Record<string, boolean> {
  const out: Record<string, boolean> = {};
  const sorted = routes.filter(route => !route.serviceOnly).sort((a, b) => compareCodePoints(a.operationId, b.operationId));
  for (const route of sorted) out[route.operationId] = routeAdmits(route, permissions, matcher);
  return out;
}

/** Compares strings by code point, the order of their UTF-8 bytes, as Go compares strings. */
export function compareCodePoints(a: string, b: string): number {
  const x = [...a];
  const y = [...b];
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    const d = x[i]!.codePointAt(0)! - y[i]!.codePointAt(0)!;
    if (d !== 0) return d;
  }
  return x.length - y.length;
}

/**
 * Go's codegen.ToPascalCase, which names an operation's handler, and so its
 * OpenAPI operation id: a name with '-' or '_' joins its parts, each
 * lowercased and capitalized; a name in upper case alone is lowercased
 * first; any other has its first letter capitalized.
 */
function toPascalCase(s: string): string {
  if (s === '') return s;
  if (/[-_]/u.test(s)) {
    return s
      .split(/[-_]/u)
      .filter(part => part !== '')
      .map(part => part.toLowerCase())
      .map(part => part[0]!.toUpperCase() + part.slice(1))
      .join('');
  }
  const word = s === s.toUpperCase() ? s.toLowerCase() : s;
  return word[0]!.toUpperCase() + word.slice(1);
}

/** The OpenAPI operation id of a generated operation: its namespace's and its name's PascalCase, then Handler. */
export function operationIdOf(spec: Pick<OperationSpec, 'namespace' | 'name'>): string {
  return `${toPascalCase(spec.namespace)}${toPascalCase(spec.name)}Handler`;
}

/**
 * The routes of a generated operation table, by the router's rule: a public
 * route, or one that requires no caller, admits anyone; another requires a
 * caller, and one of its permissions when it lists any (@requireOwnership
 * is a requirement of a caller in the table); an @requireService route is
 * service-only. An @allowService route is an end user's too.
 */
export function routesOf(specs: Iterable<OperationSpec> | Readonly<Record<string, OperationSpec>>): IdentityRoute[] {
  const list: Iterable<OperationSpec> = Symbol.iterator in Object(specs) ? (specs as Iterable<OperationSpec>) : Object.values(specs);
  const routes: IdentityRoute[] = [];
  for (const spec of list) {
    const gated = !spec.auth.public && spec.auth.required;
    routes.push({
      operationId: operationIdOf(spec),
      requiresAuth: gated,
      permissions: gated ? [...spec.auth.permissions] : [],
      requireOwnership: false,
      serviceOnly: spec.service?.mode === 'require',
    });
  }
  return routes;
}
