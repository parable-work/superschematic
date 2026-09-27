// The config types of the fixture extension's behaviors, for tsc. The
// registry declares them (stock.behavior.json, audited.behavior.json) and
// checks every use; this augmentation of the core's BehaviorConfigs lets
// @behavior("acme.Stock", { aisles: 3 }) type-check. A General schema that
// does not import @acme/schematic lists this file in its tsconfig.json.
import "@superschematic/schema";

declare module "@superschematic/schema" {
  interface BehaviorConfigs {
    "acme.Stock": { readonly aisles: number; readonly unit?: string };
    "acme.Audited": undefined;
  }
}
