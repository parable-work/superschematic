/*
The validator of one schema version. The schema runtime checks each
field's value (presence, JSON type, scalar rules, enum membership, list
rules), built with parseSchemaIR from the loaded document, a catalog
scalar the document declares read as the catalog's (runtimeDocument); a list field
that holds no list and an object-typed field or list element that holds
no object are its `type` issues. The engine checks two things the
runtime does not, because the compatibility rule depends on what a
stored instance can hold:

- the value is JSON: plain objects and arrays, strings, finite numbers,
  booleans and null. A member whose value is undefined counts as absent;
- an object holds no key its type does not declare, at every level, so a
  new optional field never meets a value an older version let through. A
  field a behavior on the instance type adds is declared too, but it is
  the behavior's to change: an instance written with one is refused with
  the `readOnly` rule rather than `unknown`.

validateType holds a value to another type of the document by the same
rules, for a behavior that types a field the document leaves open (a
Generic.JSON field Variants types by another field's value).

normalize is what a write stores (D16, amended: a write stores what the
schema's parse makes of it): the schema runtime's strict parse of the
instance type, which puts each scalar value in the canonical form its
scalar's normalize and parse steps give it, at every depth (an email
lowercased, a color as #RRGGBBAA, a UUID in base62, a JSON object
scalar's JSON text read into the object), and fills each absent field's
default. A create fills defaults; a merge patch, whose null removes a
member, fills none. What the parse cannot read, a value of the wrong type,
a key the type does not declare, a field a behavior adds, is left as it
was for validate to report; a scalar parser's own refusal is returned for
the write to report when validate finds nothing.
*/

import { Runtime, parseSchemaIR } from '@superschematic/schema-runtime';
import type { Document, FieldDef, TypeDef } from '@superschematic/schema-ir/schema-file';

import type { ValidationIssue } from '../errors.js';
import { arrayDepth, jsonKey, refKind, runtimeDocument, type SchemaModel } from './document.js';

type RuntimeErrors = ReturnType<Runtime['validateType']>;

export class SchemaValidator {
  private readonly runtime: Runtime;
  // The runtime's object types; every other type of the document is an input.
  private readonly objectTypes: ReadonlySet<string>;

  /**
   * behaviorFields maps each field the instance type's behaviors add to
   * the behavior that adds it.
   */
  constructor(
    readonly model: SchemaModel,
    private readonly behaviorFields: ReadonlyMap<string, string> = new Map()
  ) {
    const schema = parseSchemaIR(runtimeDocument(model.document));
    this.runtime = new Runtime(schema);
    this.objectTypes = new Set(Object.keys(schema.types ?? {}));
  }

  // errorsOf runs the schema runtime's checks of a type's fields.
  private errorsOf(typeName: string, value: Record<string, unknown>): RuntimeErrors {
    return this.objectTypes.has(typeName) ? this.runtime.validateType(typeName, value) : this.runtime.validateInput(typeName, value);
  }

  /** validate returns every issue with value as an instance; empty means valid. */
  validate(value: unknown): ValidationIssue[] {
    const issues: ValidationIssue[] = [];
    if (!isPlainObject(value)) {
      issues.push({ path: '', rule: 'type', message: 'an instance is a JSON object' });
      return issues;
    }
    jsonIssues(value, '', issues);
    if (issues.length > 0) {
      return issues;
    }
    for (const key of Object.keys(value)) {
      const behavior = this.behaviorFields.get(key);
      if (behavior !== undefined && value[key] !== undefined) {
        issues.push(readOnlyIssue(key, behavior));
      }
    }
    undeclaredKeys(this.model.document, this.model.instanceType, value, '', issues, this.behaviorFields);
    flatten(this.errorsOf(this.model.instanceType, value), '', issues);
    return issues;
  }

