/*
Revisions, the core's immutable revisions of an instance's own fields
(D16). A create records revision 1 and every change to the own fields the
next, in the change's transaction: an update, and an operation that
changes them with update(), an approval included. A revision holds the
own fields as the change left them, never a behavior's field, and is
never changed or removed while the instance exists. listRevisions reads
them a page at a time, oldest first, and goes through no write.

With `review: { permission }` in its config, a change can go through a
reviewer. propose stores a JSON merge patch of the own fields as a
pending proposal, after checking it changes the instance and leaves it
valid; nothing else changes. approve applies a pending proposal's patch
with update(), so the update's validation and every guard run, and
records the revision it makes; reject settles it with an optional reason.
approve and reject need the review permission, which the guard asks the
deployment's matcher about (can); who may propose is the access policy's
call, which it gets as `write` with the operation `propose`.
listProposals is read-only and never takes the write path. A patch
applies to the instance as it is at approval, not as it was proposed:
`base` records the revision it was made against, so a reviewer can see
the instance moved since. Without review, the four refuse (no_review),
and approve and reject refuse a proposal that is not pending
(not_pending): vetoes with the codes the declaration lists.

Numbers (revisions, proposals) are per instance, 1, 2, 3, .... Deleting
the instance deletes its revisions and proposals.

A revision's fields and a proposal's patch go through the engine's value
store (values.stow): a field whose JSON is longer than the engine's
threshold is stored once by hash, however many revisions, proposals,
events and the instance itself hold it, and the row keeps a ref with the
pointers to the refs beside it (value_refs). Every read puts the values
back. A row written before Revisions' second migration has none.

configChange: review may be added, removed or given another permission.
Revisions can be added to a schema that has instances: each one's history
starts at its next change. It cannot be removed from one: the history
would stay behind with nothing to delete it.
*/

import { BehaviorVetoError, EngineError, InstanceValidationError, OperationParamsError } from '../../errors.js';
import { jsonEqual, mergePatch } from '../../instances/patch.js';
import type { Row } from '../../storage/driver.js';
import { defineBehavior, type FrozenJSON, type InstanceContext, type InstanceView, type ValueReader } from '../behavior.js';
import declaration from './declarations/Revisions.behavior.json' with { type: 'json' };
import { page, pageRequest } from '../paging.js';

/** Revisions' config. */
export interface RevisionsConfig {
  readonly review?: { readonly permission: string };
}

export type ProposalState = 'pending' | 'approved' | 'rejected';

/** One revision, as listRevisions returns it. */
export interface RevisionRecord {
  readonly revision: number;
  readonly data: Record<string, unknown>;
  readonly createdBy: string;
  readonly createdAt: number;
  /** The proposal whose approval made it. */
  readonly proposal?: number;
}

/** One proposal, as propose, approve, reject and listProposals return it. */
export interface ProposalRecord {
  readonly id: number;
  readonly patch: Record<string, unknown>;
  readonly note?: string;
  readonly base?: number;
  readonly state: ProposalState;
  readonly createdBy: string;
  readonly createdAt: number;
  readonly reviewedBy?: string;
  readonly reviewedAt?: number;
  readonly reason?: string;
  readonly revision?: number;
}

const REVIEW_OPERATIONS = ['propose', 'approve', 'reject', 'listProposals'];

const PROPOSAL_COLUMNS = 'proposal, patch, note, base, state, created_by, created_at, reviewed_by, reviewed_at, reason, revision, value_refs';

function key(view: InstanceView<unknown>): [string, string, string] {
  return [view.namespace, view.schema, view.id];
}

// The keys of a revision's and a proposal's rows in the value store.
const revisionKey = (revision: number): string => `revision ${revision}`;
const proposalKey = (proposal: number): string => `proposal ${proposal}`;

function revisionOf(values: ValueReader, row: Row): RevisionRecord {
  return {
    revision: Number(row.revision),
    data: values.load(String(row.data), row.value_refs as string | null),
    createdBy: String(row.created_by),
    createdAt: Number(row.created_at),
    ...(row.proposal === null ? {} : { proposal: Number(row.proposal) }),
  };
}

// proposalOf reads a proposal row. A member with no value is undefined,
// which the engine drops from an operation's result.
function proposalOf(values: ValueReader, row: Row): ProposalRecord {
  const text = (value: unknown) => (value === null ? undefined : String(value));
  const number = (value: unknown) => (value === null ? undefined : Number(value));
  return {
    id: Number(row.proposal),
    patch: values.load(String(row.patch), row.value_refs as string | null),
    note: text(row.note),
    base: number(row.base),
    state: String(row.state) as ProposalState,
    createdBy: String(row.created_by),
    createdAt: Number(row.created_at),
    reviewedBy: text(row.reviewed_by),
    reviewedAt: number(row.reviewed_at),
    reason: text(row.reason),
    revision: number(row.revision),
  };
}

