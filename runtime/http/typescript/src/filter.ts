import { parseGenericDecimal, parseTemporalDate, validateGenericDecimal, validateTemporalDate } from 'superscalar/scalars';
import type { ValidationError } from 'superscalar/validation';
import { decodeQueryListItem, type ParamSpec } from './params.js';
import { badRequest } from './problem.js';

/*
The filters of a list operation (FilterBy, D106). On the wire a filter is
the bracket form: filter[field]=value is equality, and
filter[field][op]=value names an operator. The generated router carries an
operation's FilterSpec, the fields a caller may filter by, each with its
operators and the ParamSpec of one of its values, reads the request's
filters with readFilter, and hands the implementation the typed filter, a
member per field the request filters by (EqualsFilter, OrderedFilter,
TextFilter), under the operation's filter argument. Members are ANDed. ne
and notIn negate eq and in (D112): each keeps the items the other leaves
out, an item whose field is null among them.
readFilters is the reader alone, the filters as the query sent them; the Go
runtime's filterparse package and the Rust runtime's filter module read the
same, held to runtime/http/testdata/filter_parity.json. Nothing here
imports node:, so it runs on Workers too.
*/

/** The query parameter every filter travels under. */
export const FILTER_PARAM = 'filter';

/** The most filter parameters a request may hold. */
export const MAX_FILTERS = 50;

/** The most values an in may list. */
export const MAX_FILTER_IN_VALUES = 100;

/** The comparison a filter makes, as the wire names it. */
export type FilterOperator = 'eq' | 'ne' | 'in' | 'notIn' | 'gt' | 'gte' | 'lt' | 'lte' | 'contains' | 'isNull';

/** Every operator, in the order a field lists its own and readFilters answers them. */
export const FILTER_OPERATORS: readonly FilterOperator[] = ['eq', 'ne', 'in', 'notIn', 'gt', 'gte', 'lt', 'lte', 'contains', 'isNull'];

/**
 * What a field a filter compares holds, as the loader classifies its
 * column (ir.FilterKinds).
 */
export type FilterKind = 'uuid' | 'string' | 'enum' | 'int' | 'float' | 'decimal' | 'bool' | 'datetime' | 'date';

/**
 * One field of a FilterSpec: its name, the item's field's JSON name, its
 * kind, the operators it takes, and one value of it as a query parameter
 * named filter[<field>], of the kind a query parameter of the field's type
 * has, which readFilter decodes each value with. A date's and a decimal's
 * value, which a query parameter takes as text, readFilter reads by the
 * field's kind, with the scalar library, as the Go server's types do.
 */
export interface FilterFieldSpec {
  readonly name: string;
  readonly kind?: FilterKind;
  readonly operators: readonly FilterOperator[];
  readonly value: ParamSpec;
}

/** An operation's FilterBy as the router carries it: the fields a caller may filter by. */
export interface FilterSpec {
  readonly fields: readonly FilterFieldSpec[];
}

/** What readFilters reads of a FilterSpec: its fields' names and operators. */
export interface FilterFields {
  readonly fields: readonly Pick<FilterFieldSpec, 'name' | 'operators'>[];
}

/**
 * One filter a request holds: the field, the operator and its values as
 * the query sent them. Every operator but in and notIn holds one value;
 * those two hold their list, split at commas, each value trimmed. isNull's
 * value is true or false.
 */
export interface QueryFilter {
  readonly field: string;
  readonly operator: FilterOperator;
  readonly values: string[];
}

/*
A field's filter as an implementation takes it (D106): a member per
operator the request sent, absent for one it did not. Which type a field's
is follows its operators: EqualsFilter for eq, ne, in and notIn alone (a
UUID, an enum, a boolean), OrderedFilter for a number, a date and a time,
which add gt, gte, lt and lte, and TextFilter for text, which adds
contains. ne and notIn keep the items eq and in leave out (D112), an item
whose field is null among them. isNull is set only on a field that may be
null: true keeps the items whose field is null, false the others.
*/

/** The filter of a field compared for equality alone. */
export interface EqualsFilter<T> {
  readonly eq?: T;
  readonly ne?: T;
  readonly in?: readonly T[];
  readonly notIn?: readonly T[];
  readonly isNull?: boolean;
}

/** The filter of a field whose values have an order: a number, a date or a time. */
export interface OrderedFilter<T> {
  readonly eq?: T;
  readonly ne?: T;
  readonly in?: readonly T[];
  readonly notIn?: readonly T[];
  readonly gt?: T;
  readonly gte?: T;
  readonly lt?: T;
  readonly lte?: T;
  readonly isNull?: boolean;
}

/** The filter of a field of text. contains is the text a value holds, in any case. */
export interface TextFilter<T> {
  readonly eq?: T;
  readonly ne?: T;
  readonly in?: readonly T[];
  readonly notIn?: readonly T[];
  readonly contains?: string;
  readonly isNull?: boolean;
}

