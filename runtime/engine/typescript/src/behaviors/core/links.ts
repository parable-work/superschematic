/*
Links, the core's typed links between instances (D16, amended; D56). The
config names each link, the schema of the instance it points at, whether
it is required, whether it is pinned, and whether it holds many targets.
A link holds one target at a time, an instance of its schema in the same
namespace, looked up as any schema name is (the namespace, then the
shared one); link points it at a target, replacing the one it had;
unlink clears it. A many-valued link (many, with a max from 1 to 100)
holds a list of them, in the order they were linked: link adds a target,
or pins again one it holds, refusing one past max (too_many), and unlink
removes the one it names (not_linked when the link does not hold it). A
create may give links too, by name, in its parameters (initialize), a
list for a many-valued link with each target once, held to link's checks
in the create's transaction, so an instance holds them from its first
event. One read-only field, targets, holds every link the instance has,
since a declaration's fields are fixed and the config's names are not; a
read returns it as Links.targets, with a list for each many-valued link.

A pinned link pins a revision of Revisions or a release of Branches, as
its config's pinned says (true and "revision" pin a revision, "release"
a release), and needs a target schema that composes that behavior: link
records the target's latest revision or release, or an earlier one it
names, and the links field reports the target's latest beside it
(latest) and whether the target has moved past it (stale), read through
the target's revision or release field as the caller, one read per
target schema. A row keeps a revision and a release in columns of their
own, so a config that changes what a link pins finds no pin of the new
kind in a row until the link is linked again.

Each target of a link is also a reference the engine records, under the
link's name, which hears the target's delete alone: no other change of
the target asks Links anything, so a target thousands of instances link
to costs its writes none of them. A required link is given at create,
which refuses an instance without it, can be moved and not unlinked, and
the delete of the target it points at is refused by guardReference,
whoever the caller: an instance always holds it. A required many-valued
link holds at least one target: unlinking its last is refused, and so is
the delete of its last target. Any other target can be deleted:
afterReferenceChange unlinks it on each instance that holds it, as the
caller, that one target of a many-valued link, and that instance's own
event records it. Deleting an instance deletes its links.

Every veto carries a code the declaration lists: no_revision and
no_release for a pinned link's target with none yet, at link or at
create (with the pointer of the create's entry), too_many for a target
past a many-valued link's max, not_linked for unlinking a target the
link does not hold, required_link for unlinking a required link or the
last target of a required many-valued one, and required_target for
deleting what one points at.

listLinked is schema-level: the instances of the schema whose link
points at a target, or holds it, with each pinned link's pin, the
target's latest and staleness, a page at a time, which answers which
instances point at a superseded revision or release.

configChange: while the schema has instances, every link keeps its name
and schema and whether it holds many targets, and a many-valued link's
max does not fall, since instances may hold it; pinned may change (a
link made before its spec was pinned, or while it was pinned to the
other kind, records no pin of the kind until it is linked again), a
required link may become optional, max may rise, and optional links may
be added. A link that becomes required, or a new required one, is
refused, as a field made required is: an instance the live version
accepts may not hold it. A schema with no instance, in any namespace
that reads it, may change the config in any way, since no instance holds
a link or lacks one. Links can be added to a schema that has instances,
which start with none, unless it has a required link, and cannot be
removed from one: their links and references would stay behind.
*/

import { behaviorField, fieldPath } from '../fields.js';
import { BehaviorVetoError, CreateParamsError, OperationParamsError, type SchemaIssue } from '../../errors.js';
import type { InstanceRecord } from '../../instances/store.js';
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

/**
 * linkMany reads whether a link of a Links config, as a schema holds it,
 * holds a list of targets: many true. What reads one target of a link
 * refuses one that does.
 */
export function linkMany(link: unknown): boolean {
  return (link as { many?: unknown } | undefined)?.many === true;
}

/** One link of a Links config. */
export interface LinkSpec {
  /** The schema of the instance the link points at. */
  readonly schema: string;
  /** Every create gives it; it is moved, never unlinked, and its target's delete is refused; a many-valued one keeps at least one target. */
  readonly required: boolean;
  /** What it pins of the target, reporting whether the target moved past it; absent when it pins nothing. */
  readonly pin?: LinkPin;
  /** Whether it holds a list of targets, in the order they were linked, rather than one. */
  readonly many: boolean;
  /** For a many-valued link, the most targets it holds. */
  readonly max?: number;
}

/** Links' config, parsed. */
export interface LinksConfig {
  readonly links: Readonly<Record<string, LinkSpec>>;
}

/** One link, or one target of a many-valued link, as the links field holds it. */
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
// The most ids one getMany reads.
const BATCH = 500;
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

