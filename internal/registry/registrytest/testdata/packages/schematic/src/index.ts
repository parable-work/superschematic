// @acme/schematic: the fixture extension's authoring package. psgen reads
// the AST, so every decorator is a no-op at runtime, as in the core packages.
const noopClassDecorator: ClassDecorator = () => {};
const noopPropertyDecorator: PropertyDecorator = () => {};

export interface ShelfArgs {
  readonly aisle: number;
  readonly bay?: string;
}

export function shelf(_args: ShelfArgs): PropertyDecorator {
  return noopPropertyDecorator;
}

export const tagged: ClassDecorator = noopClassDecorator;

export function meta(_args: Record<string, unknown>): ClassDecorator {
  return noopClassDecorator;
}

export const audited: ClassDecorator & MethodDecorator = () => {};
