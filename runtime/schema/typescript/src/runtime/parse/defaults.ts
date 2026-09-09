/**
 * Default-value parsing. FieldDef.defaultValue holds the schema-declared default
 * as a string; this parses it into the appropriate primitive when the field is
 * absent during parse. Defaults on object-typed fields and arrays are NOT applied
 * here -- the walker skips applyDefault for those kinds.
 */

import type { FieldDef, ScalarDef } from '../validation/types';

export function applyDefault(
  field: FieldDef,
  scalar: ScalarDef | null
): [unknown, boolean] {
  if (field.defaultValue === null || field.defaultValue === undefined) {
    return [null, false];
  }
  const raw = field.defaultValue;
  const primitive = scalar
    ? scalar.primitive
    : primitiveForBuiltin(field.typeRef.name);

  switch (primitive) {
    case 'Int': {
      const parsed = Number(raw);
      if (!Number.isFinite(parsed)) {
        return [null, false];
      }
      return [Math.trunc(parsed), true];
    }
    case 'Float': {
      const parsed = Number(raw);
      if (!Number.isFinite(parsed)) {
        return [null, false];
      }
      return [parsed, true];
    }
    case 'Boolean': {
      if (raw === 'true' || raw === '1') return [true, true];
      if (raw === 'false' || raw === '0') return [false, true];
      return [null, false];
    }
    default:
      return [raw, true];
  }
}

export function primitiveForBuiltin(name: string): string {
  switch (name) {
    case 'Int':
      return 'Int';
    case 'Float':
      return 'Float';
    case 'Boolean':
      return 'Boolean';
    case 'String':
    case 'ID':
      return 'String';
    default:
      return '';
  }
}
