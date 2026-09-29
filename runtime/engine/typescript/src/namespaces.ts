/*
Namespaces hold schemas and instances. There is one, `default`, unless the
deployment configures more. It may also name one of them shared: every
other namespace looks a schema name up in itself, then in the shared one.
A name is defined on one side of that lookup only, so a schema a
namespace reaches never changes under it: defining a name the shared
namespace holds is refused, and so is defining in the shared namespace a
name another namespace holds.
*/

import { EngineError } from './errors.js';

export const DEFAULT_NAMESPACE = 'default';

/** A namespace name: lowercase letters, digits and hyphens, starting with a letter, at most 63 characters. */
export const NAMESPACE_NAME = /^[a-z][a-z0-9-]{0,62}$/;

export interface NamespaceOptions {
  /** Namespaces besides `default`. */
  names?: readonly string[];
  /** The namespace every other one looks schema names up in after itself. */
  shared?: string;
}

export class Namespaces {
  /** Every namespace, `default` first. */
  readonly names: readonly string[];
  readonly shared: string | undefined;

  constructor(options: NamespaceOptions = {}) {
    const names = [DEFAULT_NAMESPACE];
    for (const name of options.names ?? []) {
      if (typeof name !== 'string' || !NAMESPACE_NAME.test(name)) {
        throw new TypeError(`namespace "${String(name)}" must match ${NAMESPACE_NAME.source}`);
      }
      if (!names.includes(name)) {
        names.push(name);
      }
    }
    if (options.shared !== undefined && !names.includes(options.shared)) {
      throw new TypeError(`the shared namespace "${options.shared}" is not one of the namespaces (${names.join(', ')})`);
    }
    this.names = names;
    this.shared = options.shared;
  }

  /** resolve returns the namespace a call names, `default` when it names none. */
  resolve(namespace: string | undefined): string {
    if (namespace === undefined) {
      return DEFAULT_NAMESPACE;
    }
    if (!this.names.includes(namespace)) {
      throw new EngineError('unknown_namespace', `unknown namespace "${String(namespace)}"`);
    }
    return namespace;
  }

  /** lookup lists where a namespace finds a schema name, in order: itself, then the shared namespace. */
  lookup(namespace: string): string[] {
    if (this.shared === undefined || this.shared === namespace) {
      return [namespace];
    }
    return [namespace, this.shared];
  }
}