// latest is the instance's latest revision number, 0 before its first.
function latest(view: InstanceView<unknown>): number {
  return Number(view.columns.get().current);
}

// record stores the instance's own fields as its next revision.
function record(context: InstanceContext<RevisionsConfig>, proposal?: number): number {
  const revision = latest(context) + 1;
  const stowed = context.values.stow(revisionKey(revision), context.data);
  context.sql.run(
    `INSERT INTO ${context.sql.table('revisions')} (namespace, schema, id, revision, data, created_by, created_at, proposal, value_refs)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    [...key(context), revision, stowed.json, context.principal.subject, context.now, proposal ?? null, stowed.refs]
  );
  context.columns.set({ current: revision });
  return revision;
}

// pending loads a proposal an approve or reject names, which must be pending.
function pending(context: InstanceContext<RevisionsConfig>, operation: string, id: number): Row {
  const row = context.sql.get(
    `SELECT ${PROPOSAL_COLUMNS} FROM ${context.sql.table('proposals')} WHERE namespace = ? AND schema = ? AND id = ? AND proposal = ?`,
    [...key(context), id]
  );
  if (!row) {
    throw new OperationParamsError('Revisions', operation, [{ path: '/proposal', message: `${context.schema} ${context.id} has no proposal ${id}` }]);
  }
  if (row.state !== 'pending') {
    throw new BehaviorVetoError('Revisions', operation, context.schema, context.id, {
      reason: `proposal ${id} is ${String(row.state)}, not pending`,
      code: 'not_pending',
      details: { proposal: id, state: String(row.state) },
    });
  }
  return row;
}

// settle records a reviewer's decision on a proposal and returns it.
function settle(context: InstanceContext<RevisionsConfig>, id: number, state: ProposalState, reason: string | undefined, revision: number | undefined): ProposalRecord {
  const table = context.sql.table('proposals');
  context.sql.run(
    `UPDATE ${table} SET state = ?, reviewed_by = ?, reviewed_at = ?, reason = ?, revision = ?
     WHERE namespace = ? AND schema = ? AND id = ? AND proposal = ?`,
    [state, context.principal.subject, context.now, reason ?? null, revision ?? null, ...key(context), id]
  );
  return proposalOf(
    context.values,
    context.sql.get(`SELECT ${PROPOSAL_COLUMNS} FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND proposal = ?`, [
      ...key(context),
      id,
    ]) as Row
  );
}

export const revisions = defineBehavior<RevisionsConfig>({
  declaration,

  configChange(before, after) {
    if (before !== undefined && after === undefined) {
      return 'the revisions and proposals its instances have would stay behind with nothing to delete them';
    }
    return undefined;
  },

  migrations: [
    {
      version: 1,
      name: 'revisions and proposals',
      columns: { current: { type: 'integer', notNull: true, default: 0 } },
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('revisions')} (
          namespace  TEXT    NOT NULL,
          schema     TEXT    NOT NULL,
          id         TEXT    NOT NULL,
          revision   INTEGER NOT NULL,
          data       TEXT    NOT NULL CHECK (json_valid(data)),
          created_by TEXT    NOT NULL,
          created_at INTEGER NOT NULL,
          proposal   INTEGER,
          PRIMARY KEY (namespace, schema, id, revision)
        ) STRICT`);
        sql.run(`CREATE TABLE ${sql.table('proposals')} (
          namespace   TEXT    NOT NULL,
          schema      TEXT    NOT NULL,
          id          TEXT    NOT NULL,
          proposal    INTEGER NOT NULL,
          patch       TEXT    NOT NULL CHECK (json_valid(patch)),
          note        TEXT,
          base        INTEGER,
          state       TEXT    NOT NULL CHECK (state IN ('pending', 'approved', 'rejected')),
          created_by  TEXT    NOT NULL,
          created_at  INTEGER NOT NULL,
          reviewed_by TEXT,
          reviewed_at INTEGER,
          reason      TEXT,
          revision    INTEGER,
          PRIMARY KEY (namespace, schema, id, proposal)
        ) STRICT`);
        sql.run(`CREATE INDEX ${sql.table('proposals_by_state')} ON ${sql.table('proposals')} (namespace, schema, id, state, proposal)`);
      },
    },
    {
      version: 2,
      name: 'large fields by hash',
      up(sql) {
        sql.run(`ALTER TABLE ${sql.table('revisions')} ADD COLUMN value_refs TEXT`);
        sql.run(`ALTER TABLE ${sql.table('proposals')} ADD COLUMN value_refs TEXT`);
      },
    },
  ],

  // The review step: its operations need review in the config, and
  // approve and reject the review permission, whoever calls them.
  guard(view, request) {
    if (request.kind !== 'operation' || request.behavior !== 'Revisions' || !REVIEW_OPERATIONS.includes(request.operation)) {
      return undefined;
    }
    const review = view.config.review;
    if (review === undefined) {
      return { reason: `${view.schema} has no review step: its Revisions config sets no review`, code: 'no_review' };
    }
    if ((request.operation === 'approve' || request.operation === 'reject') && !view.can(review.permission)) {
      throw new EngineError(
        'forbidden',
        `${view.principal.subject} may not ${request.operation} a proposal on ${view.schema} ${view.id}: it needs permission ${review.permission}`
      );
    }
    return undefined;
  },

  operations: {
    listRevisions(context, params) {
      const { limit, after } = pageRequest('Revisions', 'listRevisions', params);
      const rows = context.sql.all(
        `SELECT revision, data, created_by, created_at, proposal, value_refs FROM ${context.sql.table('revisions')}
         WHERE namespace = ? AND schema = ? AND id = ? AND revision > ?
         ORDER BY revision LIMIT ?`,
        [...key(context), after, limit + 1]
      );
      return page(
        rows.map((row) => revisionOf(context.values, row)),
        limit,
        (revision) => revision.revision
      );
    },

    propose(context, params) {
      const patch = params.patch as FrozenJSON;
      const issues = context.validateUpdate(patch);
      if (issues.length > 0) {
        throw new InstanceValidationError(context.namespace, context.schema, context.version, [...issues]);
      }
      if (jsonEqual(mergePatch(context.data, patch), context.data)) {
        throw new OperationParamsError('Revisions', 'propose', [{ path: '/patch', message: `changes nothing: ${context.schema} ${context.id} already has these fields` }]);
      }
      const table = context.sql.table('proposals');
      const last = context.sql.get(`SELECT MAX(proposal) AS last FROM ${table} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
      const id = Number(last?.last ?? 0) + 1;
      const base = latest(context);
      const stowed = context.values.stow(proposalKey(id), patch);
      context.sql.run(
        `INSERT INTO ${table} (namespace, schema, id, proposal, patch, note, base, state, created_by, created_at, value_refs)
         VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)`,
        [
          ...key(context),
          id,
          stowed.json,
          (params.note as string | undefined) ?? null,
          base > 0 ? base : null,
          context.principal.subject,
          context.now,
          stowed.refs,
        ]
      );
      return proposalOf(
        context.values,
        context.sql.get(`SELECT ${PROPOSAL_COLUMNS} FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND proposal = ?`, [
          ...key(context),
          id,
        ]) as Row
      );
    },

    approve(context, params) {
      const id = params.proposal as number;
      const row = pending(context, 'approve', id);
      const before = context.data;
      context.update(context.values.load(String(row.patch), row.value_refs as string | null));
      // The revision the approval makes; afterChange sees it recorded.
      const revision = jsonEqual(before, context.data) ? latest(context) : record(context, id);
      return settle(context, id, 'approved', undefined, revision > 0 ? revision : undefined);
    },

    reject(context, params) {
      const id = params.proposal as number;
      pending(context, 'reject', id);
      return settle(context, id, 'rejected', params.reason as string | undefined, undefined);
    },

    listProposals(context, params) {
      const { limit, after } = pageRequest('Revisions', 'listProposals', params);
      const state = params.state as ProposalState | undefined;
      const rows = context.sql.all(
        `SELECT ${PROPOSAL_COLUMNS} FROM ${context.sql.table('proposals')}
         WHERE namespace = ? AND schema = ? AND id = ? AND proposal > ?${state === undefined ? '' : ' AND state = ?'}
         ORDER BY proposal LIMIT ?`,
        [...key(context), after, ...(state === undefined ? [] : [state]), limit + 1]
      );
      return page(
        rows.map((row) => proposalOf(context.values, row)),
        limit,
        (proposal) => proposal.id
      );
    },
  },

  fields: {
    revision: (view) => {
      const revision = latest(view);
      return revision > 0 ? revision : undefined;
    },
  },

  afterChange(context, change) {
    switch (change.kind) {
      case 'create':
      case 'update':
        record(context);
        return;
      case 'operation': {
        if (change.before === undefined) {
          return;
        }
        // An approval records its own revision, with its proposal.
        const last = context.sql.get(
          `SELECT data, value_refs FROM ${context.sql.table('revisions')} WHERE namespace = ? AND schema = ? AND id = ? AND revision = ?`,
          [...key(context), latest(context)]
        );
        if (last === undefined || !jsonEqual(context.values.load(String(last.data), last.value_refs as string | null), context.data)) {
          record(context);
        }
        return;
      }
      case 'delete':
        context.sql.run(`DELETE FROM ${context.sql.table('revisions')} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
        context.sql.run(`DELETE FROM ${context.sql.table('proposals')} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
        context.values.release();
    }
  },
});
