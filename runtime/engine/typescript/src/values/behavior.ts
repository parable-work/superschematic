/*
A behavior's handle on the value store (D16, amended: a large value is
stored once), for the objects it keeps in its own tables: Revisions' copy
of an instance's own fields at each revision, say, which would otherwise
copy a large field into every revision. stow stores each large member of
an object by hash and records that the behavior's row holds it; the row
keeps the JSON text stow returns and, beside it, the pointers to the refs
in it. load puts the values back. release drops what a row holds, and a
value nothing holds any more is removed.

A row is named by the behavior, the call's namespace and schema, the
instance the context runs on ('' in one with no instance) and a key the
behavior gives, unique among its rows of that instance. A read's handle
loads only.
*/

import type { FrozenJSON, StowedObject, ValueWriter } from '../behaviors/behavior.js';
import { deepFreeze, jsonCopy } from '../behaviors/json.js';
import { BehaviorError } from '../errors.js';
import { isPlainObject } from '../instances/patch.js';
import type { Storage } from '../storage/storage.js';
import { refsOf, refsText, valuesOf } from './store.js';

/** Whose rows a handle names: a behavior on an instance of a schema, or on the schema ('' for the id). */
export interface ValueRows {
  readonly behavior: string;
  readonly namespace: string;
  readonly schema: string;
  readonly id: string;
}

/** Why a context's stow or release refuses, asked at each call; undefined when it may. */
export type ValueRefusal = (what: 'stow' | 'release') => string | undefined;

/** The refusal of a context that only reads: who it is, `a read` say. */
export function readOnlyValues(who: string): ValueRefusal {
  return (what) => `${who} cannot change what the value store holds (values.${what})`;
}

/** behaviorValues is a context's values: stow and release refuse when refusal gives a reason. */
export function behaviorValues(storage: Storage, rows: ValueRows, refusal: ValueRefusal = () => undefined): ValueWriter {
  const { behavior, namespace, schema, id } = rows;
  const writing = (what: 'stow' | 'release'): void => {
    const reason = refusal(what);
    if (reason !== undefined) {
      throw new BehaviorError(behavior, reason);
    }
  };
  const checkKey = (what: string, key: unknown): string => {
    if (typeof key !== 'string' || key === '') {
      throw new BehaviorError(behavior, `values.${what} takes the key of the behavior's row: a non-empty string`);
    }
    return key;
  };
  return Object.freeze({
    stow: (key: string, object: FrozenJSON): StowedObject => {
      writing('stow');
      checkKey('stow', key);
      const copied = jsonCopy(object);
      if (!('value' in copied) || !isPlainObject(copied.value)) {
        throw new BehaviorError(behavior, 'values.stow takes a JSON object, whose large top-level members it stores by hash');
      }
      const store = valuesOf(storage);
      const stowed = store.stow(copied.value);
      store.hold({ namespace, schema, holder: behavior, id, key }, stowed.hashes);
      return Object.freeze({ json: JSON.stringify(stowed.value), refs: refsText(stowed.refs) });
    },
    load: (json: string, refs: string | null | undefined): FrozenJSON => {
      if (typeof json !== 'string') {
        throw new BehaviorError(behavior, 'values.load takes the JSON text stow returned, and its refs');
      }
      return deepFreeze(valuesOf(storage).fill(JSON.parse(json) as Record<string, unknown>, refsOf(refs)));
    },
    release: (key?: string): void => {
      writing('release');
      valuesOf(storage).release({ namespace, schema, holder: behavior, id, ...(key === undefined ? {} : { key: checkKey('release', key) }) });
    },
  });
}
