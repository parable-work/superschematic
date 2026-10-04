/*
Links, the core's typed links between instances (D16, amended). The
config names each link, the schema of the instance it points at, whether
it is required and whether it is pinned. A link holds one target at a
time, an instance of its schema in the same namespace, looked up as any
schema name is (the namespace, then the shared one). link points it at a
target, replacing the one it had; unlink clears it. A create may give
links too, by name, in its parameters (initialize), held to link's
checks in the create's transaction, so an instance holds them from its
first event. One read-only field, links, holds every link the instance
has, since a declaration's fields are fixed and the config's names are
not.

A pinned link needs a target schema that composes Revisions: link
records the target's latest revision, or an earlier one it names, and
the links field reports whether the target has moved past it (stale),
read through the target's revision field as the caller.

Each link is also a reference the engine records, under the link's
name. A required link is given at create, which refuses an instance
without it, can be moved and not unlinked, and the delete of the target
it points at is refused by guardReference, whoever the caller: an
instance always holds it. An optional link's target can be deleted:
afterReferenceChange unlinks it on each instance that points at it, as
the caller, and that instance's own event records it. Deleting an
instance deletes its links.

Every veto carries a code the declaration lists: no_revision for a
pinned link's target with none yet, at link or at create (with the
pointer of the create's entry), required_link for unlinking a required
link, and required_target for deleting what one points at.

listLinked is schema-level: the instances of the schema whose link
points at a target, with each pinned link's revision and staleness, a
page at a time, which answers which instances point at a superseded
revision.

configChange: every link keeps its name and schema, since instances may
hold it; pinned may change (a link made before its spec was pinned
records no revision until it is linked again), a required link may
become optional, and optional links may be added. A link that becomes
required, or a new required one, is refused, as a field made required
is: an instance the live version accepts may not hold it. Links can be
added to a schema that has instances, which start with none, unless it
has a required link, and cannot be removed from one: their links and
references would stay behind.
*/

import { BehaviorVetoError, CreateParamsError, OperationParamsError, type SchemaIssue } from '../../errors.js';
import type { Row } from '../../storage/driver.js';
import { defineBehavior, type BehaviorScope, type FrozenJSON, type InstanceContext, type InstanceView } from '../behavior.js';
import { page, pageRequest } from '../paging.js';
import declaration from './declarations/Links.behavior.json' with { type: 'json' };

