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

/**
 * primitiveForBuiltin is the primitive a builtin name stands for: a GraphQL
 * scalar's, or the IR's number, boolean and string, whose default text
 * reads as a number, a boolean and the text itself (D14, amended: the
 * loader checks defaults and examples by the validators' rules); '' for any
 * other name.
 */
export function primitiveForBuiltin(name: string): string {
  switch (name) {
    case 'Int':
      return 'Int';
    case 'Float':
    case 'number':
      return 'Float';
    case 'Boolean':
    case 'boolean':
      return 'Boolean';
    case 'String':
    case 'ID':
    case 'string':
      return 'String';
    default:
      return '';
  }
}
