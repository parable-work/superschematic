/*
Links, the core's typed links between instances (D16, amended). The
config names each link, the schema of the instance it points at, whether
it is required and whether it is pinned. A link holds one target at a
time, an instance of its schema in the same namespace, looked up as any
schema name is (the namespace, then the shared one). link points it at a
target, replacing the one it had; unlink clears it. A create may give
links too, by name, in its parameters (initialize), held to link's
checks in the create's transaction, so an instance holds them from its
first event. One read-only field, targets, holds every link the instance
has, since a declaration's fields are fixed and the config's names are
not; a read returns it as Links.targets.

A pinned link pins a revision of Revisions or a release of Branches, as
its config's pinned says (true and "revision" pin a revision, "release"
a release), and needs a target schema that composes that behavior: link
records the target's latest revision or release, or an earlier one it
names, and the links field reports the target's latest beside it
(latest) and whether the target has moved past it (stale), read through
the target's revision or release field as the caller. A row keeps a
revision and a release in columns of their own, so a config that changes
what a link pins finds no pin of the new kind in a row until the link is
linked again.

Each link is also a reference the engine records, under the link's
name, which hears the target's delete alone: no other change of the
target asks Links anything, so a target thousands of instances link to
costs its writes none of them. A required link is given at create, which
refuses an instance without it, can be moved and not unlinked, and the
delete of the target it points at is refused by guardReference, whoever
the caller: an instance always holds it. An optional link's target can be deleted:
afterReferenceChange unlinks it on each instance that points at it, as
the caller, and that instance's own event records it. Deleting an
instance deletes its links.

Every veto carries a code the declaration lists: no_revision and
no_release for a pinned link's target with none yet, at link or at
create (with the pointer of the create's entry), required_link for
unlinking a required link, and required_target for deleting what one
points at.

listLinked is schema-level: the instances of the schema whose link
points at a target, with each pinned link's pin, the target's latest and
staleness, a page at a time, which answers which instances point at a
superseded revision or release.

configChange: while the schema has instances, every link keeps its name
and schema, since instances may hold it; pinned may change (a link made
before its spec was pinned, or while it was pinned to the other kind,
records no pin of the kind until it is linked again), a required link
may become optional, and optional links may be added. A link that
becomes required, or a new required one, is refused, as a field made
required is: an instance the live version accepts may not hold it. A
schema with no instance, in any namespace that reads it, may change the
config in any way, since no instance holds a link or lacks one. Links
can be added to a schema that has instances, which start with none,
unless it has a required link, and cannot be removed from one: their
links and references would stay behind.
*/

import { behaviorField, fieldPath } from '../fields.js';
import { BehaviorVetoError, CreateParamsError, OperationParamsError, type SchemaIssue } from '../../errors.js';
import type { Row } from '../../storage/driver.js';
import { defineBehavior, type BehaviorScope, type FrozenJSON, type InstanceContext, type InstanceView } from '../behavior.js';
import { page, pageRequest } from '../paging.js';
import declaration from './declarations/Links.behavior.json' with { type: 'json' };
import { linksCreateParams, linksGuidance } from './guidance/links.js';

/** What a pinned link pins: a revision of Revisions, or a release of Branches. */
export type LinkPin = 'revision' | 'release';

/**
 * linkPin reads what a link of a Links config, as a schema holds it, pins:
 * pinned true or "revision" pins a revision, "release" a release; any
 * other value pins nothing.
 */
export function linkPin(link: unknown): LinkPin | undefined {
  const pinned = (link as { pinned?: unknown } | undefined)?.pinned;
  if (pinned === true || pinned === 'revision') {
    return 'revision';
  }
  return pinned === 'release' ? 'release' : undefined;
}

/** One link of a Links config. */
export interface LinkSpec {
  /** The schema of the instance the link points at. */
  readonly schema: string;
  /** Every create gives it; it is moved, never unlinked, and its target's delete is refused. */
  readonly required: boolean;
  /** What it pins of the target, reporting whether the target moved past it; absent when it pins nothing. */
  readonly pin?: LinkPin;
}

/** Links' config, parsed. */
export interface LinksConfig {
  readonly links: Readonly<Record<string, LinkSpec>>;
}

/** One link, as the links field holds it. */
export interface LinkRecord {
  readonly schema: string;
  readonly id: string;
  /** The target's revision a link that pins revisions records. */
  readonly revision?: number;
  /** The target's release a link that pins releases records. */
  readonly release?: number;
  /** The target's latest revision or release, of the kind the link pins. */
  readonly latest?: number;
  /** Whether the target has moved past the pinned one. */
  readonly stale?: boolean;
}

const NAME = 'Links';
const COLUMNS = 'row, name, target_schema, target_id, revision, release';

