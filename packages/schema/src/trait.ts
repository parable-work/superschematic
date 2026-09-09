export type TraitConfig = Record<string, unknown>;

export type TraitOptions = {
  readonly prefix?: string;
};

const noopClassDecorator: ClassDecorator = () => {};

/**
 * Marks a configurable trait. Introduce a trait only when fields need
 * per-implementer configuration, multiple traits must compose, and at least
 * two concrete schema types need the mechanism today.
 */
export function trait(_options: TraitOptions = {}): ClassDecorator {
  return noopClassDecorator;
}

/**
 * Heritage carrier for field-bearing traits: `implements Trait<SoftDeletable>`
 * contributes SoftDeletable's fields to the implementing type.
 *
 * TypeScript requires a class to re-declare every member of a type it
 * implements (TS2720), which contradicts trait flattening. Trait<T> resolves
 * to an empty object type, so the compiler imposes no member obligations,
 * while superschematic reads T as the implemented trait and flattens its fields.
 * Marker and config-only traits have no members and may be implemented bare.
 */
export type Trait<T> = NonNullable<unknown>;
