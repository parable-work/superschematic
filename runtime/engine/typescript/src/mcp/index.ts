/*
@superschematic/engine/mcp serves an engine's MCP tools (D16): one tool
per operation of every live schema a namespace reaches, and tools that
list, describe and define schemas, at /namespaces/{namespace}/mcp over
MCP's streamable HTTP transport, which the engine speaks itself (D58). It
mounts through @superschematic/http-runtime with the engine's other
routes; Hono is this entry point's peer dependency. See
runtime/engine/README.md, "MCP".
*/

export { MCP_PATH, engineMcp } from './app.js';
export type { EngineMcpOptions } from './app.js';
