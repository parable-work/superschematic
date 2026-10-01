/*
Links, the core's typed links between instances (D16, amended). The
config names each link, the schema of the instance it points at, whether
it is required and whether it is pinned. A link holds one target at a
time, an instance of its schema in the same namespace, looked up as any
schema name is (the namespace, then the shared one). link points it at a
target, replacing the one it had; unlink clears it. One read-only field,
links, holds every link the instance has, since a declaration's fields
are fixed and the config's names are not.

A pinned link needs a target schema that composes Revisions: link
records the target's latest revision, or an earlier one it names, and
the links field reports whether the target has moved past it (stale),
read through the target's revision field as the caller.

Each link is also a reference the engine records, under the link's
name. The delete of a target a required link points at is refused by
guardReference, whoever the caller; a required link can be moved, not
unlinked. An optional link's target can be deleted: afterReferenceChange
unlinks it on each instance that points at it, as the caller, and that
instance's own event records it. Deleting an instance deletes its links.

listLinked is schema-level: the instances of the schema whose link
points at a target, with each pinned link's revision and staleness, a
page at a time, which answers which instances point at a superseded
revision.

configChange: every link keeps its name and schema, since instances may
hold it; required and pinned may change (a link made before its spec was
pinned records no revision until it is linked again), and links may be
added. Links can be added to a schema that has instances, which start
with none, and cannot be removed from one: their links and references
would stay behind.
*/

import { BehaviorVetoError, OperationParamsError } from '../../errors.js';
import type { Row } from '../../storage/driver.js';
import { defineBehavior, type BehaviorScope, type FrozenJSON, type InstanceView } from '../behavior.js';
import { page, pageRequest } from '../paging.js';
import declaration from './declarations/Links.behavior.json' with { type: 'json' };

/** One link of a Links config. */
export interface LinkSpec {
  /** The schema of the instance the link points at. */
  readonly schema: string;
  /** Once set, it is moved, never unlinked, and its target's delete is refused. */
  readonly required: boolean;
  /** It records the target's revision and reports whether the target moved past it. */
  readonly pinned: boolean;
}

/** Links' config, parsed. */
export interface LinksConfig {
  readonly links: Readonly<Record<string, LinkSpec>>;
}

/** One link, as the links field holds it. */
export interface LinkRecord {
  readonly schema: string;
  readonly id: string;
  /** The target's revision a pinned link records. */
  readonly revision?: number;
  /** Whether the target has moved past that revision. */
  readonly stale?: boolean;
}

const NAME = 'Links';
const COLUMNS = 'row, name, target_schema, target_id, revision';

function key(view: InstanceView<unknown>): [string, string, string] {
  return [view.namespace, view.schema, view.id];
}

// spec returns a link's spec, or refuses a name the config does not give.
function spec(scope: BehaviorScope<LinksConfig>, operation: string, name: string): LinkSpec {
  const found = Object.prototype.hasOwnProperty.call(scope.config.links, name) ? scope.config.links[name] : undefined;
  if (found === undefined) {
    throw new OperationParamsError(NAME, operation, [
      { path: '/name', message: `${scope.schema} has no link ${name} (its links: ${Object.keys(scope.config.links).join(', ')})` },
    ]);
  }
  return found;
}

// latest reads a target's latest revision through its revision field,
// as the caller; undefined when it has none yet.
function latest(scope: BehaviorScope<LinksConfig>, schema: string, id: string): number | undefined {
  const revision = scope.instances.get(schema, id, { fields: ['revision'] })?.data.revision;
  return typeof revision === 'number' ? revision : undefined;
}

// held reads the instance's link rows, by name.
function held(view: InstanceView<LinksConfig>): Row[] {
  return view.sql.all(`SELECT ${COLUMNS} FROM ${view.sql.table('links')} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY name`, key(view));
}

