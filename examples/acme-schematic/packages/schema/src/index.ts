// @acme/schema: the authoring package of the acme-schematic extension.
//
// superschematic reads the schema's AST, so a decorator is a no-op at run
// time, as in the core packages. The types exist so an author gets
// completion and tsc rejects a bad argument before superschematic does; the
// registry validates the same argument against ShelfArgs (ext/decorator.go)
// in both the TypeScript and the data form.

export type { ConfirmPolicy } from "./mcp";
export type { RatingConfig } from "./behaviors";

// The scalars the acme extension adds to the core set. The brand names the
// scalar; the catalog the extension registers (ext/scalars.go) gives its
// metadata, and declares Acme.Photo a file upload.
export namespace Acme {
  // A product photo, uploaded as a multipart file part.
  export type Photo = string & { readonly __brand: "Acme.Photo" };
}

// ShelfArgs is @shelf's argument: where a field's values are stocked.
export interface ShelfArgs {
  readonly aisle: number;
  readonly bay?: string;
}

// shelf marks a field of a Catalog schema with its shelf location.
export function shelf(_args: ShelfArgs): PropertyDecorator {
  return () => {};
}

// feedKey marks a field of a Catalog schema as part of the key acme's
// supplier feed matches rows on. It takes no argument.
export const feedKey: PropertyDecorator = () => {};

// SchemaClass is a schema class named as a value.
export type SchemaClass = abstract new (...args: never[]) => unknown;

// CrossSellArgs is @crossSell's argument: with names the class whose
// listing a type is offered beside, the class itself and not its name.
export interface CrossSellArgs {
  readonly with: SchemaClass;
}

// crossSell offers a type of a Catalog schema beside another class's
// listing.
export function crossSell(_args: CrossSellArgs): ClassDecorator {
  return () => {};
}
