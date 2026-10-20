/*
Instances (D16): the records engine.instances creates, reads, lists,
updates and deletes, and the options of each call, with the limits of a
list's filter.
*/

import type { BehaviorFields } from './fields.js';

/** An instance id: a letter or digit, then letters, digits, `.`, `_`, `:` and `-`, at most 256 characters. */
export const INSTANCE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/;

/** A stored instance. */
export interface InstanceRecord {
  namespace: string;
  schema: string;
  id: string;
  /** The namespace that holds the schema: the instance's own, or the shared one. */
  schemaNamespace: string;
  /** The schema version the instance was last written with. */
  version: number;
  /** The per-instance sequence of its last event. */
  seq: number;
  /** The instance's own fields, as stored. */
  data: Record<string, unknown>;
  /**
   * Its behaviors' fields, by behavior name, in the type's behavior order:
   * each an object of the fields the behavior declares that have a value,
   * in its declaration's order. A behavior that declares no field has no
   * entry, so nothing here can collide with an own field or another
   * behavior's.
   */
  behaviors: BehaviorFields;
  createdAt: number;
  createdBy: string;
  updatedAt: number;
  updatedBy: string;
  /**
   * For a read that asked for valueRefs: the JSON pointers into data of
   * the own fields that hold a ref, `{ "$value": <hash>, "bytes": <n> }`,
   * in place of a value the value store holds. Absent when none does.
   */
  valueRefs?: string[];
}

/** Where a call looks: a namespace, `default` when absent. */
export interface InstanceTarget {
  namespace?: string;
}

/** What a get takes beside the schema and the id. */
export interface GetOptions extends InstanceTarget {
  /**
   * Return each own field the value store holds as its ref, with the
   * pointers to them in valueRefs, rather than its value, which a client
   * reads by its hash when it needs it.
   */
  valueRefs?: boolean;
}

export interface CreateOptions extends InstanceTarget {
  /** The id; the engine's id generator makes one when absent. */
  id?: string;
  /**
   * The parameters the create gives the type's behaviors, by behavior
   * name, each held to that behavior's createParamsSchema and handed to
   * its initialize: Links' links and Dependencies' blockers, say, which
   * then hold from the create on, in its transaction.
   */
  behaviors?: Record<string, unknown>;
}

export interface UpdateOptions extends InstanceTarget {
  /**
   * The sequence the caller last read. The update is refused with
   * seq_mismatch unless the instance is still at it, checked inside the
   * write transaction.
   */
  expectedSeq?: number;
  /**
   * Preconditions by behavior, such as `{ Lease: { token: 7 } }`: each
   * entry names a behavior the type composes that declares a
   * preconditionSchema, and holds what that schema accepts, or the call
   * is refused (PreconditionsError, invalid_argument). Each behavior's
   * guard gets its own entry as the request's precondition, and decides
   * what it asserts. A patch that changes nothing asks no guard.
   */
  preconditions?: Readonly<Record<string, unknown>>;
}

export interface DeleteOptions extends InstanceTarget {
  /** As UpdateOptions.expectedSeq. */
  expectedSeq?: number;
  /** As UpdateOptions.preconditions. */
  preconditions?: Readonly<Record<string, unknown>>;
}

export interface InvokeOptions extends InstanceTarget {
  /**
   * As UpdateOptions.expectedSeq: the operation is refused with
   * seq_mismatch unless the instance is still at this sequence, checked
   * before any guard runs, inside the write transaction of an operation
   * that writes.
   */
  expectedSeq?: number;
  /** As UpdateOptions.preconditions, handed to the guards the operation asks. */
  preconditions?: Readonly<Record<string, unknown>>;
}

/** Where a schema-level operation runs: a namespace, `default` when absent. */
export type InvokeSchemaOptions = InstanceTarget;

/** What an operation returns, with the instance's sequence after it: its entity tag. */
export interface OperationOutcome {
  result: unknown;
  seq: number;
}

export interface ListOptions extends InstanceTarget {
  /** At most this many instances, 50 by default and at most 500. */
  limit?: number;
  /** The `next` of the previous page. */
  cursor?: string;
  /** As GetOptions.valueRefs. */
  valueRefs?: boolean;
  /**
   * Field values the instances hold, by field: a value, null for none
   * (absent or null), or a list of 1 to 100 of which the instance holds
   * one. A field is an own top-level field that holds a string, a number
   * or a boolean, or one a behavior lets a list filter on (Workflow's
   * status, or a member of a behavior's field, lease.holder). A filtered
   * page can hold fewer instances than limit, even none, while next is
   * not null.
   */
  where?: Readonly<Record<string, unknown>>;
}

/** One page of a list. */
export interface InstancePage {
  items: InstanceRecord[];
  /** The cursor of the next page; null after the last. */
  next: string | null;
}

/** The most values one member of where lists. */
export const MAX_FILTER_VALUES = 100;

/** The most combinations of values a page reads through one index. */
export const MAX_FILTER_RANGES = 100;

/**
 * The instances a filtered page reads past its cursor: every one it reads
 * when no index serves the filter, shared among its range reads when one
 * does (each reads at least two).
 */
export const FILTER_SCAN_ROWS = 1000;
