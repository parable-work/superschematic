/*
Namespaces hold schemas and instances. There is one, `default`, unless the
deployment configures more. It may also name one of them shared: every
other namespace looks a schema name up in itself, then in the shared one.
A name is defined on one side of that lookup only, so a schema a
namespace reaches never changes under it: defining a name the shared
namespace holds is refused, and so is defining in the shared namespace a
name another namespace holds.

Namespaces come from two places. The engine's options configure some,
`default` always among them, and only the options add or take one away.
A create makes another while the engine runs, which engine_namespaces
keeps, so the next engine on the file has it too; it looks names up in
the shared namespace as a configured one does. A namespace a create made
can be archived: it is read as it was and refuses every write
(namespace_archived) until it is unarchived. A configured namespace is
never archived, and a name the options list is configured whatever the
table holds for it. Who may create, archive, unarchive or list a
namespace is the access policy's `manage`, asked about the namespace
itself (access.ts).

One process writes the file (D16), so the namespaces it holds in memory
are the file's: a create, an archive and an unarchive change them once
their transaction commits.
*/

import { checkPrincipal, type Access, type Principal } from './access.js';
import { EngineError } from './errors.js';
import type { Row } from './storage/driver.js';
import type { Storage } from './storage/storage.js';

export const DEFAULT_NAMESPACE = 'default';

/** A namespace name: lowercase letters, digits and hyphens, starting with a letter, at most 63 characters. */
export const NAMESPACE_NAME = /^[a-z][a-z0-9-]{0,62}$/;

export interface NamespaceOptions {
  /** Namespaces besides `default`. */
  names?: readonly string[];
  /** The namespace every other one looks schema names up in after itself. */
  shared?: string;
}

/** A namespace as engine.namespaces lists it. */
export interface NamespaceRecord {
  name: string;
  /** configured: the engine's options name it, and only they change it; created: a create made it while an engine ran. */
  origin: 'configured' | 'created';
  /** Whether it is the shared namespace, which every other one looks schema names up in after itself. */
  shared: boolean;
  /** active, or archived: read as it was, refusing every write until it is unarchived. */
  state: 'active' | 'archived';
  /** When a create made it, and who; null for a configured one. */
  createdAt: number | null;
  createdBy: string | null;
  /** When it was archived, and who; null unless it is archived. */
  archivedAt: number | null;
  archivedBy: string | null;
}

// A row of engine_namespaces.
interface Created {
  readonly createdAt: number;
  readonly createdBy: string;
  readonly archivedAt: number | null;
  readonly archivedBy: string | null;
}