function own<T>(record: Readonly<Record<string, T>>, name: string): T | undefined {
  return Object.prototype.hasOwnProperty.call(record, name) ? record[name] : undefined;
}

// spec returns a link's spec, or refuses a name the config does not give.
function spec(scope: BehaviorScope<LinksConfig>, operation: string, name: string): LinkSpec {
  const found = own(scope.config.links, name);
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

// held reads the instance's link rows, by name, each link's in the order
// its targets were linked.
function held(view: InstanceView<LinksConfig>): Row[] {
  return view.sql.all(`SELECT ${COLUMNS} FROM ${view.sql.table('links')} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY name, row`, key(view));
}

// heldBy reads the rows of one link of the instance, in the order its
// targets were linked.
function heldBy(view: InstanceView<LinksConfig>, name: string): Row[] {
  return view.sql.all(`SELECT ${COLUMNS} FROM ${view.sql.table('links')} WHERE namespace = ? AND schema = ? AND id = ? AND name = ? ORDER BY row`, [
    ...key(view),
    name,
  ]);
}

/** The codes of Links' vetoes, as its declaration lists them. */
type LinksVeto = 'no_revision' | 'no_release' | 'required_link' | 'required_target' | 'too_many' | 'not_linked';

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

// createRefusals points at the link's entry of a create's parameters, or
// at the item of a many-valued link's list (index): the link's entry for
// its name, the item itself for its id when it is the id alone.
function createRefusals(context: InstanceContext<LinksConfig>, name: string, index: number | undefined, idOnly: boolean): LinkRefusals {
  const entry = `/behaviors/${NAME}/${name}`;
  const item = index === undefined ? entry : `${entry}/${index}`;
  return {
    param: (at, message) =>
      new CreateParamsError(context.schema, [{ path: at === 'name' ? entry : at === 'id' && idOnly ? item : `${item}/${at}`, message }]),
    veto: (reason, code) => new BehaviorVetoError(NAME, 'create', context.schema, context.id, { reason, code, details: { path: item } }),
  };
}

/** What link and a create's entry name of a target: its id, and an earlier revision or release to pin. */
interface LinkTarget {
  readonly id: string;
  readonly revision?: number;
  readonly release?: number;
}

// targetOf reads a target as a create's entry gives it: its id, or an
// object with its id and a pin.
function targetOf(value: unknown): LinkTarget {
  return typeof value === 'string' ? { id: value } : (value as LinkTarget);
}

/**
 * setLink points a link at a target, with link's checks: a revision only
 * for a link that pins revisions, whose schema composes Revisions, a
 * release only for one that pins releases, whose schema composes
 * Branches, each no later than the target's latest; a target that
 * exists, read as the caller, with a revision or a release to pin; and
 * for a many-valued link that does not hold it yet, room under max. It
 * records the link and its reference, replacing the target a link that
 * holds one had in its row, adding the target to a many-valued link or
 * pinning again the one it holds, and returns what link returns.
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
  // A link that holds one target keeps one row, which a move updates; a
  // many-valued link keeps a row per target, which pinning again updates.
  const rows = heldBy(context, name);
  const row = link.many ? rows.find((one) => String(one.target_id) === id) : rows[0];
  const pins = [revision ?? null, release ?? null, context.principal.subject, context.now];
  if (row === undefined) {
    if (link.many && rows.length >= (link.max as number)) {
      throw refuse.veto(`link ${name} holds ${link.max} targets already, its max`, 'too_many');
    }
    context.sql.run(
      `INSERT INTO ${table} (namespace, schema, id, name, target_schema, target_id, revision, release, created_by, created_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      [...key(context), name, link.schema, id, ...pins]
    );
  } else {
    if (!link.many) {
      context.references.remove(String(row.target_schema), String(row.target_id), name);
    }
    context.sql.run(`UPDATE ${table} SET target_schema = ?, target_id = ?, revision = ?, release = ?, created_by = ?, created_at = ? WHERE row = ?`, [
      link.schema,
      id,
      ...pins,
      Number(row.row),
    ]);
  }
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

// latestOf reads the latest revision or release of each pinned target,
// as the caller, one read per target schema: a map by schema and id,
// without the targets that have none yet.
function latestOf(view: InstanceView<LinksConfig>, wanted: ReadonlyArray<{ schema: string; id: string; pin: LinkPin }>): Map<string, number> {
  const bySchema = new Map<string, { ids: Set<string>; pins: Set<LinkPin> }>();
  for (const { schema, id, pin } of wanted) {
    const entry = bySchema.get(schema) ?? { ids: new Set<string>(), pins: new Set<LinkPin>() };
    entry.ids.add(id);
    entry.pins.add(pin);
    bySchema.set(schema, entry);
  }
  const found = new Map<string, InstanceRecord>();
  for (const [schema, { ids, pins }] of bySchema) {
    const all = [...ids];
    const fields = [...pins].map((pin) => fieldPath(PINS[pin].behavior, PINS[pin].field));
    for (let start = 0; start < all.length; start += BATCH) {
      for (const [id, record] of view.instances.getMany(schema, all.slice(start, start + BATCH), { fields })) {
        found.set(`${schema} ${id}`, record);
      }
    }
  }
  const out = new Map<string, number>();
  for (const { schema, id, pin } of wanted) {
    const value = behaviorField(found.get(`${schema} ${id}`), PINS[pin].behavior, PINS[pin].field);
    if (typeof value === 'number') {
      out.set(`${schema} ${id} ${pin}`, value);
    }
  }
  return out;
}

export const links = defineBehavior<LinksConfig>({
  declaration,

  guidance: linksGuidance,

  createParamsSchema: linksCreateParams,

  parseConfig(json) {
    const raw = json as { links: Record<string, { schema: string; required?: boolean; pinned?: boolean | LinkPin; many?: boolean; max?: number }> };
    const parsed: Record<string, LinkSpec> = {};
    for (const [name, link] of Object.entries(raw.links)) {
      const pin = linkPin(link);
      const many = linkMany(link);
      parsed[name] = {
        schema: link.schema,
        required: link.required === true,
        ...(pin === undefined ? {} : { pin }),
        many,
        ...(many ? { max: link.max } : {}),
      };
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
      const next = own(after.links, name);
      if (next === undefined) {
        return `link ${name} is gone, and its instances may hold it`;
      }
      if (next.schema !== link.schema) {
        return `link ${name} points at ${link.schema}, not ${next.schema}, in the instances that hold it`;
      }
      if (next.required && !link.required) {
        return `link ${name} becomes required, and an instance the live version accepts may not hold it`;
      }
      if (next.many !== link.many) {
        return `link ${name} holds ${link.many ? 'a list of targets' : 'one target'} in the instances that hold it, not ${next.many ? 'a list' : 'one'}`;
      }
      if (link.many && (next.max as number) < (link.max as number)) {
        return `link ${name} may hold ${link.max} targets in the instances that hold it, more than ${next.max}`;
      }
    }
    for (const [name, link] of Object.entries(after.links)) {
      if (link.required && own(before.links, name) === undefined) {
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
    {
      // A many-valued link keeps a row per target, so a row is keyed by its
      // target as well as its link's name. The table is built again with
      // that key, its rows copied with their row, which listLinked pages by
      // and a many-valued link's targets are ordered by.
      version: 3,
      name: 'many-valued links',
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('links_next')} (
          row           INTEGER PRIMARY KEY,
          namespace     TEXT    NOT NULL,
          schema        TEXT    NOT NULL,
          id            TEXT    NOT NULL,
          name          TEXT    NOT NULL,
          target_schema TEXT    NOT NULL,
          target_id     TEXT    NOT NULL,
          revision      INTEGER,
          release       INTEGER,
          created_by    TEXT    NOT NULL,
          created_at    INTEGER NOT NULL,
          UNIQUE (namespace, schema, id, name, target_schema, target_id)
        ) STRICT`);
        sql.run(
          `INSERT INTO ${sql.table('links_next')} (row, namespace, schema, id, name, target_schema, target_id, revision, release, created_by, created_at)
           SELECT row, namespace, schema, id, name, target_schema, target_id, revision, release, created_by, created_at FROM ${sql.table('links')}`
        );
        sql.run(`DROP TABLE ${sql.table('links')}`);
        sql.run(`ALTER TABLE ${sql.table('links_next')} RENAME TO ${sql.table('links')}`);
        sql.run(`CREATE INDEX ${sql.table('links_by_target')} ON ${sql.table('links')} (namespace, schema, name, target_id, row)`);
      },
    },
  ],

  // A create's links: every name one the config gives, every required
  // link among them, a list for each many-valued link, of at least one
  // target when it is required, at most max and each target once; each
  // set with link's checks.
  initialize(context, params) {
    const links = context.config.links;
    const issues: SchemaIssue[] = [];
    for (const [name, value] of Object.entries(params)) {
      const entry = `/behaviors/${NAME}/${name}`;
      const link = own(links, name);
      if (link === undefined) {
        issues.push({ path: entry, message: `${context.schema} has no link ${name} (its links: ${Object.keys(links).join(', ')})` });
      } else if (!link.many) {
        if (Array.isArray(value)) {
          issues.push({ path: entry, message: `link ${name} holds one target, so a create gives one, not a list` });
        }
      } else if (!Array.isArray(value)) {
        issues.push({ path: entry, message: `link ${name} holds a list of targets, so a create gives a list` });
      } else if (value.length > (link.max as number)) {
        issues.push({ path: entry, message: `link ${name} holds at most ${link.max} targets, and the create gives ${value.length}` });
      } else if (link.required && value.length === 0) {
        issues.push({ path: entry, message: `link ${name} is required, so a create of ${context.schema} gives at least one target` });
      } else {
        const seen = new Set<string>();
        value.forEach((item, index) => {
          const { id } = targetOf(item);
          if (seen.has(id)) {
            issues.push({ path: `${entry}/${index}`, message: `link ${name} holds each target once, and the create gives ${link.schema} ${id} again` });
          }
          seen.add(id);
        });
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
      const link = links[name];
      if (Array.isArray(value)) {
        value.forEach((item, index) => setLink(context, name, link, targetOf(item), createRefusals(context, name, index, typeof item === 'string')));
      } else {
        setLink(context, name, link, targetOf(value), createRefusals(context, name, undefined, typeof value === 'string'));
      }
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
    // delete is clearing its links. A many-valued link names the target
    // it removes; a link that holds one may name it, which must be the one
    // it holds.
    unlink(context, params) {
      const name = params.name as string;
      const link = spec(context, 'unlink', name);
      const id = params.id as string | undefined;
      if (link.many && id === undefined) {
        throw new OperationParamsError(NAME, 'unlink', [{ path: '/id', message: `link ${name} holds a list of targets, so unlink names the one it removes` }]);
      }
      const rows = heldBy(context, name);
      if (rows.length === 0 && id === undefined) {
        throw new OperationParamsError(NAME, 'unlink', [{ path: '/name', message: `${context.schema} ${context.id} has no link ${name}` }]);
      }
      const veto = (reason: string, code: LinksVeto) => new BehaviorVetoError(NAME, 'unlink', context.schema, context.id, { reason, code });
      const row = id === undefined ? rows[0] : rows.find((one) => String(one.target_id) === id);
      if (row === undefined) {
        throw veto(`link ${name} does not hold ${link.schema} ${id}`, 'not_linked');
      }
      if (link.required && rows.length === 1) {
        throw veto(
          link.many
            ? `link ${name} is required and ${link.schema} ${id} is its last target: link another before unlinking it`
            : `link ${name} is required: it can be moved with link, not unlinked`,
          'required_link'
        );
      }
      context.sql.run(`DELETE FROM ${context.sql.table('links')} WHERE row = ?`, [Number(row.row)]);
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
      const pinOf = (row: Row) => own(view.config.links, String(row.name))?.pin;
      const current = latestOf(
        view,
        rows.flatMap((row) => {
          const pin = pinOf(row);
          return pin !== undefined && heldPin(row, pin) !== undefined ? [{ schema: String(row.target_schema), id: String(row.target_id), pin }] : [];
        })
      );
      const out: Record<string, LinkRecord | LinkRecord[]> = {};
      const lists = new Map<string, LinkRecord[]>();
      for (const row of rows) {
        const name = String(row.name);
        const pin = pinOf(row);
        const record = linkOf(row, pin === undefined ? undefined : current.get(`${String(row.target_schema)} ${String(row.target_id)} ${pin}`), pin);
        if (own(view.config.links, name)?.many === true) {
          const list = lists.get(name) ?? [];
          list.push(record);
          lists.set(name, list);
          out[name] = list;
        } else {
          out[name] = record;
        }
      }
      return out;
    },
  },

  // The delete of a target a required link points at is refused, whoever
  // the caller, and of a required many-valued link's last target. The
  // reason names the schema, not the instance, which the caller may not be
  // able to read.
  guardReference(view, reference, request) {
    const link = request.kind === 'delete' ? own(view.config.links, reference.key) : undefined;
    if (link?.required !== true || (link.many && heldBy(view, reference.key).length > 1)) {
      return undefined;
    }
    return {
      reason: `an instance of ${view.schema} links to it through its required link ${reference.key}${link.many ? ', as its last target' : ''}`,
      code: 'required_target',
    };
  },

  // A target's delete unlinks it, through unlink, as the caller: the one
  // target of a many-valued link.
  afterReferenceChange(context, reference, change) {
    if (change.kind === 'delete') {
      const many = own(context.config.links, reference.key)?.many === true;
      context.instances.invoke(context.schema, context.id, 'unlink', { name: reference.key, ...(many ? { id: reference.id } : {}) } as FrozenJSON);
    }
  },

  afterChange(context, change) {
    if (change.kind === 'delete') {
      context.sql.run(`DELETE FROM ${context.sql.table('links')} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
    }
  },
});
