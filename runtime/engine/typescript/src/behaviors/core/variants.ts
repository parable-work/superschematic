/*
Variants, a field typed by another field's value (D16). A step's result
has a different shape for each kind of step, and the engine refuses a
union field, which the schema runtime does not check. Without this the
field is Generic.JSON, and a result in the wrong shape is accepted
without a word: every reader downstream has to guess what it holds.

The config names field, an own field of the type whose values are open
JSON objects (Generic.JSON, or a scalar whose values are JSON objects),
by, an own string or enum field, and types: by a value of by, a type of
the schema document besides the instance type. validate holds field, when
it holds a value, to the type by's value picks, by the rules the live
version holds a field of that type to (checkType): a JSON object, each
field's value, and no key the type does not declare, at any depth. While
by holds a value types does not list, or none, field holds none: a value
there is refused with the rule `variant`. So a value of by gains a type in
a later version without a stored instance breaking: no instance holds a
value for it yet. A field that is required then admits only the listed
values of by. Absent and null are one value, none, as a merge patch has it.

parseConfig checks that field and by are distinct own fields of those
kinds, that each type is a type of the document besides the instance
type, and, when by is an enum, that each value is one of its values.

checkedTypes names the types, so the compatibility rule holds a new
version to each as to a type a field reaches: a type a stored value was
checked against changes only in ways that value still satisfies.
instanceSchema writes the rule as JSON Schema for the describe document
and the create and update tools: an if/then per listed value, which
holds field to its type when by holds the value, and one more, which
holds field to null when by holds another value or none. In a patch, the
type has nothing required and an if needs by in the patch, since a patch
that leaves by alone does not show it.

configChange: field and by stay; each value keeps its type, and a value
may be added, since no stored instance holds a value for it. Removing
the behavior is allowed, which leaves field open JSON again; adding it
to a schema with instances is not, since their values were never checked.
*/

import { BehaviorConfigError, defineBehavior, type FrozenJSON } from '../behavior.js';
import declaration from './declarations/Variants.behavior.json' with { type: 'json' };
import { variantsGuidance } from './guidance/variants.js';

/** Variants' config, parsed. */
export interface VariantsConfig {
  /** The type's own field the variants type, by JSON key. */
  readonly field: string;
  /** The type's own string or enum field whose value picks the variant, by JSON key. */
  readonly by: string;
  /** By a value of by, the type of the schema document field holds while by holds it. */
  readonly types: Readonly<Record<string, string>>;
  /** Whether field is required, so that an instance always holds a value of it. */
  readonly required: boolean;
}

function hasOwn(record: object, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(record, key);
}

// jsonTypes reads the JSON types a field's JSON Schema takes, null aside.
function jsonTypes(schema: unknown): string[] {
  const type = (schema as { type?: unknown } | undefined)?.type;
  return (Array.isArray(type) ? type : [type]).filter((one): one is string => typeof one === 'string' && one !== 'null');
}

function nullable(schema: unknown): boolean {
  const type = (schema as { type?: unknown } | undefined)?.type;
  return Array.isArray(type) && type.includes('null');
}

// valueOf is a field's value, undefined for none: absent and null are one.
function valueOf(data: FrozenJSON, field: string): unknown {
  const value = hasOwn(data, field) ? data[field] : undefined;
  return value === null ? undefined : value;
}

