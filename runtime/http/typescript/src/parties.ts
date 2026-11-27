import type { GrantReach, Principal } from './auth.js';
import { activeGrants, grantEnded, permissionsOf, reachesOf, rolesByName, type GrantRow } from './access.js';
import { uuidValue } from './uuid.js';

/*
Parties (D157): the principal a @party row is, and whether a party's
channel identity still stands.

A party is a row of a @party table, a principal of kind party (D156) that
acts only through the grants it holds: the rows of the @accessGrants table
whose holder is its key. partyPrincipal resolves it the way
grantAuthenticator resolves a grant's token, with every active grant's
reach (or one grant's), and the permissions of those grants' roles.

An identity whose party's grants have all ended since it was verified has
lapsed: a phone number is reassigned, an address changes hands, and a
message from it is no longer the party's until it is verified again. It is
decided from the rows, with no stamp to keep: the party holds no active
grant, and some grant of it did not end, by its date or its revocation,
before the identity was verified. A grant that ended by its target's state
counts as ended after, since the row does not say when.

Nothing here imports pg: the ORM is the generated createOrm's, taken as a
value, and a repository is found by what its table's definition says it is.
*/

/** A party: its table, by its type's name, and its row's key. */
export interface PartyRef {
  readonly table: string;
  readonly key: string;
}

type Values = Record<string, unknown>;

interface RepositoryShape {
  readonly def?: { readonly name?: string; readonly key?: string; readonly access?: string; readonly party?: { readonly title?: string } };
  findOne(filter: Values): Promise<Values | null>;
  findMany(filter: Values, options?: Values): Promise<{ rows: Values[] }>;
}

function repositoriesOf(orm: object): [string, RepositoryShape][] {
  return Object.entries(orm).filter((entry): entry is [string, RepositoryShape] => {
    const value = entry[1] as RepositoryShape | undefined;
    return typeof value === 'object' && value !== null && typeof value.def === 'object' && typeof value.findOne === 'function';
  });
}

function system(orm: object): object {
  return (orm as { asSystem(): object }).asSystem();
}

/** The repository member of orm whose table is to access what kind says, or undefined. */
function accessMember(orm: object, kind: 'grants' | 'roles'): RepositoryShape | undefined {
  return repositoriesOf(orm).find(([, repository]) => repository.def?.access === kind)?.[1];
}

/** The @party tables of orm, by type name: each one's repository member. */
export function partyTablesOf(orm: object): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [member, repository] of repositoriesOf(orm)) {
    if (repository.def?.party && repository.def.name) out[repository.def.name] = member;
  }
  return out;
}

/** What partyPrincipal takes beside the party. */
export interface PartyPrincipalOptions {
  /** The one grant the principal acts under; every active grant of the party unless set. */
  readonly grant?: string;
  /** The time the grants are held to; the clock's unless set. */
  readonly now?: Date;
}

/**
 * The principal party is (D157): kind party, its subject the row's key,
 * the reach of each of its active grants (or of the one grant named, when
 * it is active), and the permissions of those grants' roles. A party with
 * no active grant is a principal that reaches nothing.
 */
export async function partyPrincipal(orm: object, party: PartyRef, options: PartyPrincipalOptions = {}): Promise<Principal & { readonly grants: readonly GrantReach[] }> {
  const db = system(orm);
  const roles = accessMember(db, 'roles');
  const now = options.now ?? new Date();
  const held = (await activeGrants(db, { holder: party.key, now })).filter(grant => options.grant === undefined || sameUUID(String(grant.id), options.grant));
  const byName = await rolesByName(roles, held.map(grant => grant.role));
  const grants: GrantReach[] = held.map(grant => ({
    id: String(grant.id),
    targetType: String(grant.targetType),
    targetId: String(grant.targetId),
    reaches: reachesOf(byName.get(String(grant.role))?.reaches as string[] | null | undefined),
  }));
  const permissions = permissionsOf(held.map(grant => byName.get(String(grant.role))));
  return { subject: party.key, kind: 'party', permissions, grants };
}

/** A channel identity as lapse reads it. */
export interface IdentityStanding {
  readonly principalKind: string;
  readonly principal: string;
  readonly verifiedAt?: Date | string | null;
  readonly createdAt?: Date | string | null;
}

/**
 * Whether identity has lapsed (D157): it is a party's, the party holds no
 * active grant, and some grant of the party did not end before the
 * identity was verified. A user's identity never lapses, and neither does
 * a party's that has held no grant since it was verified.
 */
export async function identityLapsed(orm: object, identity: IdentityStanding, now = new Date()): Promise<boolean> {
  if (identity.principalKind !== 'party') return false;
  const db = system(orm);
  const grants = accessMember(db, 'grants');
  if (!grants) return false;
  const rows = (await grants.findMany({ holder: { eq: identity.principal } }, { limit: 1000, count: false })).rows as unknown as GrantRow[];
  if (rows.length === 0) return false;
  const verified = new Date((identity.verifiedAt ?? identity.createdAt ?? 0) as string | Date).getTime();
  let endedSince = false;
  for (const grant of rows) {
    if ((await grantEnded(db, grant, now)) === undefined) {
      // A grant not started yet is no active one, and it lapses nothing.
      return false;
    }
    if (new Date(grant.startsAt).getTime() > now.getTime()) continue;
    const endedAt = [grant.endsAt, grant.revokedAt]
      .filter((at): at is Date => at !== null && at !== undefined)
      .map(at => new Date(at).getTime())
      .filter(at => at <= now.getTime());
    const end = endedAt.length > 0 ? Math.min(...endedAt) : Number.POSITIVE_INFINITY;
    if (end > verified) endedSince = true;
  }
  return endedSince;
}

/** Whether a and b are the same id, whatever form each UUID came in. */
export function sameUUID(a: string, b: string): boolean {
  const x = uuidValue(a);
  return x !== undefined ? x === uuidValue(b) : a === b;
}