/** A field's filter of any shape, as readFilter answers it: the generated router narrows it to the field's own. */
export interface FieldFilter {
  readonly eq?: unknown;
  readonly ne?: unknown;
  readonly in?: readonly unknown[];
  readonly notIn?: readonly unknown[];
  readonly gt?: unknown;
  readonly gte?: unknown;
  readonly lt?: unknown;
  readonly lte?: unknown;
  readonly contains?: string;
  readonly isNull?: boolean;
}

/** A request's filter, typed: a member per field the request filters by, none for a field it does not. */
export type RequestFilter = Readonly<Record<string, FieldFilter>>;

/** The 400 of a filter parameter, worded as page.ts words a page's, with the broken rule when there is one, or the errors a value's type gave. */
function refuse(parameter: string, reason: string, validator?: string | readonly ValidationError[] | null): never {
  const errors = typeof validator === 'string' ? [{ validator, message: reason }] : validator;
  throw badRequest(`Invalid query parameter ${parameter}: ${reason}`, {
    details: {
      location: 'query',
      parameter,
      reason,
      ...(errors && errors.length > 0 ? { errors } : {}),
    },
  });
}

/** The parameter a field's refusal names: filter[field]. */
function at(field: string): string {
  return `${FILTER_PARAM}[${field}]`;
}

/** Whether a code unit is half of a surrogate pair. */
function isSurrogate(unit: number): boolean {
  return unit >= 0xd800 && unit <= 0xdfff;
}

/**
 * Orders two keys by their UTF-8 bytes, the order the Go and Rust readers
 * sort theirs in. It is the order of code points, which JavaScript's own,
 * of UTF-16 code units, leaves at a surrogate pair: a character past
 * U+FFFF sorts after U+E000 to U+FFFF, whose code units are greater than a
 * surrogate's. A query's keys hold no lone surrogate.
 */
function compareUtf8(a: string, b: string): number {
  const length = Math.min(a.length, b.length);
  for (let i = 0; i < length; i++) {
    const x = a.charCodeAt(i);
    const y = b.charCodeAt(i);
    if (x === y) continue;
    if (isSurrogate(x) !== isSurrogate(y)) return isSurrogate(x) ? 1 : -1;
    return x - y;
  }
  return a.length - b.length;
}

/**
 * Unicode's White_Space, which Go's strings.TrimSpace and Rust's str::trim
 * trim an in's values of. String.prototype.trim differs at two characters:
 * it keeps U+0085 and trims U+FEFF.
 */
const SPACE = '[\\t\\n\\v\\f\\r \\u0085\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000]';
const OUTER_SPACE = new RegExp(`^${SPACE}+|${SPACE}+$`, 'gu');

/**
 * Reads a filter parameter's key, filter[field] or filter[field][op]: the
 * field, and the operator, absent when the key names none. undefined for
 * any other key: filter alone, an empty field or operator, a third
 * bracket, or text outside the brackets. A field and an operator hold no
 * bracket.
 */
function bracketKey(key: string): { field: string; operator?: string } | undefined {
  const chain = key.slice(FILTER_PARAM.length);
  if (!chain.startsWith('[') || !chain.endsWith(']')) return undefined;
  const segments = chain.slice(1, -1).split('][');
  if (segments.length > 2) return undefined;
  if (segments.some(segment => segment === '' || segment.includes('[') || segment.includes(']'))) return undefined;
  return segments.length === 2 ? { field: segments[0]!, operator: segments[1]! } : { field: segments[0]! };
}

/**
 * Reads a request's filters from a query, as the query sent them: the
 * reader every runtime shares. A key that is filter, or starts with
 * filter[, is a filter parameter; every other key is left alone. The keys
 * are read in ascending order of their UTF-8 bytes, so a request with two
 * faults is refused for the same one whatever order it sent them in and
 * whichever runtime reads it, and the filters are answered in the spec's
 * field order and, within a field, in the order of FILTER_OPERATORS. A
 * refusal throws the 400 HttpProblem, in this order:
 *
 * - more than MAX_FILTERS filter parameters, counting each occurrence, at
 *   filter;
 * - a key that is not filter[field] or filter[field][operator], at the
 *   key;
 * - a field the spec does not name, an operator the field does not take,
 *   and a field's operator sent twice (filter[f] and filter[f][eq] are
 *   one), each at filter[field];
 * - a value the operator does not take, at filter[field]: isNull's is true
 *   or false, in's and notIn's is 1 to MAX_FILTER_IN_VALUES values, and
 *   contains needs text.
 *
 * A value's own type is not checked here: readFilter decodes it.
 */
