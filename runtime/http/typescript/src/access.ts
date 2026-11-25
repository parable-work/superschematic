import type { Authenticator, GrantReach, Principal, PrincipalKind } from './auth.js';
import type { RequestContext } from './operation.js';
import { unauthorized } from './problem.js';
import { sha256 } from './sha256.js';

/*
Per-record access (D156): the grants that let a caller who is no user reach
rows, and the access log.

A grant is a row of the database's @accessGrants table: its holder (a
party's or an agent run's id) and the holder's kind, the role it acts with,
its target row, when it starts and when it ends (a date, or the target's
field reaching a state), when it was revoked, and the SHA-256 of the token
its holder presents. The role is a row of the UserRole table: its
permissions are what the holder may do, and its reaches, "Type:path"
entries, which rows beside the target the holder reaches: a row of Type
whose path leads to the target. grantAuthenticator establishes the
holder of a grant's token as the principal of its kind, with the role's
permissions and the grant's reach, on every call, so a grant that ended or
was revoked is refused at the very next one. The repositories keep every
read of such a principal to the rows its grants reach (orm.ts,
accessConditions), through the same relation paths a row rule follows.

The access log is a row of the database's @accessLog table for each
sensitive access: a reveal of an Encrypted<T> field, a read an agent run
made, and every call made with an API key (D114) or a grant's token.
accessLogOf records them through the ORM, as the system actor, so a
caller's own access never hides its log row.

Nothing here imports pg: the ORM is the generated createOrm's, taken as a
value, and a repository is found by what its table's definition says it is
(TableDefinition.access).
*/

/** What a grant reaches, read whole: the row of the @accessGrants table. */
export interface GrantRow {
  readonly id: string;
  readonly holder: string;
  readonly holderKind: string;
  readonly role: string;
  readonly targetType: string;
  readonly targetId: string;
  readonly startsAt: Date;
  readonly endsAt: Date | null;
  readonly endsWhenField: string | null;
  readonly endsWhenState: string | null;
  readonly revokedAt: Date | null;
}

/** One entry of the access log. */
export interface AccessLogEntry {
  readonly kind: 'reveal' | 'agentRead' | 'keyCall' | 'grantCall';
  readonly principal?: string;
  readonly principalKind?: PrincipalKind;
  readonly actingFor?: string;
  readonly credential?: string;
  readonly operation?: string;
  readonly targetType?: string;
  readonly targetId?: string;
  readonly field?: string;
}

/** Where the access log is written: accessLogOf's, or the project's own. */
export interface AccessLog {
  record(entry: AccessLogEntry): Promise<void>;
}

type Values = Record<string, unknown>;

/** A repository as this module runs it: the generated repositories' members it reads and writes through. */
interface RepositoryShape {
  readonly def?: { readonly name?: string; readonly key?: string; readonly access?: string };
  findOne(filter: Values): Promise<Values | null>;
  findMany(filter: Values, options?: Values): Promise<{ rows: Values[] }>;
  createOne(input: Values): Promise<Values>;
  updateOne(id: unknown, update: Values): Promise<Values>;
}

/** An ORM as this module runs it: its repositories by member, and asSystem. */
interface OrmShape {
  asSystem(): OrmShape;
}

/** The repositories of orm, by member: its members that hold a table's definition. */
function repositoriesOf(orm: object): [string, RepositoryShape][] {
  return Object.entries(orm).filter((entry): entry is [string, RepositoryShape] => {
    const value = entry[1] as RepositoryShape | undefined;
    return typeof value === 'object' && value !== null && typeof value.def === 'object' && typeof value.findOne === 'function';
  });
}

/** The repository of orm whose table is to access what kind says, or undefined. */
function accessRepository(orm: object, kind: 'grants' | 'log' | 'roles'): RepositoryShape | undefined {
  return repositoriesOf(orm).find(([, repository]) => repository.def?.access === kind)?.[1];
}

/** The repository of orm whose table is the type name, or undefined. */
function repositoryOfType(orm: object, type: string): RepositoryShape | undefined {
  return repositoriesOf(orm).find(([, repository]) => repository.def?.name === type)?.[1];
}

/** The ORM as the system actor: what access reads and writes through, which no caller's audience or grants hold. */
function system(orm: object): object {
  return (orm as OrmShape).asSystem() as object;
}

/** The prefix of a grant's token, which tells a person and a secret scanner what it is. */
export const GRANT_TOKEN_PREFIX = 'grant_';

function base64url(bytes: Uint8Array): string {
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/u, '');
}

/** The SHA-256 of a token's text, as the grant's tokenHash holds it: lowercase hex. */
export function grantTokenHash(token: string): string {
  return [...sha256(new TextEncoder().encode(token))].map(byte => byte.toString(16).padStart(2, '0')).join('');
}