  /**
   * normalize returns value as the version stores it, and the issues a
   * scalar's parser found that validate does not report: `create` fills
   * each absent field's default, at every depth; `patch` reads a merge
   * patch and fills none. A value that is not a JSON object, or holds
   * anything that is not JSON, comes back as it is.
   */
  normalize(value: unknown, mode: NormalizeMode): Normalized {
    if (!isPlainObject(value)) {
      return { value, issues: [] };
    }
    const notJSON: ValidationIssue[] = [];
    jsonIssues(value, '', notJSON);
    if (notJSON.length > 0) {
      return { value, issues: [] };
    }
    const type = this.model.instanceType;
    const parsed = this.objectTypes.has(type)
      ? this.runtime.parseType(type, value, { strict: true })
      : this.runtime.parseInput(type, value, { strict: true });
    const issues: ValidationIssue[] = [];
    flatten(parsed.errors, '', issues);
    return { value: overlay(value, parsed.data, mode === 'create'), issues: issues.filter((issue) => !VALIDATE_REPORTS.has(issue.rule)) };
  }

  /**
   * normalizeField returns one value of a top-level own field as a write
   * would store it, for a lookup's key and a list's where, which compare
   * with what is stored; any other key's value comes back as it is.
   */
  normalizeField(key: string, value: unknown): unknown {
    const normalized = this.normalize({ [key]: value }, 'patch').value as Record<string, unknown>;
    return Object.prototype.hasOwnProperty.call(normalized, key) ? normalized[key] : value;
  }

  /**
   * validateType returns every issue with value as a value of one of the
   * document's types besides the instance type, by the rules a field of
   * that type is held to: a JSON object, each field's value, and no key
   * the type does not declare, at any depth. Each issue's path is under
   * path. A name that is no such type is a TypeError.
   */
  validateType(typeName: string, value: unknown, path: string): ValidationIssue[] {
    if (typeName === this.model.instanceType || refKind(this.model.document, typeName) !== 'type') {
      throw new TypeError(`${typeName} is not a type of schema ${this.model.name} besides its instance type`);
    }
    const issues: ValidationIssue[] = [];
    if (!isPlainObject(value)) {
      issues.push({ path, rule: 'type', message: `a ${typeName} is a JSON object` });
      return issues;
    }
    jsonIssues(value, path, issues);
    if (issues.length > 0) {
      return issues;
    }
    undeclaredKeys(this.model.document, typeName, value, path, issues);
    flatten(this.errorsOf(typeName, value), path, issues);
    return issues;
  }
}

/** How normalize reads a write: a create's instance, which takes defaults, or a merge patch, which takes none. */
export type NormalizeMode = 'create' | 'patch';

/** A write as the version stores it, and what a scalar's parser refused in it. */
export interface Normalized {
  readonly value: unknown;
  /** A scalar parser's refusals (rule `parse`), which the write reports when validate reports nothing. */
  readonly issues: ValidationIssue[];
}

// The parse's findings validate reports itself, under its own rules: a
// value of the wrong type, a key no type declares, and a default whose
// text the field's type cannot read, which leaves the field absent.
const VALIDATE_REPORTS: ReadonlySet<string> = new Set(['type', 'unknown_field', 'default']);

// overlay is given with each value the parse read in its place: the
// parse's value for each member it read, at every depth, and the given one
// for a member it did not (an undeclared key, a behavior's field), so
// validate still sees it; with defaults, also the members the parse filled.
function overlay(given: unknown, parsed: unknown, defaults: boolean): unknown {
  if (isPlainObject(given) && isPlainObject(parsed)) {
    const out: Record<string, unknown> = {};
    for (const [key, member] of Object.entries(given)) {
      if (member !== undefined) {
        setMember(out, key, Object.prototype.hasOwnProperty.call(parsed, key) ? overlay(member, parsed[key], defaults) : member);
      }
    }
    if (defaults) {
      for (const [key, member] of Object.entries(parsed)) {
        if (member !== undefined && (!Object.prototype.hasOwnProperty.call(given, key) || given[key] === undefined)) {
          setMember(out, key, member);
        }
      }
    }
    return out;
  }
  if (Array.isArray(given) && Array.isArray(parsed) && given.length === parsed.length) {
    return given.map((element, index) => overlay(element, parsed[index], defaults));
  }
  return parsed === undefined ? given : parsed;
}