export class Namespaces {
  readonly shared: string | undefined;
  private readonly configured: readonly string[];
  private readonly created = new Map<string, Created>();
  private all: readonly string[];
  private bound: { storage: Storage; access: Access; clock: () => number } | undefined;
  private changes = 0;

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
    this.configured = names;
    this.all = names;
    this.shared = options.shared;
  }

  /**
   * Every namespace, archived ones included: the configured ones, `default`
   * first, then the ones a create made, in the order it made them.
   */
  get names(): readonly string[] {
    return this.all;
  }

  /** How many times a create, an archive or an unarchive changed the namespaces since the engine opened. */
  get generation(): number {
    return this.changes;
  }

  /**
   * bind reads the namespaces a create made from the engine's file and
   * lets this instance make, archive and list them; Engine.open calls it
   * once the engine's migrations have run.
   */
  bind(storage: Storage, access: Access, clock: () => number): void {
    this.bound = { storage, access, clock };
    this.created.clear();
    for (const row of storage.all('SELECT name, created_at, created_by, archived_at, archived_by FROM engine_namespaces ORDER BY created_at, name')) {
      this.created.set(String(row.name), createdOf(row));
    }
    this.refresh();
  }

  /** resolve returns the namespace a call names, `default` when it names none. */
  resolve(namespace: string | undefined): string {
    if (namespace === undefined) {
      return DEFAULT_NAMESPACE;
    }
    if (!this.all.includes(namespace)) {
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

  /** archived reports whether a namespace is archived: a created one, archived and not unarchived since. */
  archived(namespace: string): boolean {
    return !this.configured.includes(namespace) && (this.created.get(namespace)?.archivedAt ?? null) !== null;
  }

  /** requireWritable throws namespace_archived for an archived namespace, which refuses every write. */
  requireWritable(namespace: string): void {
    if (this.archived(namespace)) {
      throw new EngineError(
        'namespace_archived',
        `namespace ${namespace} is archived: it is read as it was and refuses every write until it is unarchived`
      );
    }
  }

  /**
   * list returns the namespaces the policy lets the principal list
   * (`manage`, operation `list`, asked of each), in the order of names.
   */
  list(principal: Principal): NamespaceRecord[] {
    checkPrincipal(principal);
    const { access } = this.binding();
    return this.all.filter((name) => access.allowsManage(principal, name, 'list')).map((name) => this.record(name));
  }

  /** get returns one namespace, which the policy must let the principal list. */
  get(principal: Principal, name: string): NamespaceRecord {
    checkPrincipal(principal);
    const namespace = this.known(name);
    this.binding().access.requireManage(principal, namespace, 'list');
    return this.record(namespace);
  }

  /**
   * create makes a namespace while the engine runs, which the policy must
   * allow (`manage`, operation `create`). A name that is a namespace
   * already, configured or created, archived or not, is a conflict.
   */
  create(principal: Principal, name: string): NamespaceRecord {
    checkPrincipal(principal);
    if (typeof name !== 'string' || !NAMESPACE_NAME.test(name)) {
      throw new EngineError(
        'invalid_argument',
        `a namespace name is lowercase letters, digits and hyphens, starting with a letter, at most 63 characters, not ${JSON.stringify(name)}`
      );
    }
    const { storage, access, clock } = this.binding();
    access.requireManage(principal, name, 'create');
    if (this.all.includes(name) || this.created.has(name)) {
      throw new EngineError('conflict', `namespace ${name} exists`);
    }
    const created: Created = { createdAt: clock(), createdBy: principal.subject, archivedAt: null, archivedBy: null };
    storage.transaction(() => {
      storage.run('INSERT INTO engine_namespaces (name, created_at, created_by) VALUES (?, ?, ?)', [name, created.createdAt, created.createdBy]);
      storage.afterCommit(() => this.change(name, created));
    });
    return this.record(name, created);
  }

  /**
   * archive archives a namespace a create made, which the policy must
   * allow (`manage`, operation `archive`): it is read as it was, and
   * refuses every write until it is unarchived. A configured namespace is
   * a conflict. Archiving an archived namespace changes nothing.
   */
  archive(principal: Principal, name: string): NamespaceRecord {
    return this.setArchived(principal, name, true);
  }

  /** unarchive lets an archived namespace be written again (`manage`, operation `unarchive`). An active one is left as it is. */
  unarchive(principal: Principal, name: string): NamespaceRecord {
    return this.setArchived(principal, name, false);
  }

  private setArchived(principal: Principal, name: string, archive: boolean): NamespaceRecord {
    checkPrincipal(principal);
    const namespace = this.known(name);
    const { storage, access, clock } = this.binding();
    access.requireManage(principal, namespace, archive ? 'archive' : 'unarchive');
    if (this.configured.includes(namespace)) {
      throw new EngineError(
        'conflict',
        `namespace ${namespace} is configured by the engine's options, which alone change it; only a namespace a create made is ${archive ? 'archived' : 'unarchived'}`
      );
    }
    const current = this.created.get(namespace) as Created;
    if ((current.archivedAt !== null) === archive) {
      return this.record(namespace);
    }
    const next: Created = archive
      ? { ...current, archivedAt: clock(), archivedBy: principal.subject }
      : { ...current, archivedAt: null, archivedBy: null };
    storage.transaction(() => {
      storage.run('UPDATE engine_namespaces SET archived_at = ?, archived_by = ? WHERE name = ?', [next.archivedAt, next.archivedBy, namespace]);
      storage.afterCommit(() => this.change(namespace, next));
    });
    return this.record(namespace, next);
  }

  // known resolves a namespace an archive, an unarchive or a get names:
  // unknown_namespace for one there is not.
  private known(name: string): string {
    if (typeof name !== 'string') {
      throw new EngineError('invalid_argument', `a namespace name is a string, not ${JSON.stringify(name)}`);
    }
    return this.resolve(name);
  }

  // record is a namespace as list returns it: configured, or created as
  // the file holds it, or as a write is making it.
  private record(name: string, made?: Created): NamespaceRecord {
    const created = this.configured.includes(name) ? undefined : (made ?? this.created.get(name));
    const archivedAt = created?.archivedAt ?? null;
    return {
      name,
      origin: created === undefined ? 'configured' : 'created',
      shared: name === this.shared,
      state: archivedAt === null ? 'active' : 'archived',
      createdAt: created?.createdAt ?? null,
      createdBy: created?.createdBy ?? null,
      archivedAt,
      archivedBy: created?.archivedBy ?? null,
    };
  }

  private change(name: string, created: Created): void {
    this.created.set(name, created);
    this.changes += 1;
    this.refresh();
  }

  private refresh(): void {
    this.all = [...this.configured, ...[...this.created.keys()].filter((name) => !this.configured.includes(name))];
  }

  private binding(): { storage: Storage; access: Access; clock: () => number } {
    if (this.bound === undefined) {
      throw new Error("these namespaces are not an engine's: openEngine binds them to its file");
    }
    return this.bound;
  }
}

function createdOf(row: Row): Created {
  return {
    createdAt: Number(row.created_at),
    createdBy: String(row.created_by),
    archivedAt: row.archived_at === null ? null : Number(row.archived_at),
    archivedBy: row.archived_by === null ? null : String(row.archived_by),
  };
}
