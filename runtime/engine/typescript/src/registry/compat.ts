/*
The compatibility rule (D16): a new version of a schema may change it only
in ways every instance the live version accepts still satisfies. The diff
walks what an instance can hold under the live version, the instance type
and the types, enums and scalars its fields reach, and compares each with
the new version:

- a field removed, renamed or given another JSON key, a changed type or
  list depth, an optional field made required, a new required field, a
  narrower bound (a higher minLength, min or listMin, a lower maxLength,
  max or listMax, a bound where there was none), a new or changed pattern,
  a removed enum value, a removed type or enum, and a scalar that accepts
  less are refused;
- a new optional field, a new enum value, a wider or removed bound, a
  required field made optional, and any change that does not affect which
  values validate (descriptions, comments, defaults, UI metadata) pass.

The walk also starts at each type a behavior's validate checks values
against under both versions (checkedTypes, behaviors/composition.ts), a
Variants type say: a stored instance holds values the live version
checked against it, so it is held to the rule as a type a field reaches.
Other types, enums and scalars no field reaches are free to change.
Scalars are compared as the schema runtime reads them to validate: a
scalar the builtin catalog holds is the catalog's, whatever the document
declares for it (runtimeDocument), and any other is the document's.
*/

import { parseSchemaIR, type ScalarDef, type Schema } from '@superschematic/schema-runtime';
import type { EnumDef, FieldDef, TypeRef } from '@superschematic/schema-ir/schema-file';

import type { SchemaChange } from '../errors.js';
import { arrayDepth, jsonKey, refKind, runtimeDocument, scalarKey, type SchemaModel } from './document.js';

/**
 * incompatibleChanges lists every change from before to after that the
 * rule refuses; empty means compatible. checked names the types besides
 * the instance type a behavior checks values against under both versions,
 * which the walk starts from too.
 */
export function incompatibleChanges(before: SchemaModel, after: SchemaModel, checked: readonly string[] = []): SchemaChange[] {
  const changes: SchemaChange[] = [];
  if (before.instanceType !== after.instanceType) {
    changes.push({
      path: before.instanceType,
      message: `the instance type changes from ${before.instanceType} to ${after.instanceType}`,
    });
    return changes;
  }

  const oldTypes = before.document.types ?? {};
  const newTypes = after.document.types ?? {};
  const enums = new Set<string>();
  const scalars = new Set<string>();
  const seen = new Set<string>();
  const queue = [before.instanceType, ...checked];
  while (queue.length > 0) {
    const typeName = queue.shift() as string;
    if (seen.has(typeName)) {
      continue;
    }
    seen.add(typeName);
    const oldType = oldTypes[typeName];
    if (oldType === undefined) {
      continue;
    }
    const newType = newTypes[typeName];
    if (newType === undefined) {
      changes.push({ path: typeName, message: `type ${typeName} is removed` });
      continue;
    }
    // A field that is removed or changes type is reported as such; what
    // its old type reaches matters only through the fields that keep it.
    for (const field of compareFields(typeName, oldType.fields ?? [], newType.fields ?? [], changes)) {
      switch (refKind(before.document, field.typeRef.name)) {
        case 'type':
          queue.push(field.typeRef.name);
          break;
        case 'enum':
          enums.add(field.typeRef.name);
          break;
        case 'scalar':
          scalars.add(field.typeRef.name);
          break;
        default:
          break;
      }
    }
  }

  for (const enumName of [...enums].sort()) {
    compareEnum(enumName, (before.document.enums ?? {})[enumName] as EnumDef, (after.document.enums ?? {})[enumName], changes);
  }

  if (scalars.size > 0) {
    const oldSchema = parseSchemaIR(runtimeDocument(before.document));
    const newSchema = parseSchemaIR(runtimeDocument(after.document));
    for (const scalarName of [...scalars].sort()) {
      compareScalar(scalarName, oldSchema, newSchema, changes);
    }
  }
  return changes;
}

// compareFields reports the changes to one type's fields and returns the
// old fields that keep their type.
function compareFields(typeName: string, oldFields: FieldDef[], newFields: FieldDef[], changes: SchemaChange[]): FieldDef[] {
  const newByName = new Map(newFields.map((field) => [field.name, field]));
  const kept: FieldDef[] = [];
  for (const oldField of oldFields) {
    const path = `${typeName}.${oldField.name}`;
    const newField = newByName.get(oldField.name);
    if (newField === undefined) {
      changes.push({ path, message: `field ${path} is removed` });
      continue;
    }
    if (jsonKey(oldField) !== jsonKey(newField)) {
      changes.push({
        path,
        message: `field ${path} changes its JSON key from ${jsonKey(oldField)} to ${jsonKey(newField)}`,
      });
    }
    if (
      oldField.typeRef.name !== newField.typeRef.name ||
      arrayDepth(oldField.typeRef) !== arrayDepth(newField.typeRef) ||
      Boolean(oldField.typeRef.isMap) !== Boolean(newField.typeRef.isMap)
    ) {
      changes.push({
        path,
        message: `field ${path} changes type from ${typeLabel(oldField.typeRef)} to ${typeLabel(newField.typeRef)}`,
      });
      // Its bounds applied to another type; the type change says enough.
      continue;
    }
    kept.push(oldField);
    if (!oldField.required && newField.required) {
      changes.push({ path, message: `field ${path} becomes required` });
    }
    lowerBound(changes, path, 'minLength', oldField.validateMinLength, newField.validateMinLength);
    upperBound(changes, path, 'maxLength', oldField.validateMaxLength, newField.validateMaxLength);
    lowerBound(changes, path, 'min', oldField.validateMin, newField.validateMin);
    upperBound(changes, path, 'max', oldField.validateMax, newField.validateMax);
    lowerBound(changes, path, 'listMin', oldField.validateListMin, newField.validateListMin);
    upperBound(changes, path, 'listMax', oldField.validateListMax, newField.validateListMax);
    pattern(changes, `field ${path}`, path, oldField.validatePattern ?? '', newField.validatePattern ?? '');
  }
  const oldNames = new Set(oldFields.map((field) => field.name));
  for (const newField of newFields) {
    if (!oldNames.has(newField.name) && newField.required) {
      const path = `${typeName}.${newField.name}`;
      changes.push({ path, message: `field ${path} is added as required` });
    }
  }
  return kept;
}

