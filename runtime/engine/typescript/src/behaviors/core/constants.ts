/*
Constants, fields set at create and never changed after (D16). Work is
routed by what an instance is: a step's kind, the key a blueprint stamped
it with, the fields it copied from its parent. When any writer can change
them, the holder of a lease included, a worker can turn the step it holds
into another one. The config lists the type's own top-level fields, by
JSON key, that keep the value their create gives them.

validate refuses an update that changes a listed field, whoever makes it
(a caller's update, or an operation's update() on a caller's behalf),
with the rule `constant` at the field, unless the caller holds the
config's permission. A field the create leaves absent stays absent: its
absence is the value the create set, and setting it later is a change.
Absent and null are one value, none, since a merge patch removes a member
with null. A value is compared as JSON, so a change inside an object or a
list is a change. A create is never refused: whatever it gives is what the
fields keep.

configChange: any config may change, and the behavior can be added to
and removed from a schema with instances: it keeps no state, and a stored
instance satisfies any list, since validate only compares an update with
what the instance holds.
*/

import { jsonEqual } from '../../instances/patch.js';
import { BehaviorConfigError, defineBehavior, type FrozenJSON } from '../behavior.js';
import declaration from './declarations/Constants.behavior.json' with { type: 'json' };

/** Constants' config, parsed. */
export interface ConstantsConfig {
  /** The type's own top-level fields, by JSON key, that keep the value their create gives them. */
  readonly fields: readonly string[];
  /** The permission a caller needs to change them after the create. */
  readonly permission?: string;
}

// valueOf is a field's value, null for none: absent and null are one.
function valueOf(data: FrozenJSON, field: string): unknown {
  const value = Object.prototype.hasOwnProperty.call(data, field) ? data[field] : undefined;
  return value === undefined ? null : value;
}

export const constants = defineBehavior<ConstantsConfig>({
  declaration,

  // The configSchema holds the shape; this holds the fields to the type's own.
  parseConfig(json, target) {
    const raw = json as { fields: string[]; permission?: string };
    for (const field of raw.fields) {
      if (!target.fields.includes(field)) {
        throw new BehaviorConfigError(`fields: ${field} is not a field of ${target.type} (its fields: ${target.fields.join(', ')})`);
      }
    }
    return { fields: [...raw.fields], ...(raw.permission === undefined ? {} : { permission: raw.permission }) };
  },

  configChange() {
    return undefined;
  },

  validate(context, request) {
    if (request.kind !== 'update') {
      return undefined;
    }
    const { fields, permission } = context.config;
    const changed = fields.filter((field) => !jsonEqual(valueOf(request.before, field), valueOf(request.after, field)));
    if (changed.length === 0 || (permission !== undefined && context.can(permission))) {
      return undefined;
    }
    const unless = permission === undefined ? '' : `; only a caller with ${permission} may change it`;
    return changed.map((field) => ({
      path: field,
      rule: 'constant',
      message: `${field} is a constant of ${context.schema}: its create sets it and nothing changes it after${unless}`,
    }));
  },
});
