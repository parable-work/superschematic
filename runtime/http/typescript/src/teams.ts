import type { Authenticator, Principal } from './auth.js';
import type { RequestContext } from './operation.js';
import { ormDialect } from './orm.js';
import { uuidToDb, uuidValue } from './uuid.js';

/*
A team in an audience (#735). A database's @team table holds teams, whose
rows are principals an @audience row's audienceNamed may name by their ids,
and its @teamMembers table their membership: each row puts a member, a
user's id or another team's, in a team.

Teams nest: a caller's ids are its own and those of every team it belongs
to, directly or through another team. teamAuthenticator reads them on every
call, with the membership table's one recursive statement (sqlutil's
TeamsSQL, its definition's teams), and puts them on the principal
(Principal.teams), so a read of an @audience table, one overlap of a row's
effective audience with the caller's ids (orm.ts, audienceConditions),
admits a row named for a team to each of its members. The statement's
UNION drops a team it has found already, so a cycle in the membership ends
the read. Nothing is cached: a member removed from a team, or a team from
another, is refused the team's rows at its next call, as a revoked grant is.

An agent run's teams are those of whom it acts for, beside its own, as its
audience is.
*/

/** The options of teamAuthenticator. */
export interface TeamAuthenticatorOptions {
  /** The generated ORM of a database with a @teamMembers table, which the teams are read through as the system actor. */
  readonly orm: object;
  /** The authenticator whose principal gets its teams; every request goes to it. */
  readonly inner: Authenticator;
}

/** A table's definition as this module reads it. */
interface TeamsDefinition {
  readonly access?: string;
  /** The statement that reads the teams the ids of its one parameter are members of, through every nesting. */
  readonly teams?: string;
}

interface OrmShape {
  asSystem(): OrmShape;
  query(text: string, values: readonly unknown[]): Promise<{ rows: Record<string, unknown>[] }>;
}

/** The statement of orm's @teamMembers table, or undefined for an ORM with none. */
function teamsStatement(orm: object): string | undefined {
  for (const value of Object.values(orm)) {
    const def = (value as { def?: TeamsDefinition } | null)?.def;
    if (typeof value === 'object' && value !== null && def?.access === 'teamMembers' && def.teams) return def.teams;
  }
  return undefined;
}

/**
 * The teams ids are members of, directly or through another team, once
 * each, by their keys as the database writes them, read through orm's
 * @teamMembers table as the system actor (#735). An id that is no UUID
 * belongs to no team; a cycle in the membership ends the read.
 */
export async function teamsOf(orm: object, ids: readonly string[]): Promise<string[]> {
  const statement = teamsStatement(orm);
  if (statement === undefined) throw new TypeError('teamsOf: the ORM has no @teamMembers table');
  const starts = ids.filter(id => uuidValue(id) !== undefined).map(id => uuidToDb(id));
  if (starts.length === 0) return [];
  const db = (orm as OrmShape).asSystem();
  const param = ormDialect(orm) === 'sqlite' ? JSON.stringify(starts) : starts;
  const { rows } = await db.query(statement, [param]);
  return rows.map(row => uuidToDb(String(row.id)));
}

/**
 * The authenticator that puts a caller's teams on the principal inner
 * establishes (#735): Principal.teams, read on every call (teamsOf) from
 * its subject and, for an agent run, whom it acts for. A request inner
 * establishes no principal for stays anonymous; a principal whose ids
 * belong to no team gets none.
 */
export function teamAuthenticator(options: TeamAuthenticatorOptions): Authenticator {
  const { orm, inner } = options;
  if (teamsStatement(orm) === undefined) throw new TypeError('teamAuthenticator: the ORM has no @teamMembers table');
  const authenticate = async (ctx: RequestContext): Promise<Principal | null> => {
    const principal = await inner(ctx);
    if (principal === null || !principal.subject) return principal;
    const ids = [principal.subject];
    if (principal.kind === 'agent' && principal.actingFor?.subject) ids.push(principal.actingFor.subject);
    const teams = await teamsOf(orm, ids);
    return teams.length === 0 ? principal : { ...principal, teams };
  };
  return inner.readsAuthorization ? Object.assign(authenticate, { readsAuthorization: true }) : authenticate;
}