// What each kind of pin needs: the behavior the target's schema composes,
// the target's field that holds its latest, the column a row keeps the pin
// in, and the veto when the target has none yet. link's parameter for an
// earlier one is named like the kind.
const PINS = {
  revision: { behavior: 'Revisions', field: 'revision', column: 'revision', code: 'no_revision' },
  release: { behavior: 'Branches', field: 'release', column: 'release', code: 'no_release' },
} as const;

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

// latest reads a target's latest revision or release, through its
// revision or release field, as the caller; undefined when it has none
// yet.
function latest(scope: BehaviorScope<LinksConfig>, pin: LinkPin, schema: string, id: string): number | undefined {
  const { behavior, field } = PINS[pin];
  const value = behaviorField(scope.instances.get(schema, id, { fields: [fieldPath(behavior, field)] }), behavior, field);
  return typeof value === 'number' ? value : undefined;
}

// heldPin is the pin a row keeps of the kind the link pins; undefined
// when it pins nothing, or keeps none of that kind.
function heldPin(row: Row, pin: LinkPin | undefined): number | undefined {
  const value = pin === undefined ? null : row[PINS[pin].column];
  return value === null || value === undefined ? undefined : Number(value);
}

// held reads the instance's link rows, by name.
function held(view: InstanceView<LinksConfig>): Row[] {
  return view.sql.all(`SELECT ${COLUMNS} FROM ${view.sql.table('links')} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY name`, key(view));
}

/** The codes of Links' vetoes, as its declaration lists them. */
type LinksVeto = 'no_revision' | 'no_release' | 'required_link' | 'required_target';

/**
 * How a link's checks refuse: at the parameter they name (the link's
 * name, the target's id, the revision or the release), or as a veto with
 * its declared code. link refuses its own parameters; a create, the
 * link's entry of its parameters, and its veto is the create's.
 */
