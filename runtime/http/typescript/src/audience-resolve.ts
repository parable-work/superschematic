/*
An audience resolved through a queue (D156, #734).

A table whose @audience inherits from a parent resolves its rows in the
writer's transaction when the parent's audience changes, unless its
@audience names a queue. Then the parent's trigger enqueues one message on
it, the table and the parent's key, and the queue's worker hands the
message to resolveAudience after the parent's write has committed. It
resolves the parent's rows of that table a batch at a time, each batch its
own statement, so no transaction holds them all: each row is updated with
no change, and its own trigger reads its audience from the parent again,
as the cascade does in the writer's transaction. A row of the batch that is
a parent itself cascades to its own children the same way, in the batch's
statement or through its children's queue.

Until the worker has run, a read of such a row keeps to the audience it
had: a parent made private shows its children to whom it showed them, and
one made public hides them, until then. A message is resolved again from
the first row on a retry, which changes nothing of a row already resolved.

Postgres alone: the loader refuses the queue in a service that lists
sqlite.
*/

/** A message of an @audience queue (#734): the table whose rows inherit, by its type's name, and the parent row's key as text. */
export interface AudienceResolving {
  readonly childTable: string;
  readonly parentKey: string;
}

/** What resolveAudience takes. */
export interface ResolveAudienceOptions {
  /** How many rows one statement resolves: AUDIENCE_RESOLVE_BATCH unless set. */
  readonly batch?: number;
}

/** How many rows one statement of resolveAudience resolves, unless its options say. */
export const AUDIENCE_RESOLVE_BATCH = 500;

interface ResolveOrm {
  asSystem(): { query(text: string, values: unknown[]): Promise<{ rows: Record<string, unknown>[] }> };
}

interface ResolveRepository {
  readonly def?: { readonly name?: string; readonly audience?: { readonly resolve?: string } };
}

/**
 * Resolves again the rows of message's table that inherit from its parent
 * (#734), as the system actor, a batch at a time in key order, and answers
 * how many it resolved: what a worker of an @audience queue runs for each
 * message. A table whose @audience names no queue, or none of orm's, is
 * refused.
 */
export async function resolveAudience(orm: object, message: AudienceResolving, options: ResolveAudienceOptions = {}): Promise<number> {
  const batch = options.batch ?? AUDIENCE_RESOLVE_BATCH;
  if (!Number.isInteger(batch) || batch < 1) throw new Error(`resolveAudience: the batch is ${batch}, and it is a whole number of rows, one or more`);
  const repository = Object.values(orm).find(
    (value): value is ResolveRepository => typeof value === 'object' && value !== null && (value as ResolveRepository).def?.name === message.childTable,
  );
  const statement = repository?.def?.audience?.resolve;
  if (!statement) throw new Error(`resolveAudience: ${message.childTable} is no table of the ORM whose @audience names a queue`);
  const system = (orm as ResolveOrm).asSystem();
  let after: string | null = null;
  let resolved = 0;
  for (;;) {
    const row: Record<string, unknown> | undefined = (await system.query(statement, [message.parentKey, after, batch])).rows[0];
    const n = Number(row?.resolved ?? 0);
    resolved += n;
    if (n === 0 || row?.last === null || row?.last === undefined) return resolved;
    after = String(row.last);
  }
}