function linkOf(row: Row, current: number | undefined, pinned: boolean): LinkRecord {
  const base = { schema: String(row.target_schema), id: String(row.target_id) };
  if (!pinned || row.revision === null) {
    return base;
  }
  const revision = Number(row.revision);
  return { ...base, revision, ...(current === undefined ? {} : { stale: current > revision }) };
}

export const links = defineBehavior<LinksConfig>({
  declaration,

  parseConfig(json) {
    const raw = json as { links: Record<string, { schema: string; required?: boolean; pinned?: boolean }> };
    const parsed: Record<string, LinkSpec> = {};
    for (const [name, link] of Object.entries(raw.links)) {
      parsed[name] = { schema: link.schema, required: link.required === true, pinned: link.pinned === true };
    }
    return { links: parsed };
  },

  configChange(before, after) {
    if (before === undefined) {
      return undefined;
    }
    if (after === undefined) {
      return 'the links and references its instances hold would stay behind';
    }
    for (const [name, link] of Object.entries(before.links)) {
      const next = after.links[name];
      if (next === undefined) {
        return `link ${name} is gone, and its instances may hold it`;
      }
      if (next.schema !== link.schema) {
        return `link ${name} points at ${link.schema}, not ${next.schema}, in the instances that hold it`;
      }
    }
    return undefined;
  },

  migrations: [
    {
      version: 1,
      name: 'links',
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('links')} (
          row           INTEGER PRIMARY KEY,
          namespace     TEXT    NOT NULL,
          schema        TEXT    NOT NULL,
          id            TEXT    NOT NULL,
          name          TEXT    NOT NULL,
          target_schema TEXT    NOT NULL,
          target_id     TEXT    NOT NULL,
          revision      INTEGER,
          created_by    TEXT    NOT NULL,
          created_at    INTEGER NOT NULL,
          UNIQUE (namespace, schema, id, name)
        ) STRICT`);
        sql.run(`CREATE INDEX ${sql.table('links_by_target')} ON ${sql.table('links')} (namespace, schema, name, target_id, row)`);
      },
    },
  ],

  operations: {
    link(context, params) {
      const name = params.name as string;
      const link = spec(context, 'link', name);
      const id = params.id as string;
      const wanted = params.revision as number | undefined;
      if (wanted !== undefined && !link.pinned) {
        throw new OperationParamsError(NAME, 'link', [{ path: '/revision', message: `link ${name} is not pinned, so it records no revision` }]);
      }
      if (link.pinned && context.schemas.config(link.schema, 'Revisions') === undefined) {
        throw new OperationParamsError(NAME, 'link', [
          { path: '/name', message: `link ${name} is pinned, but ${link.schema} does not compose Revisions` },
        ]);
      }
      const target = context.instances.get(link.schema, id, { fields: link.pinned ? ['revision'] : [] });
      if (target === undefined) {
        throw new OperationParamsError(NAME, 'link', [{ path: '/id', message: `${link.schema} ${id} does not exist` }]);
      }
      let revision: number | undefined;
      if (link.pinned) {
        const current = typeof target.data.revision === 'number' ? target.data.revision : undefined;
        if (current === undefined) {
          throw new BehaviorVetoError(NAME, 'link', context.schema, context.id, `${link.schema} ${id} has no revision to pin yet`);
        }
        if (wanted !== undefined && wanted > current) {
          throw new OperationParamsError(NAME, 'link', [
            { path: '/revision', message: `${link.schema} ${id} has revisions 1 to ${current}, not ${wanted}` },
          ]);
        }
        revision = wanted ?? current;
      }
      const table = context.sql.table('links');
      const previous = context.sql.get(`SELECT target_schema, target_id FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND name = ?`, [
        ...key(context),
        name,
      ]);
      if (previous) {
        context.references.remove(String(previous.target_schema), String(previous.target_id), name);
      }
      context.sql.run(
        `INSERT INTO ${table} (namespace, schema, id, name, target_schema, target_id, revision, created_by, created_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT (namespace, schema, id, name) DO UPDATE SET
           target_schema = excluded.target_schema, target_id = excluded.target_id, revision = excluded.revision,
           created_by = excluded.created_by, created_at = excluded.created_at`,
        [...key(context), name, link.schema, id, revision ?? null, context.principal.subject, context.now]
      );
      context.references.add(link.schema, id, name);
      return { name, schema: link.schema, id, ...(revision === undefined ? {} : { revision }) };
    },

    // unlink reads nothing of the target, which may be the instance whose
    // delete is clearing its links.
    unlink(context, params) {
      const name = params.name as string;
      const link = spec(context, 'unlink', name);
      const table = context.sql.table('links');
      const row = context.sql.get(`SELECT ${COLUMNS} FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND name = ?`, [...key(context), name]);
      if (!row) {
        throw new OperationParamsError(NAME, 'unlink', [{ path: '/name', message: `${context.schema} ${context.id} has no link ${name}` }]);
      }
      if (link.required) {
        throw new BehaviorVetoError(NAME, 'unlink', context.schema, context.id, `link ${name} is required: it can be moved with link, not unlinked`);
      }
      context.sql.run(`DELETE FROM ${table} WHERE row = ?`, [Number(row.row)]);
      context.references.remove(String(row.target_schema), String(row.target_id), name);
      return { name, schema: String(row.target_schema), id: String(row.target_id), ...(row.revision === null ? {} : { revision: Number(row.revision) }) };
    },
  },

  schemaOperations: {
    listLinked(context, params) {
      const name = params.name as string;
      const link = spec(context, 'listLinked', name);
      const id = params.id as string;
      const { limit, after } = pageRequest(NAME, 'listLinked', params);
      const current = link.pinned ? latest(context, link.schema, id) : undefined;
      if (params.stale === true && (!link.pinned || current === undefined)) {
        return { items: [], next: null };
      }
      const rows = context.sql.all(
        `SELECT ${COLUMNS}, id FROM ${context.sql.table('links')}
         WHERE namespace = ? AND schema = ? AND name = ? AND target_id = ? AND row > ?${params.stale === true ? ' AND revision < ?' : ''}
         ORDER BY row LIMIT ?`,
        [context.namespace, context.schema, name, id, after, ...(params.stale === true ? [current as number] : []), limit + 1]
      );
      const { items, next } = page(rows, limit, (row) => Number(row.row));
      return {
        items: items.map((row) => {
          const record = linkOf(row, current, link.pinned);
          return { id: String(row.id), ...(record.revision === undefined ? {} : { revision: record.revision }), ...(record.stale === undefined ? {} : { stale: record.stale }) };
        }),
        next,
      };
    },
  },

  fields: {
    links: (view) => {
      const rows = held(view);
      if (rows.length === 0) {
        return undefined;
      }
      const out: Record<string, LinkRecord> = {};
      for (const row of rows) {
        const name = String(row.name);
        const pinned = view.config.links[name]?.pinned === true;
        const current = pinned && row.revision !== null ? latest(view, String(row.target_schema), String(row.target_id)) : undefined;
        out[name] = linkOf(row, current, pinned);
      }
      return out;
    },
  },

  // The delete of a target a required link points at is refused, whoever
  // the caller. The reason names the schema, not the instance, which the
  // caller may not be able to read.
  guardReference(view, reference, request) {
    if (request.kind === 'delete' && view.config.links[reference.key]?.required === true) {
      return `an instance of ${view.schema} links to it through its required link ${reference.key}`;
    }
    return undefined;
  },

  // An optional link's target's delete unlinks it, through unlink, as the caller.
  afterReferenceChange(context, reference, change) {
    if (change.kind === 'delete') {
      context.instances.invoke(context.schema, context.id, 'unlink', { name: reference.key } as FrozenJSON);
    }
  },

  afterChange(context, change) {
    if (change.kind === 'delete') {
      context.sql.run(`DELETE FROM ${context.sql.table('links')} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
    }
  },
});
