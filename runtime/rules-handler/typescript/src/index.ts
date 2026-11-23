// @superschematic/rules-handler: development mode's request handler (D117,
// D103's step 6) for a server written by hand. The handler
// (runtime/engine/rust/rules-handler) is built for wasm32-unknown-unknown
// (runtime/engine/rust/rules-handler-wasm) and driven over its JSON ABI
// (./wasm.ts); the host loop (./host.ts) answers its reads from a store and
// applies its units; the development layout over SQLite (./store.ts) is the
// tables superschematic-rules-dev keeps; and the development host
// (./dev.ts) serves a typed request, the routes over the Fetch API, the
// event stream and the tools.
//
// This entry imports nothing, so it runs in Bun, Node, Workers and a
// browser over any store with a synchronous SQLite connection;
// @superschematic/rules-handler/sqlite opens one on bun:sqlite.

export type * from './contract.js';
export { encode, raw, RawJson, JSON_CODEC, type Codec } from './json.js';
export { loadHandler, loadHandlerSync, HandlerError, WASM_FILE, type HandlerErrorCode, type HandlerModule, type Operation, type WasmSource } from './wasm.js';
export { serve, runDue, refusalOf, RefusalError, type Fresh, type Outcome, type Ran, type ServeOptions, type Store } from './host.js';
export { SqliteStore, LAYOUT_VERSION, type SqlValue, type SqliteConnection, type SqliteStatement } from './store.js';
export {
  Dev,
  devCallers,
  responseOf,
  BODY_LIMIT,
  MCP_BODY_LIMIT,
  PERMISSIONS_HEADER,
  SUBJECT_HEADER,
  type Authenticate,
  type DevMcp,
  type DevOptions,
  type McpCallOutcome,
  type McpTool,
  type McpToolSet,
  type ResponseOf,
  type Responses,
  type Row,
  type ToolEntry,
} from './dev.js';