/** What issueGrant takes: the grant's row, but its key, its token's hash and its stamps. */
export interface IssueGrantInput {
  readonly holder: string;
  /** party or agent; user for a calendar feed (D152), whose token reads its feed alone. */
  readonly holderKind: 'party' | 'agent' | 'user';
  readonly role: string;
  readonly targetType: string;
  readonly targetId: string;
  readonly startsAt?: Date;
  readonly endsAt?: Date | null;
  readonly endsWhen?: { readonly field: string; readonly state: string };
}

/**
 * Makes a grant (D156) and its token, which only the holder is given:
 * `grant_` and 43 characters of base64url, 32 random bytes, stored as the
 * SHA-256 of the whole text. It writes through orm as it is, so the grant
 * records who made it, and a caller the target is no row of reaches none.
 */
export async function issueGrant(orm: object, input: IssueGrantInput): Promise<{ readonly grant: Values; readonly token: string }> {
  const grants = accessRepository(orm, 'grants');
  if (!grants) throw new Error('issueGrant: the ORM has no @accessGrants table');
  const random = new Uint8Array(32);
  crypto.getRandomValues(random);
  const token = GRANT_TOKEN_PREFIX + base64url(random);
  const grant = await grants.createOne({
    holder: input.holder,
    holderKind: input.holderKind,
    role: input.role,
    targetType: input.targetType,
    targetId: input.targetId,
    startsAt: input.startsAt ?? new Date(),
    endsAt: input.endsAt ?? null,
    endsWhenField: input.endsWhen?.field ?? null,
    endsWhenState: input.endsWhen?.state ?? null,
    tokenHash: grantTokenHash(token),
    createdAt: new Date(),
  });
  return { grant, token };
}

/** Revokes a grant: the next call its token makes is refused. */
export async function revokeGrant(orm: object, id: string, at = new Date()): Promise<void> {
  const grants = accessRepository(system(orm), 'grants');
  if (!grants) throw new Error('revokeGrant: the ORM has no @accessGrants table');
  await grants.updateOne(id, { revokedAt: at });
}

/**
 * Why grant is not active at now, or undefined when it is (D156): it has
 * not started, it has ended by its date, it was revoked, or its target's
 * field holds the state that ends it, or its target is gone.
 */
export async function grantEnded(orm: object, grant: GrantRow, now: Date): Promise<string | undefined> {
  if (grant.revokedAt !== null && grant.revokedAt !== undefined) return 'revoked';
  if (new Date(grant.startsAt).getTime() > now.getTime()) return 'not started';
  if (grant.endsAt !== null && grant.endsAt !== undefined && new Date(grant.endsAt).getTime() <= now.getTime()) return 'expired';
  if (grant.endsWhenField) {
    const target = repositoryOfType(system(orm), grant.targetType);
    const key = target?.def?.key;
    if (!target || !key) return 'target gone';
    const row = await target.findOne({ [key]: { eq: grant.targetId } });
    if (row === null) return 'target gone';
    if (String(row[grant.endsWhenField]) === grant.endsWhenState) return 'target ended';
  }
  return undefined;
}

/** A role's reaches, "Type:path" each, by type: the paths a grant of the role reaches rows of the type by. */
export function reachesOf(entries: readonly string[] | null | undefined): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const entry of entries ?? []) {
    const at = entry.indexOf(':');
    if (at <= 0 || at === entry.length - 1) continue;
    const type = entry.slice(0, at);
    (out[type] ??= []).push(entry.slice(at + 1));
  }
  return out;
}

/** What activeGrants keeps: a holder's grants, a target's, or every one. */
export interface ActiveGrantsFilter {
  readonly holder?: string;
  readonly targetType?: string;
  readonly targetId?: string;
  readonly now?: Date;
}

/**
 * The read of the active grants (D156): each grant that has started, has
 * not ended by its date or its target's state, and was not revoked, of a
 * holder, of a target, or every one, read as the system actor.
 */
export async function activeGrants(orm: object, filter: ActiveGrantsFilter = {}): Promise<Values[]> {
  const db = system(orm);
  const grants = accessRepository(db, 'grants');
  if (!grants) throw new Error('activeGrants: the ORM has no @accessGrants table');
  const now = filter.now ?? new Date();
  const where: Values = {
    revokedAt: { isNull: true },
    startsAt: { lte: now },
    ...(filter.holder !== undefined ? { holder: { eq: filter.holder } } : {}),
    ...(filter.targetType !== undefined ? { targetType: { eq: filter.targetType } } : {}),
    ...(filter.targetId !== undefined ? { targetId: { eq: filter.targetId } } : {}),
    anyOf: [{ endsAt: { isNull: true } }, { endsAt: { gt: now } }],
  };
  const { rows } = await grants.findMany(where, { count: false, limit: 10000 });
  const active: Values[] = [];
  for (const row of rows) {
    if ((await grantEnded(db, row as unknown as GrantRow, now)) === undefined) active.push(row);
  }
  return active;
}