export function readFilters(query: URLSearchParams, spec: FilterFields): QueryFilter[] {
  const sent = new Map<string, string[]>();
  let total = 0;
  query.forEach((value, key) => {
    if (key !== FILTER_PARAM && !key.startsWith(`${FILTER_PARAM}[`)) return;
    const values = sent.get(key);
    if (values) values.push(value);
    else sent.set(key, [value]);
    total++;
  });
  if (total > MAX_FILTERS) refuse(FILTER_PARAM, `must hold at most ${MAX_FILTERS} filters`, 'max');

  // Each filter found, at its field's place in the spec and its operator's
  // in FILTER_OPERATORS.
  const found: { fieldIndex: number; operatorIndex: number; filter: QueryFilter }[] = [];
  for (const key of [...sent.keys()].sort(compareUtf8)) {
    const parsed = bracketKey(key);
    if (parsed === undefined) refuse(key, 'expected filter[field] or filter[field][operator]');
    const { field } = parsed;
    const fieldIndex = spec.fields.findIndex(candidate => candidate.name === field);
    if (fieldIndex < 0) {
      const names = spec.fields.map(candidate => candidate.name);
      refuse(
        at(field),
        `is not a field this operation filters by${names.length > 0 ? `; expected one of ${names.join(', ')}` : ''}`
      );
    }
    const fieldSpec = spec.fields[fieldIndex]!;
    const operator = (parsed.operator ?? 'eq') as FilterOperator;
    const operatorIndex = FILTER_OPERATORS.indexOf(operator);
    if (operatorIndex < 0 || !fieldSpec.operators.includes(operator)) {
      refuse(at(field), `takes no operator ${operator}; expected one of ${fieldSpec.operators.join(', ')}`);
    }
    const values = sent.get(key)!;
    if (values.length !== 1 || found.some(other => other.fieldIndex === fieldIndex && other.operatorIndex === operatorIndex)) {
      refuse(at(field), `holds ${operator} more than once`);
    }
    const value = values[0]!;
    let read = [value];
    switch (operator) {
      case 'isNull':
        if (value !== 'true' && value !== 'false') refuse(at(field), 'isNull expects true or false');
        break;
      case 'in':
      case 'notIn':
        read = value
          .split(',')
          .map(part => part.replace(OUTER_SPACE, ''))
          .filter(part => part !== '');
        if (read.length === 0) refuse(at(field), `${operator} names no value`);
        if (read.length > MAX_FILTER_IN_VALUES) refuse(at(field), `${operator} must hold at most ${MAX_FILTER_IN_VALUES} values`, 'max');
        break;
      case 'contains':
        if (value === '') refuse(at(field), 'contains needs text');
        break;
    }
    found.push({ fieldIndex, operatorIndex, filter: { field, operator, values: read } });
  }
  return found
    .sort((a, b) => a.fieldIndex - b.fieldIndex || a.operatorIndex - b.operatorIndex)
    .map(entry => entry.filter);
}

/**
 * One value of a field, read as its type: a date and a decimal by the
 * scalar library, as its canonical text (2026-3-1 is 2026-03-01, and 0.30
 * is 0.3), and any other as an item of a query list of the field's type
 * is, by the field's value spec. A value its type refuses throws the 400
 * at filter[field].
 */
function decodeValue(field: FilterFieldSpec, text: string): unknown {
  switch (field.kind) {
    case 'date': {
      const parsed = parseTemporalDate(text.trim());
      if (parsed === null) refuse(field.value.name, 'expected a date', validateTemporalDate(text.trim())[1]);
      return parsed;
    }
    case 'decimal': {
      const parsed = parseGenericDecimal(text.trim());
      if (parsed === null) refuse(field.value.name, 'expected a decimal number', validateGenericDecimal(text.trim())[1]);
      return parsed;
    }
    default:
      return decodeQueryListItem('query', field.value, undefined, text);
  }
}

/**
 * Reads a request's filter from a query, typed, the router's whole read:
 * the request's filters (readFilters), then each value decoded as its
 * field's type (decodeValue), so a UUID is its canonical text, a timestamp
 * a Date, a number a number, an enum one of its values, and a date and a
 * decimal their canonical text; contains is its text and isNull a boolean.
 * The result holds a member per field the request filters by, in the
 * spec's order, each with a member per operator it sent, and is {} for a
 * request that holds no filter. A refusal throws the 400 HttpProblem at
 * filter[field]: the reader's first, then the first value its type
 * refuses, in the spec's field order and the operators'.
 */
export function readFilter(query: URLSearchParams, spec: FilterSpec): RequestFilter {
  const members = new Map<string, Record<string, unknown>>();
  for (const filter of readFilters(query, spec)) {
    const field = spec.fields.find(candidate => candidate.name === filter.field)!;
    const decode = (text: string): unknown => decodeValue(field, text);
    let member = members.get(filter.field);
    if (member === undefined) members.set(filter.field, (member = {}));
    const first = filter.values[0]!;
    switch (filter.operator) {
      case 'in':
      case 'notIn':
        member[filter.operator] = filter.values.map(decode);
        break;
      case 'contains':
        member.contains = first;
        break;
      case 'isNull':
        member.isNull = first === 'true';
        break;
      default:
        member[filter.operator] = decode(first);
    }
  }
  // A field's name is the spec's, and stays an own key whatever it is.
  return Object.fromEntries(members);
}