/** One link of a Links config. */
export interface LinkSpec {
  /** The schema of the instance the link points at. */
  readonly schema: string;
  /** Every create gives it; it is moved, never unlinked, and its target's delete is refused. */
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

/** The codes of Links' vetoes, as its declaration lists them. */
type LinksVeto = 'no_revision' | 'required_link' | 'required_target';

/**
 * How a link's checks refuse: at the parameter they name (the link's
 * name, the target's id, the revision), or as a veto with its declared
 * code. link refuses its own parameters; a create, the link's entry of
 * its parameters, and its veto is the create's.
 */
interface LinkRefusals {
  param(at: 'name' | 'id' | 'revision', message: string): Error;
  veto(reason: string, code: LinksVeto): Error;
}

function operationRefusals(context: InstanceContext<LinksConfig>): LinkRefusals {
  return {
    param: (at, message) => new OperationParamsError(NAME, 'link', [{ path: `/${at}`, message }]),
    veto: (reason, code) => new BehaviorVetoError(NAME, 'link', context.schema, context.id, { reason, code }),
  };
}

// createRefusals points at the link's entry of a create's parameters: the
// entry itself for its name, or for its id when it is the id alone.
function createRefusals(context: InstanceContext<LinksConfig>, name: string, idOnly: boolean): LinkRefusals {
  const entry = `/behaviors/${NAME}/${name}`;
  return {
    param: (at, message) =>
      new CreateParamsError(context.schema, [{ path: at === 'name' || (at === 'id' && idOnly) ? entry : `${entry}/${at}`, message }]),
    veto: (reason, code) => new BehaviorVetoError(NAME, 'create', context.schema, context.id, { reason, code, details: { path: entry } }),
  };
}

/**
 * setLink points a link at a target, with link's checks: a revision only
 * for a pinned link, whose schema composes Revisions, and no later than
 * the target's latest; a target that exists, read as the caller, with a
 * revision to pin. It records the link and its reference, replacing the
 * target it had, and returns what link returns.
 */
function setLink(
  context: InstanceContext<LinksConfig>,
  name: string,
  link: LinkSpec,
  id: string,
  wanted: number | undefined,
  refuse: LinkRefusals
): { name: string; schema: string; id: string; revision?: number } {
  if (wanted !== undefined && !link.pinned) {
    throw refuse.param('revision', `link ${name} is not pinned, so it records no revision`);
  }
  if (link.pinned && context.schemas.config(link.schema, 'Revisions') === undefined) {
    throw refuse.param('name', `link ${name} is pinned, but ${link.schema} does not compose Revisions`);
  }
  const target = context.instances.get(link.schema, id, { fields: link.pinned ? ['revision'] : [] });
  if (target === undefined) {
    throw refuse.param('id', `${link.schema} ${id} does not exist`);
  }
  let revision: number | undefined;
  if (link.pinned) {
    const current = typeof target.data.revision === 'number' ? target.data.revision : undefined;
    if (current === undefined) {
      throw refuse.veto(`${link.schema} ${id} has no revision to pin yet`, 'no_revision');
    }
    if (wanted !== undefined && wanted > current) {
      throw refuse.param('revision', `${link.schema} ${id} has revisions 1 to ${current}, not ${wanted}`);
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
    if (after === undefined) {
      return 'the links and references its instances hold would stay behind';
    }
    if (before === undefined) {
      const required = Object.keys(after.links).filter((name) => after.links[name].required);
      return required.length > 0 ? `the instances that exist hold no link ${required.join(', ')}, which is required` : undefined;
    }
    for (const [name, link] of Object.entries(before.links)) {
      const next = Object.prototype.hasOwnProperty.call(after.links, name) ? after.links[name] : undefined;
      if (next === undefined) {
        return `link ${name} is gone, and its instances may hold it`;
      }
      if (next.schema !== link.schema) {
        return `link ${name} points at ${link.schema}, not ${next.schema}, in the instances that hold it`;
      }
      if (next.required && !link.required) {
        return `link ${name} becomes required, and an instance the live version accepts may not hold it`;
      }
    }
    for (const [name, link] of Object.entries(after.links)) {
      if (link.required && !Object.prototype.hasOwnProperty.call(before.links, name)) {
        return `link ${name} is new and required, and an instance the live version accepts holds none`;
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

  // A create's links: every name one the config gives, every required
  // link among them, each set with link's checks.
  initialize(context, params) {
    const links = context.config.links;
    const issues: SchemaIssue[] = [];
    for (const name of Object.keys(params)) {
      if (!Object.prototype.hasOwnProperty.call(links, name)) {
        issues.push({ path: `/behaviors/${NAME}/${name}`, message: `${context.schema} has no link ${name} (its links: ${Object.keys(links).join(', ')})` });
      }
    }
    for (const [name, link] of Object.entries(links)) {
      if (link.required && !Object.prototype.hasOwnProperty.call(params, name)) {
        issues.push({ path: `/behaviors/${NAME}`, message: `link ${name} is required, so a create of ${context.schema} gives it` });
      }
    }
    if (issues.length > 0) {
      throw new CreateParamsError(context.schema, issues);
    }
    for (const [name, value] of Object.entries(params)) {
      const target = typeof value === 'string' ? { id: value } : (value as { id: string; revision?: number });
      setLink(context, name, links[name], target.id, target.revision, createRefusals(context, name, typeof value === 'string'));
    }
  },

  operations: {
    link(context, params) {
      const name = params.name as string;
      const link = spec(context, 'link', name);
      return setLink(context, name, link, params.id as string, params.revision as number | undefined, operationRefusals(context));
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
        throw new BehaviorVetoError(NAME, 'unlink', context.schema, context.id, {
          reason: `link ${name} is required: it can be moved with link, not unlinked`,
          code: 'required_link',
        });
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
      return { reason: `an instance of ${view.schema} links to it through its required link ${reference.key}`, code: 'required_target' };
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
