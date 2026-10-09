/*
A type's display (D48): what a UI, or an agent, reads to render its
instances, written with @display. The meta-schema holds its shape; at
define and publish the engine holds it to the document as the compiler's
loader does (internal/loader/verify/display.go), with the same wording:

- titleField names one of the type's own fields, by its name or its JSON
  key, that holds a single text value: a string primitive, or a scalar
  whose values are strings, not a list or a map, and neither secret nor
  uiHidden;
- each summaryFields entry names one of the type's own fields that is
  neither secret nor uiHidden, or a field one of its behaviors adds, by
  its qualified name (Workflow.status), as a read returns it under the
  behavior's name;
- states and transitions label the states and transitions of the type's
  Workflow, as its config lists them, so a type that does not compose
  Workflow, a nested type among them, takes neither.

describedDisplay writes a display as the describe document carries it:
titleField and summaryFields name each own field by its JSON key, the key
an instance's data has it under, and each behavior field by its qualified
name.
*/

import type { Document, FieldDef, TypeDef, TypeDisplay } from '@superschematic/schema-ir/schema-file';
import { BUILTIN_SCALARS, isAnyJSONScalar, parseSchemaIR, structuredJSONType } from '@superschematic/schema-runtime';

import type { Composition } from '../behaviors/composition.js';
import type { SchemaIssue } from '../errors.js';
import { jsonKey, pointer, refKind, scalarKey } from './document.js';

/** The core behavior whose states and transitions a display labels. */
const WORKFLOW = 'Workflow';

// What a display reads of a Workflow config, as parseConfig returns it.
interface WorkflowStates {
  readonly states: readonly string[];
  readonly transitions: ReadonlyArray<{ readonly from: string; readonly to: string }>;
}

/**
 * displayIssues checks the display of every type of a document whose
 * instance type composed, against the type and, for the instance type,
 * its behaviors.
 */
export function displayIssues(document: Document, instanceType: string, composition: Composition): SchemaIssue[] {
  const issues: SchemaIssue[] = [];
  const types = document.types ?? {};
  for (const typeName of Object.keys(types).sort()) {
    const type = types[typeName] as TypeDef;
    if (type.display === undefined) {
      continue;
    }
    const instance = typeName === instanceType;
    issues.push(...typeIssues(document, typeName, type, instance ? composition : undefined));
  }
  return issues;
}

