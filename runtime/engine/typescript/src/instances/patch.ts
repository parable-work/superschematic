/*
JSON merge patch (RFC 7386), the engine's update rule: a member of the
patch replaces the instance's member, a nested object merges into the
nested object, null removes the member, and an array or any other value
replaces what was there. A member whose value is undefined is absent, as
it is in JSON.
*/

/** mergePatch returns target with patch applied; it changes neither. */
export function mergePatch(target: unknown, patch: unknown): unknown {
  if (!isPlainObject(patch)) {
    return patch;
  }
  const result: Record<string, unknown> = isPlainObject(target) ? { ...target } : {};
  for (const [key, value] of Object.entries(patch)) {
    if (value === undefined) {
      continue;
    }
    if (value === null) {
      delete result[key];
    } else {
      setMember(result, key, mergePatch(result[key], value));
    }
  }
  return result;
}

/**
 * diffPatch returns the merge patch that turns before into after: each
 * member that is new or changed, a nested object changed member by member,
 * and null for a member after no longer has. Applying it to before with
 * mergePatch gives after, for values that hold no null.
 */
export function diffPatch(before: Readonly<Record<string, unknown>>, after: Readonly<Record<string, unknown>>): Record<string, unknown> {
  const patch: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(after)) {
    if (value === undefined) {
      continue;
    }
    const previous = Object.prototype.hasOwnProperty.call(before, key) ? before[key] : undefined;
    if (previous !== undefined && jsonEqual(previous, value)) {
      continue;
    }
    setMember(patch, key, isPlainObject(previous) && isPlainObject(value) ? diffPatch(previous, value) : value);
  }
  for (const [key, value] of Object.entries(before)) {
    if (value !== undefined && (!Object.prototype.hasOwnProperty.call(after, key) || after[key] === undefined)) {
      setMember(patch, key, null);
    }
  }
  return patch;
}

/** setMember sets an own member, so one named __proto__ stays a member. */
export function setMember(target: Record<string, unknown>, key: string, value: unknown): void {
  Object.defineProperty(target, key, { value, enumerable: true, writable: true, configurable: true });
}

/** jsonEqual compares two JSON values; object members compare in any order. */
export function jsonEqual(a: unknown, b: unknown): boolean {
  if (a === b) {
    return true;
  }
  if (Array.isArray(a) || Array.isArray(b)) {
    return Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((item, index) => jsonEqual(item, b[index]));
  }
  if (!isPlainObject(a) || !isPlainObject(b)) {
    return false;
  }
  const keys = Object.keys(a).filter((key) => a[key] !== undefined);
  const otherKeys = Object.keys(b).filter((key) => b[key] !== undefined);
  return keys.length === otherKeys.length && keys.every((key) => Object.prototype.hasOwnProperty.call(b, key) && jsonEqual(a[key], b[key]));
}

export function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}
