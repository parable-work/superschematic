/*
Reading a value by its hash (D16, amended: a large value is stored once),
what engine.values, the route GET /namespaces/{namespace}/values/{hash}
and the MCP tool get_value serve. A caller reads a value only through a
schema of the namespace that references it: an instance of the schema, an
event of one, or a row a behavior on the schema keeps (store.ts, the
holders), and the access policy must allow the caller `read` on one of
those schemas. Knowing a hash grants nothing: a value no schema of the
namespace references, and one only schemas the caller may not read
reference, are not_found alike, so the answer does not tell whether the
namespace holds the value.
*/

import { checkPrincipal, type Access, type Principal } from '../access.js';
import { EngineError } from '../errors.js';
import type { Namespaces } from '../namespaces.js';
import type { Storage } from '../storage/storage.js';
import { VALUE_HASH, valuesOf, type StoredValue } from './store.js';

/** Where a read of a value looks: a namespace, `default` when absent. */
export interface ValueTarget {
  namespace?: string;
}

export class EngineValues {
  constructor(
    private readonly storage: Storage,
    private readonly namespaces: Namespaces,
    private readonly access: Access
  ) {}

  /**
   * get returns the value stored under a hash, `{ hash, bytes, value }`,
   * when a schema of the namespace the principal may read references it.
   * Otherwise it throws not_found, whether or not the namespace holds the
   * value.
   */
  get(principal: Principal, hash: string, target: ValueTarget = {}): StoredValue {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(target.namespace);
    if (typeof hash !== 'string' || !VALUE_HASH.test(hash)) {
      throw new EngineError('invalid_argument', `a value's hash is the SHA-256 of its canonical JSON, 64 lowercase hex digits, not ${JSON.stringify(hash)}`);
    }
    const store = valuesOf(this.storage);
    const readable = store.schemasHolding(hash, namespace).some((schema) => this.access.allows(principal, 'read', namespace, schema));
    const value = readable ? store.read(hash) : undefined;
    if (value === undefined) {
      throw new EngineError('not_found', `namespace ${namespace} holds no value ${hash} that ${principal.subject} may read`);
    }
    return value;
  }

  /** The value store's threshold: a member whose JSON is longer is stored by hash. */
  get thresholdBytes(): number {
    return valuesOf(this.storage).threshold;
  }

  /** The longest value the store keeps (values.maxBytes); a write of a longer one is value_too_large. */
  get maxBytes(): number {
    return valuesOf(this.storage).maxBytes;
  }

  /**
   * sweep removes each value the driver stores that no holder references,
   * what a crash left between a write through a driver outside the file's
   * transactions and the end of that transaction, and returns how many.
   * It acts for no principal, as retention's prune does, so it takes none;
   * it needs a driver that lists its hashes (ValueDriver.list, which the
   * default has) and no open transaction.
   */
  sweep(): { removed: number } {
    return valuesOf(this.storage).sweep();
  }
}
