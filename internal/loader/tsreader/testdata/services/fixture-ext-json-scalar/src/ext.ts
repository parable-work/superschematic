// Ext.Doc stands in for a JSON object scalar an extension's scalar package
// defines: the brand names it, and the catalog the extension registers gives
// it the Object primitive and the json_schema mapping object.
export namespace Ext {
  export type Doc = Readonly<Record<string, unknown>> & { readonly __brand: "Ext.Doc" };
}
