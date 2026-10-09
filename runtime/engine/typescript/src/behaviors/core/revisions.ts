/*
Revisions, the core's immutable revisions of an instance's own fields
(D16). A create records revision 1 and every change to the own fields the
next, in the change's transaction: an update, and an operation that
changes them with update(), an approval included. A revision holds the
own fields as the change left them, never a behavior's field, and is
never changed or removed while the instance exists. listRevisions reads
them a page at a time, oldest first, and getRevision one by its number;
neither goes through a write.

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
(not_pending): vetoes with the codes the declaration lists. With review,
the pendingProposals field counts the proposals still pending; without
it the field is absent.

A proposal may cite evidence: instances, each optionally at one of its
revisions, which propose checks as the proposer reads them (the target
exists, its schema is readable, and a cited revision is one the target,
whose schema composes Revisions, has had) and stores as data. Evidence
is no reference: the engine records none, so a target's later change or
delete leaves the proposal as it was proposed.

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

import { REVISIONS_REVISION, behaviorField } from '../fields.js';
import { BehaviorVetoError, EngineError, InstanceValidationError, OperationParamsError, type SchemaIssue } from '../../errors.js';
import { jsonEqual, mergePatch } from '../../instances/patch.js';
import type { Row } from '../../storage/driver.js';
import { defineBehavior, type FrozenJSON, type InstanceContext, type InstanceView, type ValueReader } from '../behavior.js';
import declaration from './declarations/Revisions.behavior.json' with { type: 'json' };
import { revisionsGuidance } from './guidance/revisions.js';
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

/** One instance a proposal cites as evidence, and the revision of it, when it names one. */
export interface EvidenceRecord {
  readonly schema: string;
  readonly id: string;
  readonly revision?: number;
}

/** One proposal, as propose, approve, reject and listProposals return it. */
export interface ProposalRecord {
  readonly id: number;
  readonly patch: Record<string, unknown>;
  readonly note?: string;
  /** What the proposer cited, as propose checked it. */
  readonly evidence?: readonly EvidenceRecord[];
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

const PROPOSAL_COLUMNS = 'proposal, patch, note, evidence, base, state, created_by, created_at, reviewed_by, reviewed_at, reason, revision, value_refs';

const REVISION_COLUMNS = 'revision, data, created_by, created_at, proposal, value_refs';

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
    evidence: row.evidence === null || row.evidence === undefined ? undefined : (JSON.parse(String(row.evidence)) as EvidenceRecord[]),
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

// evidenceOf checks the evidence a proposal cites, as the proposer reads
// it: each target's schema and the target exist, the proposer may read
// the schema (instances.get asks, and a refusal is forbidden), and a
// cited revision is one the target has had, of a schema that composes
// Revisions. It returns the entries as they are stored.
function evidenceOf(context: InstanceContext<RevisionsConfig>, entries: ReadonlyArray<FrozenJSON>): EvidenceRecord[] {
  const issues: SchemaIssue[] = [];
  const out: EvidenceRecord[] = [];
  entries.forEach((entry, index) => {
    const at = `/evidence/${index}`;
    const schema = entry.schema as string;
    const id = entry.id as string;
    const revision = entry.revision as number | undefined;
    let target;
    try {
      target = context.instances.get(schema, id, { fields: revision === undefined ? [] : [REVISIONS_REVISION] });
    } catch (error) {
      if (error instanceof EngineError && error.code === 'not_found') {
        issues.push({ path: `${at}/schema`, message: `${schema} is not a schema of namespace ${context.namespace}` });
        return;
      }
      throw error;
    }
    if (target === undefined) {
      issues.push({ path: `${at}/id`, message: `${schema} ${id} does not exist` });
      return;
    }
    if (revision !== undefined) {
      if (context.schemas.config(schema, 'Revisions') === undefined) {
        issues.push({ path: `${at}/revision`, message: `${schema} does not compose Revisions, so ${schema} ${id} has no revision to cite` });
        return;
      }
      const held = behaviorField(target, 'Revisions', 'revision');
      const current = typeof held === 'number' ? held : 0;
      if (revision > current) {
        issues.push({
          path: `${at}/revision`,
          message: current === 0 ? `${schema} ${id} has no revision yet` : `${schema} ${id} has revisions 1 to ${current}, not ${revision}`,
        });
        return;
      }
    }
    out.push({ schema, id, ...(revision === undefined ? {} : { revision }) });
  });
  if (issues.length > 0) {
    throw new OperationParamsError('Revisions', 'propose', issues);
  }
  return out;
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

  guidance: revisionsGuidance,

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
    {
      version: 3,
      name: 'evidence',
      up(sql) {
        sql.run(`ALTER TABLE ${sql.table('proposals')} ADD COLUMN evidence TEXT`);
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
        `SELECT ${REVISION_COLUMNS} FROM ${context.sql.table('revisions')}
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

    // getRevision reads one revision through the value store, as
    // listRevisions reads a page of them.
    getRevision(context, params) {
      const revision = params.revision as number;
      const row = context.sql.get(
        `SELECT ${REVISION_COLUMNS} FROM ${context.sql.table('revisions')} WHERE namespace = ? AND schema = ? AND id = ? AND revision = ?`,
        [...key(context), revision]
      );
      if (row === undefined) {
        const current = latest(context);
        throw new EngineError(
          'not_found',
          `${context.schema} ${context.id} has ${current === 0 ? 'no revision yet' : `revisions 1 to ${current}`}, not revision ${revision}`
        );
      }
      return revisionOf(context.values, row);
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
      const evidence = params.evidence === undefined ? undefined : evidenceOf(context, params.evidence as ReadonlyArray<FrozenJSON>);
      const table = context.sql.table('proposals');
      const last = context.sql.get(`SELECT MAX(proposal) AS last FROM ${table} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
      const id = Number(last?.last ?? 0) + 1;
      const base = latest(context);
      const stowed = context.values.stow(proposalKey(id), patch);
      context.sql.run(
        `INSERT INTO ${table} (namespace, schema, id, proposal, patch, note, evidence, base, state, created_by, created_at, value_refs)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)`,
        [
          ...key(context),
          id,
          stowed.json,
          (params.note as string | undefined) ?? null,
          evidence === undefined ? null : JSON.stringify(evidence),
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
    // The proposals still pending, through the index by state; absent
    // without a review step, where there are none to count.
    pendingProposals: (view) => {
      if (view.config.review === undefined) {
        return undefined;
      }
      const row = view.sql.get(
        `SELECT COUNT(*) AS pending FROM ${view.sql.table('proposals')} WHERE namespace = ? AND schema = ? AND id = ? AND state = 'pending'`,
        key(view)
      );
      return Number(row?.pending ?? 0);
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