function typeIssues(document: Document, typeName: string, type: TypeDef, composition: Composition | undefined): SchemaIssue[] {
  const issues: SchemaIssue[] = [];
  const display = type.display as TypeDisplay;
  const path = pointer('types', typeName, 'display');
  const fields = type.fields ?? [];
  const own = (name: string): FieldDef | undefined => fields.find((field) => field.name === name || (field.jsonTag !== undefined && field.jsonTag !== '' && field.jsonTag === name));
  const listed = fields.length === 0 ? '' : ` (fields: ${fields.map((field) => field.name).join(', ')})`;
  const shown = (field: FieldDef, member: string, at: string): void => {
    if (field.secret) {
      issues.push({ path: at, message: `type ${type.name}: @display ${member} names field ${field.name}, which is secret` });
    } else if (field.uiHidden) {
      issues.push({ path: at, message: `type ${type.name}: @display ${member} names field ${field.name}, which is @uiHidden` });
    }
  };

  if (display.titleField !== undefined) {
    const at = `${path}/titleField`;
    const field = own(display.titleField);
    const owner = composition?.fields.get(display.titleField)?.behavior.name;
    if (field === undefined && owner !== undefined) {
      issues.push({
        path: at,
        message: `type ${type.name}: @display titleField ${JSON.stringify(display.titleField)} is a field behavior ${owner} adds; a title is one of the type's own fields${listed}`,
      });
    } else if (field === undefined) {
      issues.push({ path: at, message: `type ${type.name}: @display titleField ${JSON.stringify(display.titleField)} is not a field of the type${listed}` });
    } else if (!textField(document, field)) {
      issues.push({
        path: at,
        message: `type ${type.name}: @display titleField ${field.name} has type ${typeLabel(field)}; a title is a single text value: a string, or a scalar whose values are strings`,
      });
    } else {
      shown(field, 'titleField', at);
    }
  }

  (display.summaryFields ?? []).forEach((name, index) => {
    const at = `${path}/summaryFields/${index}`;
    const field = own(name);
    if (field !== undefined) {
      shown(field, 'summaryFields', at);
    } else if (!composition?.fields.has(name)) {
      // A behavior's field named bare: say the name that reaches it.
      const qualified = (composition?.behaviors ?? [])
        .filter((bound) => bound.behavior.fields.some((candidate) => candidate.name === name))
        .map((bound) => `${bound.behavior.name}.${name}`);
      const hint = qualified.length === 0 ? '' : `; a behavior's field is named by its qualified name: ${qualified.join(' or ')}`;
      issues.push({
        path: at,
        message: `type ${type.name}: @display summaryFields lists ${JSON.stringify(name)}, which is not a field of the type or of its behaviors${listed}${hint}`,
      });
    }
  });

  const states = Object.keys(display.states ?? {}).sort();
  const transitions = Object.keys(display.transitions ?? {}).sort();
  if (states.length === 0 && transitions.length === 0) {
    return issues;
  }
  const workflow = composition?.bound(WORKFLOW)?.config as WorkflowStates | undefined;
  if (workflow === undefined) {
    issues.push({ path, message: `type ${type.name}: @display states and transitions label a Workflow's; the type does not compose Workflow` });
    return issues;
  }
  for (const state of states) {
    if (!workflow.states.includes(state)) {
      issues.push({
        path: pointer('types', typeName, 'display', 'states', state),
        message: `type ${type.name}: @display states labels ${JSON.stringify(state)}, which is not a state of its Workflow (${workflow.states.join(', ')})`,
      });
    }
  }
  for (const from of transitions) {
    for (const to of Object.keys((display.transitions ?? {})[from]).sort()) {
      if (!workflow.transitions.some((transition) => transition.from === from && transition.to === to)) {
        issues.push({
          path: pointer('types', typeName, 'display', 'transitions', from, to),
          message: `type ${type.name}: @display transitions labels the move from ${from} to ${to}, which is not a transition of its Workflow`,
        });
      }
    }
  }
  return issues;
}

/** describedDisplay is a type's display with the fields it names by their JSON keys. */
export function describedDisplay(type: TypeDef): TypeDisplay | undefined {
  const display = type.display;
  if (display === undefined) {
    return undefined;
  }
  const keyOf = (name: string): string => {
    const field = (type.fields ?? []).find((candidate) => candidate.name === name);
    return field === undefined ? name : jsonKey(field);
  };
  return {
    ...display,
    ...(display.titleField !== undefined ? { titleField: keyOf(display.titleField) } : {}),
    ...(display.summaryFields !== undefined ? { summaryFields: display.summaryFields.map(keyOf) } : {}),
  };
}

// textField reports whether a field holds a single text value: a string
// primitive, or a scalar whose values are strings. The catalog gives a
// scalar that holds any JSON value, a JSON object or a JSON array the
// string primitive too, so the runtime's reading of it rules it out, as
// the compiler's loader rules it out by its json_schema mapping.
function textField(document: Document, field: FieldDef): boolean {
  const ref = field.typeRef;
  if (ref.isArray || ref.isMap) {
    return false;
  }
  switch (refKind(document, ref.name)) {
    case 'primitive':
      return ref.name === 'string' || ref.name === 'String' || ref.name === 'ID';
    case 'scalar': {
      // A catalog scalar is the catalog's, whatever the document declares.
      const builtin = BUILTIN_SCALARS[scalarKey(ref.name)];
      if (builtin === undefined && document.scalars?.[ref.name]?.languagePrimitive !== 'string') {
        return false;
      }
      const scalar = builtin ?? (parseSchemaIR({ scalars: { [ref.name]: document.scalars?.[ref.name] } }).scalars ?? {})[scalarKey(ref.name)];
      return scalar !== undefined && scalar.primitive === 'String' && !isAnyJSONScalar(scalar) && structuredJSONType(scalar) === '';
    }
    default:
      return false;
  }
}

// typeLabel writes a field's type as a message names it.
function typeLabel(field: FieldDef): string {
  const ref = field.typeRef;
  const label = `${ref.name}${ref.isArray ? (ref.isArrayOfArrays ? '[][]' : '[]') : ''}`;
  return ref.isMap ? `a map of ${label}` : label;
}
