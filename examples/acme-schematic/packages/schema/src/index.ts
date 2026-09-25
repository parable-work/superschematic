// @acme/schema: the authoring package of the acme-schematic extension.
//
// superschematic reads the schema's AST, so a decorator is a no-op at run
// time, as in the core packages. The types exist so an author gets
// completion and tsc rejects a bad argument before superschematic does; the
// registry validates the same argument against ShelfArgs (ext/decorator.go)
// in both the TypeScript and the data form.

export type { ConfirmPolicy } from "./mcp";

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