interface LinkRefusals {
  param(at: 'name' | 'id' | 'revision' | 'release', message: string): Error;
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

/** What link and a create's entry name of a target: its id, and an earlier revision or release to pin. */
interface LinkTarget {
  readonly id: string;
  readonly revision?: number;
  readonly release?: number;
}

/**
 * setLink points a link at a target, with link's checks: a revision only
 * for a link that pins revisions, whose schema composes Revisions, a
 * release only for one that pins releases, whose schema composes
 * Branches, each no later than the target's latest; a target that
 * exists, read as the caller, with a revision or a release to pin. It
 * records the link and its reference, replacing the target it had, and
 * returns what link returns.
 */
function setLink(
  context: InstanceContext<LinksConfig>,
  name: string,
  link: LinkSpec,
  given: LinkTarget,
  refuse: LinkRefusals
): { name: string; schema: string; id: string; revision?: number; release?: number } {
  const pin = link.pin;
  for (const kind of ['revision', 'release'] as const) {
    if (given[kind] !== undefined && pin !== kind) {
      throw refuse.param(kind, `link ${name} ${pin === undefined ? 'is not pinned' : `is pinned to a ${pin}`}, so it records no ${kind}`);
    }
  }
  if (pin !== undefined && context.schemas.config(link.schema, PINS[pin].behavior) === undefined) {
    throw refuse.param('name', `link ${name} is pinned to a ${pin}, but ${link.schema} does not compose ${PINS[pin].behavior}`);
  }
  const target = context.instances.get(link.schema, given.id, { fields: pin === undefined ? [] : [fieldPath(PINS[pin].behavior, PINS[pin].field)] });
  if (target === undefined) {
    throw refuse.param('id', `${link.schema} ${given.id} does not exist`);
  }
  let value: number | undefined;
  if (pin !== undefined) {
    const found = behaviorField(target, PINS[pin].behavior, PINS[pin].field);
    const current = typeof found === 'number' ? found : undefined;
    if (current === undefined) {
      throw refuse.veto(`${link.schema} ${given.id} has no ${pin} to pin yet`, PINS[pin].code);
    }
    const wanted = given[pin];
    if (wanted !== undefined && wanted > current) {
      throw refuse.param(pin, `${link.schema} ${given.id} has ${pin}s 1 to ${current}, not ${wanted}`);
    }
    value = wanted ?? current;
  }
  const revision = pin === 'revision' ? value : undefined;
  const release = pin === 'release' ? value : undefined;
  const id = given.id;
  const table = context.sql.table('links');
  const previous = context.sql.get(`SELECT target_schema, target_id FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND name = ?`, [
    ...key(context),
    name,
  ]);
  if (previous) {
    context.references.remove(String(previous.target_schema), String(previous.target_id), name);
  }
  context.sql.run(
    `INSERT INTO ${table} (namespace, schema, id, name, target_schema, target_id, revision, release, created_by, created_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
     ON CONFLICT (namespace, schema, id, name) DO UPDATE SET
       target_schema = excluded.target_schema, target_id = excluded.target_id, revision = excluded.revision,
       release = excluded.release, created_by = excluded.created_by, created_at = excluded.created_at`,
    [...key(context), name, link.schema, id, revision ?? null, release ?? null, context.principal.subject, context.now]
  );
  context.references.add(link.schema, id, name, 'delete');
  return { name, schema: link.schema, id, ...(revision === undefined ? {} : { revision }), ...(release === undefined ? {} : { release }) };
}

// linkOf is a link as the links field and listLinked give it: the pin of
// the kind the link pins, and with the target's latest of that kind, the
// latest and whether the target has moved past the pin.
function linkOf(row: Row, current: number | undefined, pin: LinkPin | undefined): LinkRecord {
  const base = { schema: String(row.target_schema), id: String(row.target_id) };
  const value = heldPin(row, pin);
  if (pin === undefined || value === undefined) {
    return base;
  }
  return { ...base, [pin]: value, ...(current === undefined ? {} : { latest: current, stale: current > value }) };
}

export const links = defineBehavior<LinksConfig>({
  declaration,

  guidance: linksGuidance,

  createParamsSchema: linksCreateParams,

  parseConfig(json) {
    const raw = json as { links: Record<string, { schema: string; required?: boolean; pinned?: boolean | LinkPin }> };
    const parsed: Record<string, LinkSpec> = {};
    for (const [name, link] of Object.entries(raw.links)) {
      const pin = linkPin(link);
      parsed[name] = { schema: link.schema, required: link.required === true, ...(pin === undefined ? {} : { pin }) };
    }
    return { links: parsed };
  },

  configChange(before, after, change) {
    if (after === undefined) {
      return 'the links and references its instances hold would stay behind';
    }
    if (before === undefined) {
      const required = Object.keys(after.links).filter((name) => after.links[name].required);
      return required.length > 0 ? `the instances that exist hold no link ${required.join(', ')}, which is required` : undefined;
    }
    // No instance holds a link, or lacks one, that the new config refuses.
    if (!change.instances) {
      return undefined;
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
    {
      // A link that pins a release of Branches keeps it beside the revision
      // column, which a link that pins a revision of Revisions keeps.
      version: 2,
      name: 'release pins',
      up(sql) {
        sql.run(`ALTER TABLE ${sql.table('links')} ADD COLUMN release INTEGER`);
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
      const target = typeof value === 'string' ? { id: value } : (value as LinkTarget);
      setLink(context, name, links[name], target, createRefusals(context, name, typeof value === 'string'));
    }
  },

  operations: {
    link(context, params) {
      const name = params.name as string;
      const link = spec(context, 'link', name);
      return setLink(
        context,
        name,
        link,
        { id: params.id as string, revision: params.revision as number | undefined, release: params.release as number | undefined },
        operationRefusals(context)
      );
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
      return {
        name,
        schema: String(row.target_schema),
        id: String(row.target_id),
        ...(row.revision === null ? {} : { revision: Number(row.revision) }),
        ...(row.release === null ? {} : { release: Number(row.release) }),
      };
    },
  },

  schemaOperations: {
    listLinked(context, params) {
      const name = params.name as string;
      const link = spec(context, 'listLinked', name);
      const id = params.id as string;
      const { limit, after } = pageRequest(NAME, 'listLinked', params);
      const pin = link.pin;
      const current = pin === undefined ? undefined : latest(context, pin, link.schema, id);
      if (params.stale === true && (pin === undefined || current === undefined)) {
        return { items: [], next: null };
      }
      const rows = context.sql.all(
        `SELECT ${COLUMNS}, id FROM ${context.sql.table('links')}
         WHERE namespace = ? AND schema = ? AND name = ? AND target_id = ? AND row > ?${params.stale === true ? ` AND ${PINS[pin as LinkPin].column} < ?` : ''}
         ORDER BY row LIMIT ?`,
        [context.namespace, context.schema, name, id, after, ...(params.stale === true ? [current as number] : []), limit + 1]
      );
      const { items, next } = page(rows, limit, (row) => Number(row.row));
      return {
        items: items.map((row) => {
          const record = linkOf(row, current, pin);
          return {
            id: String(row.id),
            ...(record.revision === undefined ? {} : { revision: record.revision }),
            ...(record.release === undefined ? {} : { release: record.release }),
            ...(record.latest === undefined ? {} : { latest: record.latest }),
            ...(record.stale === undefined ? {} : { stale: record.stale }),
          };
        }),
        next,
      };
    },
  },

  fields: {
    targets: (view) => {
      const rows = held(view);
      if (rows.length === 0) {
        return undefined;
      }
      const out: Record<string, LinkRecord> = {};
      for (const row of rows) {
        const name = String(row.name);
        const pin = Object.prototype.hasOwnProperty.call(view.config.links, name) ? view.config.links[name].pin : undefined;
        const current = pin !== undefined && heldPin(row, pin) !== undefined ? latest(view, pin, String(row.target_schema), String(row.target_id)) : undefined;
        out[name] = linkOf(row, current, pin);
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
