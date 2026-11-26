/*
Per-record access (D156): who may see a row of a table, the grants that let a
caller who is no user reach rows, and the log of sensitive reads.
*/

/** Who may see a row: every principal the route admits, the owner alone, the owner and the principals it names, or what its parent sees. */
export type AudienceValue = "everyone" | "private" | "named" | "inherit";

/** What an agent run may do with a row: what its role allows, read it only, or nothing. */
export type AgentAccessValue = "followRole" | "readOnly" | "none";

/** The options of @audience. */
export interface AudienceOptions<T> {
  /**
   * The field that holds the row's owner, the one principal a private row is
   * visible to: a field that holds a UUID, or a to-one relation. Unset, the
   * owner is createdBy, which the table then declares.
   */
  readonly owner?: keyof T & string;
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
