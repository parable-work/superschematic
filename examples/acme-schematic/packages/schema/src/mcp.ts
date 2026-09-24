// acme's invocation policy key on @mcp, for tsc. The acme extension
// registers confirm in place of the core's invocationPolicy
// (ext/mcp.go), and this augmentation of the core's MCPToolOptions lets
// @mcp({ handle, confirm: "always" }) type-check. The registry, not tsc,
// decides which key a build accepts: under acme the core key still
// type-checks and fails the load.
//
// A program sees the augmentation when it includes this file: every program
// that imports @acme/schema does, and an API service lists the file in its
// tsconfig.json, because an API schema cannot import @acme/schema (its
// decorators are for Catalog schemas).
import "@superschematic/api";

/** acme's invocation policy values: "never" (the default) runs the tool when a model calls it; "always" asks the person first. */
export type ConfirmPolicy = "never" | "always";

declare module "@superschematic/api" {
  interface MCPToolOptions {
    /** Whether a client asks the person before it runs the tool: "never" (the default) or "always". */
    readonly confirm?: ConfirmPolicy;
  }
}