function compareEnum(enumName: string, oldEnum: EnumDef, newEnum: EnumDef | undefined, changes: SchemaChange[]): void {
  if (newEnum === undefined) {
    changes.push({ path: enumName, message: `enum ${enumName} is removed` });
    return;
  }
  const kept = new Set(newEnum.values.map((value) => value.serializedAs || value.name));
  for (const value of oldEnum.values) {
    const serialized = value.serializedAs || value.name;
    if (!kept.has(serialized)) {
      changes.push({ path: enumName, message: `enum ${enumName} drops value ${serialized}` });
    }
  }
}

// The schema runtime's scalar rules: 0 is no length bound, null no range
// bound, '' no pattern.
function compareScalar(scalarName: string, oldSchema: Schema, newSchema: Schema, changes: SchemaChange[]): void {
  const key = scalarKey(scalarName);
  const oldScalar = (oldSchema.scalars ?? {})[key] as ScalarDef;
  const newScalar = (newSchema.scalars ?? {})[key];
  const label = `scalar ${scalarName}`;
  if (newScalar === undefined) {
    changes.push({ path: scalarName, message: `${label} is removed` });
    return;
  }
  if (oldScalar.primitive !== newScalar.primitive) {
    changes.push({ path: scalarName, message: `${label} changes primitive from ${oldScalar.primitive} to ${newScalar.primitive}` });
  }
  const oldJSONType = oldScalar.typeMappings?.json_schema ?? '';
  const newJSONType = newScalar.typeMappings?.json_schema ?? '';
  if (oldJSONType !== newJSONType) {
    changes.push({ path: scalarName, message: `${label} changes its JSON type from ${oldJSONType || 'none'} to ${newJSONType || 'none'}` });
  }
  lowerBound(changes, scalarName, 'minLength', positive(oldScalar.minLength), positive(newScalar.minLength), label);
  upperBound(changes, scalarName, 'maxLength', positive(oldScalar.maxLength), positive(newScalar.maxLength), label);
  lowerBound(changes, scalarName, 'minimum', oldScalar.minimum ?? undefined, newScalar.minimum ?? undefined, label);
  upperBound(changes, scalarName, 'maximum', oldScalar.maximum ?? undefined, newScalar.maximum ?? undefined, label);
  pattern(changes, label, scalarName, oldScalar.pattern, newScalar.pattern);
  const oldWords = new Set(oldScalar.reservedWords);
  const added = newScalar.reservedWords.filter((word) => !oldWords.has(word));
  if (added.length > 0) {
    changes.push({ path: scalarName, message: `${label} reserves ${added.join(', ')}` });
  }
  if (
    (!oldScalar.caseInsensitive && newScalar.caseInsensitive) ||
    (!oldScalar.reservedWordsCaseInsensitive && newScalar.reservedWordsCaseInsensitive) ||
    (!oldScalar.reservedWordsMatchPartial && newScalar.reservedWordsMatchPartial)
  ) {
    changes.push({ path: scalarName, message: `${label} matches its reserved words more widely` });
  }
  if (!oldScalar.hasCustomValidate && newScalar.hasCustomValidate) {
    changes.push({ path: scalarName, message: `${label} gains a custom validator` });
  }
}

// A lower bound (minLength, min, listMin) may fall or go; it may not rise
// or appear.
function lowerBound(
  changes: SchemaChange[],
  path: string,
  rule: string,
  before: number | undefined,
  after: number | undefined,
  label = `field ${path}`
): void {
  if (after === undefined || (before !== undefined && after <= before)) {
    return;
  }
  changes.push({
    path,
    message: before === undefined ? `${label} gains ${rule} ${after}` : `${label} raises ${rule} from ${before} to ${after}`,
  });
}

// An upper bound (maxLength, max, listMax) may rise or go; it may not fall
// or appear.
function upperBound(
  changes: SchemaChange[],
  path: string,
  rule: string,
  before: number | undefined,
  after: number | undefined,
  label = `field ${path}`
): void {
  if (after === undefined || (before !== undefined && after >= before)) {
    return;
  }
  changes.push({
    path,
    message: before === undefined ? `${label} gains ${rule} ${after}` : `${label} lowers ${rule} from ${before} to ${after}`,
  });
}

// Whether one pattern accepts every string another does is not decidable
// here, so a pattern may only be dropped.
function pattern(changes: SchemaChange[], label: string, path: string, before: string, after: string): void {
  if (after === '' || after === before) {
    return;
  }
  changes.push({ path, message: before === '' ? `${label} gains a pattern` : `${label} changes its pattern` });
}

function positive(value: number): number | undefined {
  return value > 0 ? value : undefined;
}

function typeLabel(typeRef: TypeRef): string {
  const suffix = '[]'.repeat(arrayDepth(typeRef));
  return typeRef.isMap ? `Record<string, ${typeRef.name}>` : `${typeRef.name}${suffix}`;
}
