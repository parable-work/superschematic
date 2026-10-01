/*
@superschematic/engine/mcp serves an engine's MCP tools (D16): one tool
per operation of every live schema a namespace reaches, and tools that
list, describe and define schemas, at /namespaces/{namespace}/mcp over
MCP's streamable HTTP transport. It mounts through
@superschematic/http-runtime with the engine's other routes and speaks MCP
through @modelcontextprotocol/server; both, and Hono, are this entry
point's peer dependencies. See runtime/engine/README.md, "MCP".
*/

export { MCP_PATH, engineMcp, listTools } from './app.js';
export type { EngineMcpOptions } from './app.js';