export const variants = defineBehavior<VariantsConfig>({
  declaration,

  guidance: variantsGuidance,

  // The configSchema holds the shape; this holds the config to the type
  // and the document, as the header says.
  parseConfig(json, target) {
    const raw = json as { field: string; by: string; types: Record<string, string> };
    for (const [role, name] of [
      ['field', raw.field],
      ['by', raw.by],
    ] as const) {
      if (!target.fields.includes(name)) {
        throw new BehaviorConfigError(`${role}: ${name} is not a field of ${target.type} (its fields: ${target.fields.join(', ')})`);
      }
    }
    if (raw.field === raw.by) {
      throw new BehaviorConfigError(`field and by both name ${raw.field}: a field cannot be typed by its own value`);
    }
    const fieldSchema = target.fieldSchemas[raw.field] as { items?: unknown; additionalProperties?: unknown } | undefined;
    if (!jsonTypes(fieldSchema).includes('object') || fieldSchema?.items !== undefined || fieldSchema?.additionalProperties === false) {
      throw new BehaviorConfigError(
        `field: ${target.type}.${raw.field} is not an open JSON object; Variants types a Generic.JSON field, or one of a scalar whose values are JSON objects`
      );
    }
    const bySchema = target.fieldSchemas[raw.by] as { items?: unknown; enum?: unknown } | undefined;
    const byTypes = jsonTypes(bySchema);
    if (byTypes.length !== 1 || byTypes[0] !== 'string' || bySchema?.items !== undefined) {
      throw new BehaviorConfigError(`by: ${target.type}.${raw.by} is not a string or enum field`);
    }
    const values = Array.isArray(bySchema?.enum) ? bySchema.enum.filter((value): value is string => typeof value === 'string') : undefined;
    for (const [value, type] of Object.entries(raw.types)) {
      if (!target.types.names.includes(type)) {
        throw new BehaviorConfigError(
          `types: ${JSON.stringify(value)} names ${type}, which is not a type of the schema document besides ${target.type} (its types: ${target.types.names.join(', ') || 'none'})`
        );
      }
      if (values !== undefined && !values.includes(value)) {
        throw new BehaviorConfigError(`types: ${JSON.stringify(value)} is not a value of ${raw.by} (its values: ${values.join(', ')})`);
      }
    }
    return { field: raw.field, by: raw.by, types: { ...raw.types }, required: !nullable(fieldSchema) };
  },

  configChange(before, after) {
    if (before === undefined) {
      return `the instances already hold values of ${(after as VariantsConfig).field} that no type checked`;
    }
    if (after === undefined) {
      return undefined;
    }
    if (before.field !== after.field || before.by !== after.by) {
      return `field and by stay ${before.field} and ${before.by}: the instances hold values of ${before.field} typed by ${before.by}`;
    }
    for (const [value, type] of Object.entries(before.types)) {
      if (!hasOwn(after.types, value)) {
        return `types keeps ${JSON.stringify(value)}: an instance whose ${before.by} is ${JSON.stringify(value)} may hold a ${type}, which a version without it refuses`;
      }
      if (after.types[value] !== type) {
        return `types: ${JSON.stringify(value)} stays ${type}: an instance's ${before.field} was checked against it, not against ${after.types[value]}`;
      }
    }
    return undefined;
  },

  checkedTypes(config) {
    return [...new Set(Object.values(config.types))];
  },

  validate(context, request) {
    const { field, by, types } = context.config;
    const data = request.kind === 'create' ? request.data : request.after;
    const value = valueOf(data, field);
    if (value === undefined) {
      return undefined;
    }
    const key = valueOf(data, by);
    const type = typeof key === 'string' && hasOwn(types, key) ? types[key] : undefined;
    if (type !== undefined) {
      return context.checkType(type, value, field);
    }
    const holds = key === undefined ? 'holds none' : `is ${JSON.stringify(key)}`;
    return [
      {
        path: field,
        rule: 'variant',
        message: `${field} holds a value while ${by} ${holds}; ${field} has a type only while ${by} is ${Object.keys(types)
          .map((listed) => JSON.stringify(listed))
          .join(', ')}, and no value otherwise`,
      },
    ];
  },

  instanceSchema(config, form, typeSchema) {
    const { field, by, types, required } = config;
    const listed = Object.keys(types);
    const rules: unknown[] = listed.map((value) => ({
      description: `While ${by} is ${JSON.stringify(value)}, ${field} is a ${types[value]}`,
      if: { properties: { [by]: { const: value } }, required: [by] },
      then: { properties: { [field]: typeSchema(types[value], { nullable: !required }) } },
    }));
    rules.push({
      description: `While ${by} holds another value or none, ${field} holds none`,
      if: form === 'instance' ? { not: { properties: { [by]: { enum: listed } }, required: [by] } } : { properties: { [by]: { not: { enum: listed } } }, required: [by] },
      then: { properties: { [field]: { type: 'null' } } },
    });
    return rules;
  },
});
