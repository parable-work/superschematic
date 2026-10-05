/*
What a publish runs of a behavior: its afterConfigChange, when the
published version adds the behavior, removes it or changes its config
(composition.ts, configTransitions). It runs in the publish's transaction,
after the version is recorded and before its publish event, once for each
namespace whose instances the schema serves: the namespace that holds it,
or every namespace for a schema of the shared one, since each holds its
own instances of it. A throw rolls the publish back, version and storage
with it.

The context reaches the behavior's own tables, with writes, and the
schema's instances in the namespace, read 500 at a time in creation
order, each with its own fields only. It has no principal and asks no
policy: the publish was allowed, and what the behavior reads goes into
its own storage, never back to the publisher. A search index is rebuilt
this way when the fields it indexes change. Its validate(type, value)
checks a value with the version being published, as a call's does with
the live version.
*/

import type { Storage } from '../storage/storage.js';
import type { FrozenJSON, PublishContext, StoredInstance } from './behavior.js';
import type { ConfigTransition } from './composition.js';
import { typeCheck, type Runtime } from './execution.js';
import { deepFreeze } from './json.js';
import { BehaviorSql, prefixOf, storedKey, synchronous } from './storage.js';

/** How many instances eachInstance reads at a time. */
const BATCH = 500;

/** The version a publish records, and where its instances are. */
export interface PublishTarget {
  /** The namespace that holds the schema. */
  readonly holder: string;
  readonly schema: string;
  readonly version: number;
  readonly now: number;
  /** The namespaces whose instances the schema serves: the holder, or every namespace for the shared one. */
  readonly namespaces: readonly string[];
  /** The version's behaviors and validator, which the context's validate checks values with. */
  readonly runtime: Pick<Runtime, 'composition' | 'validator'>;
}

/**
 * afterConfigChanges runs each transition's afterConfigChange, in order,
 * for each namespace of the target. Call it inside the publish's
 * transaction, once the behaviors' storage exists.
 */
export function afterConfigChanges(storage: Storage, transitions: readonly ConfigTransition[], target: PublishTarget): void {
  for (const { behavior, before, after } of transitions) {
    const hook = behavior.implementation.afterConfigChange;
    const key = storedKey(storage, behavior.name);
    if (!hook || key === undefined) {
      continue;
    }
    const prefix = prefixOf(key);
    for (const namespace of target.namespaces) {
      const context: PublishContext<unknown> = Object.freeze({
        behavior: behavior.name,
        config: after,
        before,
        namespace,
        schema: target.schema,
        version: target.version,
        now: target.now,
        sql: new BehaviorSql(storage, behavior.name, prefix, 'write'),
        eachInstance: (visit: (instance: StoredInstance) => void) =>
          eachInstance(storage, namespace, target, (instance) => synchronous(behavior.name, 'eachInstance', visit(instance))),
        validate: typeCheck(target.runtime, behavior.name),
      });
      synchronous(behavior.name, 'afterConfigChange', hook.call(behavior.implementation, context));
    }
  }
}

function eachInstance(storage: Storage, namespace: string, target: PublishTarget, visit: (instance: StoredInstance) => void): void {
  for (let after = 0; ; ) {
    const rows = storage.all(
      `SELECT position, id, data FROM engine_instances
       WHERE namespace = ? AND schema = ? AND schema_namespace = ? AND position > ?
       ORDER BY position LIMIT ?`,
      [namespace, target.schema, target.holder, after, BATCH]
    );
    for (const row of rows) {
      visit(Object.freeze({ id: String(row.id), data: deepFreeze(JSON.parse(String(row.data)) as FrozenJSON) }));
    }
    if (rows.length < BATCH) {
      return;
    }
    after = Number(rows[rows.length - 1].position);
  }
}
