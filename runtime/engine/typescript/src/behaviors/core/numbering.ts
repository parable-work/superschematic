/*
Numbering, a number a person reads on each instance (D56): an invoice's
INV-2026-0042, a ticket's T-17. The config's format prints it, a
template with a closed set of tokens: {seq}, or {seq:N} zero-padded to
N digits (1 to 10), exactly once, and {yyyy} and {yy}, the UTC year the
instance was created in, as often as the format wants them. A brace
that is not one of these tokens is refused, so a format holds no text a
reader could take for a token.

The count runs per namespace and schema; with scope: { link }, per
target of that link of the type's Links config, which must be required
and hold one target (a many-valued link is refused, its raw config read
from the type's configs); with reset: "yearly", per UTC year as well,
which needs the year in the format. Only {seq} varies in width, and it
is there once, so a label's length fixes where every token sits: two
numbers of one count print two labels, and with the year in the label a
yearly count's labels differ across years too. A label is unique within
its scope, and only there: two targets each have a T-1.

The number is set in afterChange of the create, inside its transaction,
so Links' initialize has run and every link a create gives is held, and
a create a later behavior refuses rolls the count back with it: a
refused create takes no number and leaves no gap. A delete leaves one.
The year is the instance's created_at, read with the scope link's target
in one read of the instance (instances.get, as the caller), so a number
assignNumber gives later still counts in the year the instance was made.
counters holds the last number of each count, by a scope key: the
target's id, the year, both with a / between (an id holds no /), or ''.
labels holds each numbered instance's label under its scope key, unique
there, so a defect that would print one label twice fails the write
(BehaviorError) rather than store it; a delete drops its row.

The instance's own columns hold its number and label, which the fields
seq and label read and a list filters on through an index of each
(where: { "Numbering.label": "INV-2026-0042" }). The number's column is
named number: the relation over the instances (sql.instances()) has a
seq of its own, the instance's event sequence, so a column named seq
would make it a BehaviorError for this behavior. Nothing changes a
number once set: no operation writes the columns but the one that sets
them when they are empty, and a create or an update writes own fields
only.

assignNumber numbers an instance made before its schema composed
Numbering, as its create would have, with an operation event; on an
instance numbered already it is unchanged() and returns its number. A
deployment backfills by invoking it in creation order. Moving a scope
link to another target keeps the number, counted in the count of the
target it was numbered under.

configChange: Numbering can be added to a schema with instances, which
have no number until assignNumber gives them one. While the schema has
instances, the format, the scope and the reset stay, since the instances
were numbered under them and a new count could print a label one of
them holds, and Numbering cannot be removed: added again, it would
count from 1. With none, anything may change, and afterConfigChange
drops the schema's counts and labels whenever the behavior comes, goes,
or counts in another way, so numbering starts again at 1.
*/

import { BehaviorError } from '../../errors.js';
import { LINKS_TARGETS, behaviorField } from '../fields.js';
import { BehaviorConfigError, defineBehavior, type ConfigTarget, type InstanceContext, type InstanceView } from '../behavior.js';
import declaration from './declarations/Numbering.behavior.json' with { type: 'json' };
import { numberingGuidance } from './guidance/numbering.js';

/** Numbering's config, parsed. */
export interface NumberingConfig {
  /** How a number prints: {seq} or {seq:N} once, {yyyy} and {yy} as often as wanted, and text. */
  readonly format: string;
  /** The link of the type's Links config whose target each count runs per; one count for the schema when absent. */
  readonly scope?: { readonly link: string };
  /** yearly starts each count again at 1 every UTC year. */
  readonly reset?: 'yearly';
}

/** An instance's number, as assignNumber returns it and the fields hold it. */
export interface NumberingNumber {
  readonly seq: number;
  readonly label: string;
}

/** One piece of a format: text, the number padded to a width (0 for none), or the year in 4 or 2 digits. */
type FormatPart = { readonly text: string } | { readonly seq: number } | { readonly year: 4 | 2 };

const NAME = 'Numbering';

/** The widest {seq:N} a format may pad to. */
const MAX_SEQ_WIDTH = 10;

const TOKENS = '{seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}';

