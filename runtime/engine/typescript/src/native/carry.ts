/*
Values JSON cannot carry (D58): a NaN, an Infinity, a Date, a Map, an
undefined list item. JSON.stringify would turn them into something else
(null, a date's text, {}), which the Rust engine would then judge as that;
the TypeScript engine refuses each where it meets it (jsonCopy, the
validator's JSON check), in its own words. carry marks each with those
words instead, `{ "$superschematic:notJSON": "NaN is not a JSON number" }`,
which the Rust engine refuses at the same points with the same message
(runtime/engine/rust/engine/src/json.rs, NOT_JSON): an operation's or a
create's parameters, and a value the validator checks.
*/

import { isPlainObject, setMember } from '../instances/patch.js';

/** The member of the object that stands for a value JSON cannot carry. */
export const NOT_JSON = '$superschematic:notJSON';

/** carry is a value as the addon takes it, each part JSON cannot carry marked with the TypeScript engine's words for it. */
export function carry(value: unknown): unknown {
  return isJSON(value) ? value : mark(value);
}

function isJSON(value: unknown): boolean {
  if (value === null || typeof value === 'string' || typeof value === 'boolean' || value === undefined) {
    return true;
  }
  if (typeof value === 'number') {
    return Number.isFinite(value);
  }
  if (Array.isArray(value)) {
    return value.every((item) => item !== undefined && isJSON(item));
  }
  if (isPlainObject(value)) {
    return Object.values(value).every(isJSON);
  }
  return false;
}

function mark(value: unknown): unknown {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') {
    return value;
  }
  if (typeof value === 'number') {
    return Number.isFinite(value) ? value : { [NOT_JSON]: `${String(value)} is not a JSON number` };
  }
  if (Array.isArray(value)) {
    return value.map((item: unknown) => (item === undefined ? { [NOT_JSON]: 'undefined is not a JSON value' } : mark(item)));
  }
  if (isPlainObject(value)) {
    const out: Record<string, unknown> = {};
    for (const [key, member] of Object.entries(value)) {
      if (member !== undefined) {
        setMember(out, key, mark(member));
      }
    }
    return out;
  }
  const kind = typeof value === 'object' ? ((value as { constructor?: { name?: string } }).constructor?.name ?? 'object') : typeof value;
  return { [NOT_JSON]: `a ${kind} is not a JSON value` };
}