/** What grantAuthenticator takes. */
export interface GrantAuthenticatorOptions {
  /** The ORM of the database that holds the @accessGrants table and the UserRole table its roles are rows of. */
  readonly orm: object;
  /** Establishes every caller that presents no grant's token: the identity service's, say. Absent, such a request has no principal. */
  readonly inner?: Authenticator;
  /** The time a grant is held to; the clock's unless set. */
  readonly now?: () => Date;
}

/**
 * The router's Authenticator for grants (D156): a request whose
 * Authorization is `Bearer grant_...` runs as the grant's holder, a party
 * or an agent run, with its role's permissions and the grant's reach;
 * every other request is inner's to establish. The grant is read on every
 * call, so a grant that ended, by its date or its target's state, or was
 * revoked is refused 401 unauthorized at the very next call, as an unknown
 * token is.
 */
export function grantAuthenticator(options: GrantAuthenticatorOptions): Authenticator {
  const db = system(options.orm);
  const grants = accessRepository(db, 'grants');
  const roles = accessRepository(db, 'roles');
  if (!grants) throw new TypeError('grantAuthenticator: the ORM has no @accessGrants table');
  if (!roles) throw new TypeError('grantAuthenticator: the ORM has no UserRole table, whose rows a grant\'s role names');
  const authenticate = async (ctx: RequestContext): Promise<Principal | null> => {
    const header = ctx.headers.get('authorization') ?? '';
    const token = header.startsWith('Bearer ') ? header.slice('Bearer '.length).trim() : '';
    if (!token.startsWith(GRANT_TOKEN_PREFIX)) return options.inner ? options.inner(ctx) : null;
    const grant = (await grants.findOne({ tokenHash: { eq: grantTokenHash(token) } })) as unknown as GrantRow | null;
    if (grant === null) throw unauthorized();
    // A calendar feed's token reads its feed alone (D152): a leaked feed URL
    // is no credential of any route.
    if (grant.targetType === 'CalendarFeed' || grant.targetType.startsWith('CalendarFeed;')) throw unauthorized("A calendar feed's token reads its feed alone");
    const ended = await grantEnded(db, grant, options.now?.() ?? new Date());
    if (ended !== undefined) throw unauthorized(`The grant has ${ended === 'not started' ? 'not started' : 'ended'}`);
    const role = await roles.findOne({ name: { eq: grant.role } });
    const permissions = (role?.permissions as readonly string[] | null | undefined) ?? [];
    const reach: GrantReach = {
      id: grant.id,
      targetType: grant.targetType,
      targetId: grant.targetId,
      reaches: reachesOf(role?.reaches as readonly string[] | null | undefined),
    };
    return {
      subject: grant.holder,
      kind: grant.holderKind === 'agent' ? 'agent' : 'party',
      permissions: [...permissions],
      grants: [reach],
      credential: { kind: 'grant', id: grant.id },
    };
  };
  return Object.assign(authenticate, { readsAuthorization: true });
}

/**
 * The access log over orm's @accessLog table (D156), written as the system
 * actor; undefined for an ORM with none, so a router that takes it logs
 * nothing.
 */
export function accessLogOf(orm: object | undefined): AccessLog | undefined {
  if (orm === undefined || orm === null) return undefined;
  const log = accessRepository(orm, 'log');
  if (!log) return undefined;
  const db = system(orm);
  const repository = accessRepository(db, 'log')!;
  return {
    async record(entry: AccessLogEntry): Promise<void> {
      await repository.createOne({ at: new Date(), ...entry });
    },
  };
}

/** The access log entry of a call (D156): an API key's or a grant's call, and an agent run's read; undefined for any other. */
export function callEntry(principal: Principal | null, operation: string, method: string): AccessLogEntry | undefined {
  if (!principal) return undefined;
  const who = {
    principal: principal.subject,
    principalKind: principal.kind ?? 'user',
    ...(principal.actingFor ? { actingFor: principal.actingFor.subject } : {}),
    operation,
  } as const;
  if (principal.kind === 'agent' && (method === 'GET' || method === 'HEAD')) {
    return { kind: 'agentRead', ...who, ...(principal.credential ? { credential: principal.credential.id } : {}) };
  }
  if (principal.credential?.kind === 'apiKey') return { kind: 'keyCall', ...who, credential: principal.credential.id };
  if (principal.credential?.kind === 'grant') return { kind: 'grantCall', ...who, credential: principal.credential.id };
  return undefined;
}
