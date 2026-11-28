/*
Per-record access (D156): who may see a row of a table, the grants that let a
caller who is no user reach rows, and the log of sensitive reads.
*/

import type { RelationKeyOf } from "./wrappers";

/** Who may see a row: every principal the route admits, the owner alone, the owner and the principals it names, or what its parent sees. */
export type AudienceValue = "everyone" | "private" | "named" | "inherit";

/** What an agent run may do with a row: what its role allows, read it only, or nothing. */
export type AgentAccessValue = "followRole" | "readOnly" | "none";

/** The options of @audience. */
export interface AudienceOptions<T> {
  /**
   * The field that holds the row's owner, the one principal a private row is
   * visible to: a field that holds a UUID, or a to-one relation, by its
   * name or by the field that holds its key (ownerId). Unset, the owner is
   * createdBy, which the table then declares.
   */
  readonly owner?: (keyof T & string) | RelationKeyOf<T>;
  /**
   * A to-one relation to a table that is @audience too: a row whose audience
   * is inherit sees what its parent sees, and keeps doing so when the
   * parent's audience changes.
   */
  readonly inheritFrom?: keyof T & string;
  /** The audience of a row whose create names none: inherit with inheritFrom, everyone without, unless set. */
  readonly default?: AudienceValue;
  /** What an agent run may do with a row whose create names nothing: followRole unless set. */
  readonly agents?: AgentAccessValue;
}

/**
 * Gives a DB table an audience on each row (D156): who may see it and what
 * an agent run may do with it.
 *
 * ```ts
 * @audience<Folder>({ owner: "ownerId" })
 * export abstract class Folder { ... }
 *
 * @audience<Document>({ owner: "ownerId", inheritFrom: "folder", agents: "readOnly" })
 * export abstract class Document { ... }
 * ```
 *
 * The build adds the columns audience, audienceNamed and agentAccess, which
 * a row's writes set, and effectiveAudience and effectiveAgents, which a
 * trigger keeps: the audience resolved through the parent, recomputed for
 * every child when a parent changes. Every read of the table in an
 * operation, through its repository, a projection of it or a generated
 * table operation, keeps to the rows the caller is in the audience of. A
 * job, a worker and the database's own role read every row.
 */
export function audience<T>(_options?: AudienceOptions<T>): ClassDecorator {
  return () => {};
}

/**
 * Marks the class that is the table of grants (D156). The build fills its
 * fields: the holder (a party's or an agent run's id) and its kind, the role
 * it acts with, the target row, when it starts, and when it ends (a date, or
 * the target reaching a state), when it was revoked, the SHA-256 of the
 * token its holder presents, and who made it. A role's reaches, a list of
 * "Type:path" on the UserRole table, say which rows beside the target a
 * grant of the role reaches. A revoked or ended grant is refused at the next
 * call.
 *
 * ```ts
 * @accessGrants()
 * export abstract class Grant {}
 * ```
 */
export function accessGrants(): ClassDecorator {
  return () => {};
}

/**
 * Marks the class that is the access log (D156). The build fills its fields:
 * when, what (a reveal, an agent run's read, an API key's call, a grant's
 * call), who and for whom, the credential, the operation, and the row and
 * field read.
 *
 * ```ts
 * @accessLog()
 * export abstract class AccessEntry {}
 * ```
 */
export function accessLog(): ClassDecorator {
  return () => {};
}

/** The options of @calendarSource (D152). */
export interface CalendarSourceOptions<T> {
  /** The source's name, which a calendar read's sources and a feed's scope name; the table's name, with its first letter lowered, unless set (and the field's, or occurrences, after a dot when the table has more than one source). */
  readonly name?: string;
  /** The date or date-time field each row is dated by. Exactly one of field and occurrences. */
  readonly field?: keyof T & string;
  /** The date or date-time field a row's span ends at; a point in time when absent. */
  readonly end?: keyof T & string;
  /** Dates each occurrence of the table's Recurrence by its due time, in place of a field. */
  readonly occurrences?: true;
  /** The field an entry is titled by. */
  readonly title: keyof T & string;
  /** What an entry of the source is, for a reader: deadline, meeting, renewal. */
  readonly kind: string;
  /** The key a reader colours its entries by. */
  readonly color?: string;
  /** The field that holds a row's owner, which a read's scope "mine" keeps to the caller. */
  readonly owner?: keyof T & string;
  /** The permission a caller needs to read any entry of the source, beside its table's audience and grants (#601). A server's calendar options may name another for the source, which wins. */
  readonly permission?: string;
}

/**
 * Puts a table's rows on the calendar (D152): each row dated by a field, or
 * each occurrence of its Recurrence by its due, titled by a field, with a
 * kind and a colour key. A table repeats it for each source.
 *
 * ```ts
 * @calendarSource<Contract>({ field: "endsOn", title: "title", kind: "renewal" })
 * export abstract class Contract { ... }
 *
 * @calendarSource<Filing>({ occurrences: true, title: "title", kind: "deadline", color: "filing" })
 * export abstract class Filing implements Recurrence<Filing, { ... }> { ... }
 * ```
 *
 * The calendar read (readCalendar, its route and its MCP tool) and the
 * iCalendar feed merge every source, each row read through its table's
 * repository as the caller, so a row's audience and the caller's grants
 * hold, and a feed shows only what its token's principal reads.
 */
export function calendarSource<T>(_options: CalendarSourceOptions<T>): ClassDecorator {
  return () => {};
}