// parseFormat splits a format into its parts, refusing a brace outside
// a token, a token it does not know, and a {seq} that is not there once.
function parseFormat(format: string): FormatPart[] {
  const parts: FormatPart[] = [];
  let seqs = 0;
  let last = 0;
  for (const match of format.matchAll(/\{([^{}]*)\}|[{}]/g)) {
    const at = match.index;
    if (at > last) {
      parts.push({ text: format.slice(last, at) });
    }
    last = at + match[0].length;
    const token = match[1];
    if (token === undefined) {
      throw new BehaviorConfigError(`format "${format}" has a "${match[0]}" that is not part of a token; its tokens are ${TOKENS}`);
    }
    if (token === 'yyyy' || token === 'yy') {
      parts.push({ year: token === 'yyyy' ? 4 : 2 });
      continue;
    }
    const seq = /^seq(?::([1-9][0-9]?))?$/.exec(token);
    const width = seq?.[1] === undefined ? 0 : Number(seq[1]);
    if (seq === null || width > MAX_SEQ_WIDTH) {
      throw new BehaviorConfigError(`format "${format}" has {${token}}, which is not one of its tokens: ${TOKENS}`);
    }
    seqs += 1;
    parts.push({ seq: width });
  }
  if (last < format.length) {
    parts.push({ text: format.slice(last) });
  }
  if (seqs !== 1) {
    throw new BehaviorConfigError(
      seqs === 0
        ? `format "${format}" has no {seq}: it prints the number with {seq}, or {seq:N} padded to N digits, once`
        : `format "${format}" has the number ${seqs} times: it prints it with {seq} or {seq:N} once, so two numbers never print one label`
    );
  }
  return parts;
}

// numberLabel prints a number by a format the config accepted: the
// number padded to its token's width, a longer one in full, and the year
// in 4 or 2 digits.
function numberLabel(format: string, seq: number, year: number): string {
  return parseFormat(format)
    .map((part) => {
      if ('text' in part) {
        return part.text;
      }
      if ('seq' in part) {
        return String(seq).padStart(part.seq, '0');
      }
      return String(part.year === 4 ? year : year % 100).padStart(part.year, '0');
    })
    .join('');
}

// checkScope holds the scope's link to the type's Links config: one of
// its links, required, and holding one target. It reads the raw config,
// so a link marked many is refused before Links reads that key.
function checkScope(target: ConfigTarget, link: string): void {
  const at = `scope.link ${link}`;
  if (!target.behaviors.includes('Links')) {
    throw new BehaviorConfigError(`${at} is a link of Links, which the type does not list`);
  }
  const links = (target.configs.Links as { links?: Readonly<Record<string, { required?: unknown; many?: unknown }>> } | undefined)?.links;
  if (links === undefined || links === null || typeof links !== 'object') {
    // A Links config of the wrong shape is Links' to refuse.
    return;
  }
  const spec = Object.prototype.hasOwnProperty.call(links, link) ? links[link] : undefined;
  if (spec === undefined) {
    throw new BehaviorConfigError(`${at} is not a link of the type's Links config (its links: ${Object.keys(links).join(', ')})`);
  }
  if (spec.many === true) {
    throw new BehaviorConfigError(`${at} holds many targets (many: true), so an instance has no one target to count by; name a link that holds one`);
  }
  if (spec.required !== true) {
    throw new BehaviorConfigError(`${at} is not required, so an instance may hold no target to count by; make it required`);
  }
}

// same reports whether two configs count and print alike.
function same(before: NumberingConfig, after: NumberingConfig): boolean {
  return before.format === after.format && before.scope?.link === after.scope?.link && before.reset === after.reset;
}

// held is the instance's number, or undefined before it has one.
function held(view: InstanceView<NumberingConfig>): NumberingNumber | undefined {
  const { number, label } = view.columns.get();
  return number === null || number === undefined ? undefined : { seq: Number(number), label: String(label) };
}

/**
 * assign numbers the instance: the next number of its count, read from
 * the instance's created_at and its scope link's target, printed by the
 * format. action names the write, the create or assignNumber.
 */
function assign(context: InstanceContext<NumberingConfig>, action: string): NumberingNumber {
  const { config, namespace, schema, id } = context;
  const link = config.scope?.link;
  const record = context.instances.get(schema, id, { fields: link === undefined ? [] : [LINKS_TARGETS] });
  const createdAt = record?.createdAt ?? context.now;
  const year = new Date(createdAt).getUTCFullYear();
  const keys: string[] = [];
  if (link !== undefined) {
    // A required link that holds one target is always held: Links gives
    // it at create, moves it and never unlinks it, and refuses its
    // target's delete.
    const target = (behaviorField(record, 'Links', 'targets') as Readonly<Record<string, { readonly id?: unknown }>> | undefined)?.[link];
    if (typeof target?.id !== 'string') {
      throw new BehaviorError(NAME, `${action}: ${schema} ${id} holds no target of its required link ${link}, which its number is counted by`);
    }
    keys.push(target.id);
  }
  if (config.reset === 'yearly') {
    keys.push(String(year));
  }
  const scopeKey = keys.join('/');
  const counters = context.sql.table('counters');
  context.sql.run(
    `INSERT INTO ${counters} (namespace, schema, scope_key, last) VALUES (?, ?, ?, 1)
     ON CONFLICT (namespace, schema, scope_key) DO UPDATE SET last = last + 1`,
    [namespace, schema, scopeKey]
  );
  const seq = Number(context.sql.get(`SELECT last FROM ${counters} WHERE namespace = ? AND schema = ? AND scope_key = ?`, [namespace, schema, scopeKey])?.last);
  const label = numberLabel(config.format, seq, year);
  const stored = context.sql.run(
    `INSERT INTO ${context.sql.table('labels')} (namespace, schema, id, scope_key, label) VALUES (?, ?, ?, ?, ?)
     ON CONFLICT DO NOTHING`,
    [namespace, schema, id, scopeKey, label]
  );
  if (stored.changes === 0) {
    throw new BehaviorError(NAME, `${schema} ${id} would be numbered ${label}, which another instance of its count holds`);
  }
  context.columns.set({ number: seq, label });
  return { seq, label };
}

