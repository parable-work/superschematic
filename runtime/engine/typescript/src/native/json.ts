/*
JSON values across the line between the engine and TypeScript code: what
a behavior written in TypeScript returns is copied and checked to be JSON
(jsonCopy), and what it is given, and what the engine returns that the
TypeScript engine froze, is deep-frozen (deepFreeze), so neither side can
change the other's value afterwards. setMember and isPlainObject are the
helpers both read objects with.
*/

/**
 * jsonCopy returns a deep copy of value when it is JSON (plain objects and
 * arrays, strings, finite numbers, booleans, null; a member whose value is
 * undefined is dropped), or the path of the first part that is not.
 */
export function jsonCopy(value: unknown, path = ''): { value: unknown } | { path: string; problem: string } {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') {
    return { value };
  }
  if (typeof value === 'number') {
    return Number.isFinite(value) ? { value } : { path, problem: `${String(value)} is not a JSON number` };
  }
  if (Array.isArray(value)) {
    const out: unknown[] = [];
    for (let index = 0; index < value.length; index += 1) {
      const element: unknown = value[index];
      if (element === undefined) {
        return { path: `${path}[${index}]`, problem: 'undefined is not a JSON value' };
      }
      const copied = jsonCopy(element, `${path}[${index}]`);
      if (!('value' in copied)) {
        return copied;
      }
      out.push(copied.value);
    }
    return { value: out };
  }
  if (isPlainObject(value)) {
    const out: Record<string, unknown> = {};
    for (const [key, member] of Object.entries(value)) {
      if (member === undefined) {
        continue;
      }
      const copied = jsonCopy(member, path === '' ? key : `${path}.${key}`);
      if (!('value' in copied)) {
        return copied;
      }
      setMember(out, key, copied.value);
    }
    return { value: out };
  }
  const kind = typeof value === 'object' ? (value.constructor?.name ?? 'object') : typeof value;
  return { path, problem: `a ${kind} is not a JSON value` };
}

/** deepFreeze freezes a JSON value and everything in it, and returns it. */
export function deepFreeze<T>(value: T): T {
  if (typeof value === 'object' && value !== null && !Object.isFrozen(value)) {
    Object.freeze(value);
    for (const member of Object.values(value as object)) {
      deepFreeze(member);
    }
  }
  return value;
}

/** setMember sets an own member, so one named __proto__ stays a member. */
export function setMember(target: Record<string, unknown>, key: string, value: unknown): void {
  Object.defineProperty(target, key, { value, enumerable: true, writable: true, configurable: true });
}

/** isPlainObject reports whether a value is a plain object: not null, an array or a class's instance. */
export function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}