// setMember sets an own member, so one named __proto__ stays a member.
function setMember(target: Record<string, unknown>, key: string, value: unknown): void {
  Object.defineProperty(target, key, { value, enumerable: true, writable: true, configurable: true });
}

/** readOnlyIssue refuses a value for a field a behavior adds. */
export function readOnlyIssue(field: string, behavior: string): ValidationIssue {
  return { path: field, rule: 'readOnly', message: `${field} is a field of behavior ${behavior}, which only its operations change` };
}

function jsonIssues(value: unknown, path: string, issues: ValidationIssue[]): void {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') {
    return;
  }
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) {
      issues.push({ path, rule: 'type', message: `${String(value)} is not a JSON number` });
    }
    return;
  }
  if (Array.isArray(value)) {
    value.forEach((element, index) => {
      if (element === undefined) {
        issues.push({ path: `${path}[${index}]`, rule: 'type', message: 'undefined is not a JSON value' });
      } else {
        jsonIssues(element, `${path}[${index}]`, issues);
      }
    });
    return;
  }
  if (isPlainObject(value)) {
    for (const [key, member] of Object.entries(value)) {
      if (member !== undefined) {
        jsonIssues(member, join(path, key), issues);
      }
    }
    return;
  }
  const kind = typeof value === 'object' ? (value.constructor?.name ?? 'object') : typeof value;
  issues.push({ path, rule: 'type', message: `a ${kind} is not a JSON value` });
}

// undeclaredKeys reports each key of value that typeName does not declare,
// then walks into the values of its object-typed fields.
function undeclaredKeys(
  document: Document,
  typeName: string,
  value: Record<string, unknown>,
  path: string,
  issues: ValidationIssue[],
  behaviorFields: ReadonlyMap<string, string> = new Map()
): void {
  const type = (document.types ?? {})[typeName] as TypeDef;
  const fields = new Map<string, FieldDef>((type.fields ?? []).map((field) => [jsonKey(field), field]));
  for (const [key, member] of Object.entries(value)) {
    if (member !== undefined && !fields.has(key) && !behaviorFields.has(key)) {
      issues.push({ path: join(path, key), rule: 'unknown', message: `${typeName} has no field ${key}` });
    }
  }
  for (const [key, field] of fields) {
    if (refKind(document, field.typeRef.name) === 'type') {
      nestedKeys(document, field.typeRef.name, value[key], arrayDepth(field.typeRef), join(path, key), issues);
    }
  }
}

// nestedKeys walks a value of an object type down its list depth to each
// object it holds. It skips a value of the wrong shape (a list that is no
// list, an object that is no object, a null element), which the runtime
// reports.
function nestedKeys(
  document: Document,
  typeName: string,
  value: unknown,
  depth: number,
  path: string,
  issues: ValidationIssue[]
): void {
  if (depth === 0) {
    if (isPlainObject(value)) {
      undeclaredKeys(document, typeName, value, path, issues);
    }
    return;
  }
  if (Array.isArray(value)) {
    value.forEach((element, index) => nestedKeys(document, typeName, element, depth - 1, `${path}[${index}]`, issues));
  }
}

// The runtime nests a type's errors under the field's key; a list
// element's key already carries its index (`lines[2]`).
function flatten(errors: RuntimeErrors, path: string, issues: ValidationIssue[]): void {
  for (const [key, entry] of Object.entries(errors)) {
    const at = key === '' ? path : join(path, key);
    if (Array.isArray(entry)) {
      for (const error of entry) {
        issues.push({ path: at, rule: error.validator, message: error.message });
      }
    } else {
      flatten(entry, at, issues);
    }
  }
}

function join(path: string, key: string): string {
  return path === '' ? key : `${path}.${key}`;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}
