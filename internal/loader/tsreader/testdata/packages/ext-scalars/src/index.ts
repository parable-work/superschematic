// @fixture/ext-scalars stands in for the package an extension's scalar
// brands live in, as acme's live in @acme/schema. Ext.Doc is a JSON object
// scalar: the brand names it, and the catalog the extension registers gives
// it the Object primitive and the json_schema mapping object, and names this
// package as the one the Ext namespace is imported from.
export namespace Ext {
  export type Doc = Readonly<Record<string, unknown>> & { readonly __brand: "Ext.Doc" };
}