export const numbering = defineBehavior<NumberingConfig>({
  declaration,

  guidance: numberingGuidance,

  // The configSchema holds the shape; this holds the format to its
  // tokens and the scope to the type's Links.
  parseConfig(json, target) {
    const raw = json as { format: string; scope?: { link: string }; reset?: 'yearly' };
    const parts = parseFormat(raw.format);
    if (raw.reset === 'yearly' && !parts.some((part) => 'year' in part)) {
      throw new BehaviorConfigError(
        `reset yearly counts each year from 1, so format "${raw.format}" prints the year, {yyyy} or {yy}, or two years would print one label`
      );
    }
    if (raw.scope !== undefined) {
      checkScope(target, raw.scope.link);
    }
    return {
      format: raw.format,
      ...(raw.scope === undefined ? {} : { scope: { link: raw.scope.link } }),
      ...(raw.reset === undefined ? {} : { reset: raw.reset }),
    };
  },

  configChange(before, after, change) {
    if (before === undefined) {
      return undefined;
    }
    if (after === undefined) {
      return 'the instances would lose their numbers, and Numbering added again would count from 1, printing labels they hold';
    }
    // No instance holds a number a new count could print again.
    if (!change.instances || same(before, after)) {
      return undefined;
    }
    const changed = [
      ...(before.format === after.format ? [] : [`the format from "${before.format}" to "${after.format}"`]),
      ...(before.scope?.link === after.scope?.link
        ? []
        : [`the scope from ${before.scope === undefined ? 'none' : `link ${before.scope.link}`} to ${after.scope === undefined ? 'none' : `link ${after.scope.link}`}`]),
      ...(before.reset === after.reset ? [] : [`the reset from ${before.reset ?? 'none'} to ${after.reset ?? 'none'}`]),
    ];
    return `the instances were numbered under the format, the scope and the reset, which stay while there are instances; the new config changes ${changed.join(', ')}`;
  },

  // A count that starts again starts at 1: Numbering came, went or counts
  // another way, which configChange allows only on a schema with no
  // instance to hold a number (an added one's instances have none).
  afterConfigChange(context) {
    if (context.before !== undefined && context.config !== undefined && same(context.before, context.config)) {
      return;
    }
    for (const table of ['counters', 'labels']) {
      context.sql.run(`DELETE FROM ${context.sql.table(table)} WHERE namespace = ? AND schema = ?`, [context.namespace, context.schema]);
    }
  },

  // The indexes let a list that filters on a number or a label read only
  // the instances that hold it, in creation order.
  migrations: [
    {
      version: 1,
      name: 'numbers',
      columns: { number: { type: 'integer' }, label: { type: 'text' } },
      indexes: { number: ['number'], label: ['label'] },
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('counters')} (
          namespace TEXT    NOT NULL,
          schema    TEXT    NOT NULL,
          scope_key TEXT    NOT NULL,
          last      INTEGER NOT NULL,
          PRIMARY KEY (namespace, schema, scope_key)
        ) STRICT`);
        sql.run(`CREATE TABLE ${sql.table('labels')} (
          namespace TEXT NOT NULL,
          schema    TEXT NOT NULL,
          id        TEXT NOT NULL,
          scope_key TEXT NOT NULL,
          label     TEXT NOT NULL,
          PRIMARY KEY (namespace, schema, id),
          UNIQUE (namespace, schema, scope_key, label)
        ) STRICT`);
      },
    },
  ],

  filters: {
    seq: { column: 'number', type: 'integer' },
    label: { column: 'label', type: 'string' },
  },

  operations: {
    assignNumber(context) {
      const number = held(context);
      if (number !== undefined) {
        context.unchanged();
        return number;
      }
      return assign(context, 'assignNumber');
    },
  },

  fields: {
    seq: (view) => held(view)?.seq,
    label: (view) => held(view)?.label,
  },

  afterChange(context, change) {
    if (change.kind === 'create') {
      assign(context, 'create');
    } else if (change.kind === 'delete') {
      context.sql.run(`DELETE FROM ${context.sql.table('labels')} WHERE namespace = ? AND schema = ? AND id = ?`, [
        context.namespace,
        context.schema,
        context.id,
      ]);
    }
  },
});
