import { activeGrants, grantTarget } from './access.js';
import { covers } from './auth.js';
import type { RequestContext } from './operation.js';
import { forbidden, unauthorized } from './problem.js';
import { uuidValue } from './uuid.js';

/*
The handler of a generated router's active grants (#735): the route the
loader adds for an API's @accessGrantsOperations set, GET grants/active,
which lists the grants active now through activeGrants, over the
database's @accessGrants table, with no implementation method. The route
runs every step an implemented route runs (the service step, the
end-user step and the class's own permissions among them); the handler
then decides which of three listers the caller is, and lists exactly the
grants that one may see:

  - a caller who holds the declaration's permission (a grant of one that
    covers it too) lists every active grant the query keeps: its holder,
    targetType and targetId, each optional;
  - the owner of the row the query names by targetType and targetId (the
    member that holds its table's owner, its @audience owner else its
    createdBy, read as the system actor) lists the active grants on that
    row, the query's holder keeping them further;
  - a holder lists its own: a query whose holder is the caller's own
    subject, its targetType and targetId keeping them further.

Any other request is refused 403 forbidden, before a grant is read: a
query that names no holder, or another's, and no row the caller owns. A
targetType no owner is known of, or a row that is not there, names no row
the caller owns. A grant ended by its date, by its target's state or by a
revocation is never listed. Each grant is answered without its token's
hash or its revocation.

It imports nothing from pg or the repositories: it takes the ORM as a
value, as activeGrants does.
*/

/** The active grants' route as the generated router carries it: the permission whose holder lists every grant, and the member that holds the owner of each table's rows. */
export interface AccessGrantsOperation {
  readonly permission: string;
  readonly owners: Readonly<Record<string, string>>;
}

/** A route's request as the handler reads it. */
export interface AccessGrantsRequest {
  readonly query: Readonly<Record<string, unknown>>;
}

type Values = Readonly<Record<string, unknown>>;

/** The members of a grant the route answers: its AccessGrant view. */
const VIEW = ['id', 'holder', 'holderKind', 'role', 'targetType', 'targetId', 'startsAt', 'endsAt', 'endsWhenField', 'endsWhenState'] as const;

/** A query argument as text, undefined when absent or empty. */
function textOf(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined;
  const text = String(value);
  return text === '' ? undefined : text;
}

/** Whether two ids name one principal or row: equal, or one UUID in either of its forms. */
function sameId(a: unknown, b: string): boolean {
  if (a === undefined || a === null) return false;
  const text = String(a);
  if (text === b) return true;
  const x = uuidValue(text);
  return x !== undefined && x === uuidValue(b);
}

/**
 * Lists the active grants the request's caller may see (#735), through
 * activeGrants over orm, and answers each as its AccessGrant view; refuses
 * a caller who is none of the three listers 403, and a request with no
 * caller 401.
 */
export async function accessGrantsOperation(orm: object, operation: AccessGrantsOperation, ctx: RequestContext, request: AccessGrantsRequest): Promise<unknown> {
  const principal = ctx.principal;
  if (!principal || principal.subject === '') throw unauthorized('the active grants take a caller');
  const holder = textOf(request.query.holder);
  const targetType = textOf(request.query.targetType);
  const targetId = textOf(request.query.targetId);
  const filter = {
    ...(holder !== undefined ? { holder } : {}),
    ...(targetType !== undefined ? { targetType } : {}),
    ...(targetId !== undefined ? { targetId } : {}),
  };
  if (!(await mayList(orm, operation, principal.subject, principal.permissions, filter))) {
    throw forbidden(`only a holder of ${operation.permission}, the owner of the target a query names, or a holder asking for its own grants lists the active grants`);
  }
  const rows = await activeGrants(orm, filter);
  return rows.map(row => Object.fromEntries(VIEW.map(member => [member, row[member] ?? null])));
}

/** Whether a caller of subject, holding permissions, may list the active grants filter keeps: the administrator, the target's owner or the holder (#735). */
async function mayList(orm: object, operation: AccessGrantsOperation, subject: string, permissions: readonly string[], filter: { holder?: string; targetType?: string; targetId?: string }): Promise<boolean> {
  if (permissions.some(granted => covers(granted, operation.permission))) return true;
  if (filter.targetType !== undefined && filter.targetId !== undefined) {
    const member = operation.owners[filter.targetType];
    if (member !== undefined) {
      const row: Values | undefined = await grantTarget(orm, filter.targetType, filter.targetId);
      if (row !== undefined && sameId(row[member], subject)) return true;
    }
  }
  return filter.holder !== undefined && sameId(filter.holder, subject);
}
