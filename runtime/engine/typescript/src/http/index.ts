/*
@superschematic/engine/http serves an engine over HTTP (D16): fixed routes
that carry the namespace and the schema name as path parameters, mounted
through @superschematic/http-runtime with its envelopes, authentication
gate, body limit, rate limit and timeout, and the event log as JSON pages
or server-sent events. Hono and the HTTP runtime are this entry point's
peer dependencies; the engine's main entry point imports neither. See
runtime/engine/README.md, "HTTP".
*/

export { JSON_MEDIA_TYPE, MERGE_PATCH_MEDIA_TYPE, PRECONDITIONS_HEADER, engineApp } from './app.js';
export type { EngineHttpOptions } from './app.js';
export { ENGINE_ERROR_STATUS, engineProblem } from './problems.js';
export { DEFAULT_HEARTBEAT_MS, DEFAULT_STREAM_PAGE_SIZE } from './stream.js';
export type { StreamOptions } from './stream.js';
