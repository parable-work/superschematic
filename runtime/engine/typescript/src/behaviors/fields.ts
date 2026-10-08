/*
How an instance's behavior fields are named and read (D16, amended: a
behavior's fields sit under its name). A read returns them apart from the
instance's own fields, in `behaviors`, by behavior name, so a behavior's
field collides with nothing. Wherever a string names one, a read's fields,
a list's filter, a rollup's field and a display's summary field, it is the
field's qualified name: the behavior's name, a dot, the field's. An own
field's name holds no dot, so the two never meet.
*/

/** An instance's behavior fields: by behavior name, an object of the fields the behavior declares that have a value. */
export type BehaviorFields = Record<string, Record<string, unknown>>;

/**
 * An instance's fields as a read returns them, and as the log holds them:
 * its own fields in data, its behaviors' fields in behaviors. A reaction's
 * before() returns one.
 */
export interface InstanceFields {
  readonly data: Readonly<Record<string, unknown>>;
  readonly behaviors: Readonly<Record<string, Readonly<Record<string, unknown>>>>;
}

/**
 * fieldPath is a behavior field's qualified name: the behavior's name, a
 * dot, the field's, as `Workflow.status`, `Lease.holder` and
 * `acme.Rating.count` name theirs.
 */
export function fieldPath(behavior: string, field: string): string {
  return `${behavior}.${field}`;
}

/**
 * behaviorField reads one behavior field of what a read returns, or of an
 * instance as the log holds it: undefined when the behavior or the field
 * has no value there.
 */
export function behaviorField(
  record: { readonly behaviors?: Readonly<Record<string, Readonly<Record<string, unknown>> | undefined>> } | null | undefined,
  behavior: string,
  field: string
): unknown {
  const fields = record?.behaviors?.[behavior];
  return fields !== undefined && Object.prototype.hasOwnProperty.call(fields, field) ? fields[field] : undefined;
}

/**
 * The fields of the core's behaviors other behaviors read by qualified
 * name: Workflow's status, Links' targets, Revisions' revision and
 * Branches' release.
 */
export const WORKFLOW_STATUS = fieldPath('Workflow', 'status');
export const LINKS_TARGETS = fieldPath('Links', 'targets');
export const REVISIONS_REVISION = fieldPath('Revisions', 'revision');
export const BRANCHES_RELEASE = fieldPath('Branches', 'release');
