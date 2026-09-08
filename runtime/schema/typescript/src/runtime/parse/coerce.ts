/**
 * Lenient/strict scalar value coercion. Returns (value, ok). When ok is false
 * the caller emits a {validator: "type"} error at the field path. Lenient mode
 * accepts numeric and boolean-shaped strings; strict mode rejects anything not
 * already the target primitive.
 */

export function coerceInt(value: unknown, strict: boolean): [number, boolean] {
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value)) {
      return [0, false];
    }
    return [value, true];
  }
  if (typeof value === 'bigint') {
    const parsed = Number(value);
    if (!Number.isSafeInteger(parsed)) {
      return [0, false];
    }
    return [parsed, true];
  }
  if (typeof value === 'string') {
    if (strict) {
      return [0, false];
    }
    const trimmed = value.trim();
    if (trimmed === '') {
      return [0, false];
    }
    let parsedBigInt: bigint;
    try {
      parsedBigInt = BigInt(trimmed);
    } catch {
      return [0, false];
    }
    const parsed = Number(parsedBigInt);
    if (!Number.isSafeInteger(parsed)) {
      return [0, false];
    }
    return [parsed, true];
  }
  return [0, false];
}

export function coerceFloat(
  value: unknown,
  strict: boolean,
): [number, boolean] {
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) {
      return [0, false];
    }
    return [value, true];
  }
  if (typeof value === 'bigint') {
    return [Number(value), true];
  }
  if (typeof value === 'string') {
    if (strict) {
      return [0, false];
    }
    const trimmed = value.trim();
    if (trimmed === '') {
      return [0, false];
    }
    const parsed = Number(trimmed);
    if (!Number.isFinite(parsed)) {
      return [0, false];
    }
    return [parsed, true];
  }
  return [0, false];
}

export function coerceBool(
  value: unknown,
  strict: boolean,
): [boolean, boolean] {
  if (typeof value === 'boolean') {
    return [value, true];
  }
  if (typeof value === 'string') {
    if (strict) {
      return [false, false];
    }
    const trimmed = value.trim().toLowerCase();
    if (trimmed === 'true') {
      return [true, true];
    }
    if (trimmed === 'false') {
      return [false, true];
    }
  }
  return [false, false];
}
