/*
@superschematic/engine/http serves an engine over HTTP (D16): fixed routes
that carry the namespace and the schema name as path parameters, which
the engine serves itself (D58), mounted through
@superschematic/http-runtime with its request ids, authentication gate,
service step, body limit, rate limit and timeout, the event log as JSON
pages or server-sent events, and the behaviors the engine runs. Hono is
this entry point's peer dependency. With the core user model
(D50), engineIdentity builds the identity service engineApp and engineMcp
take, whose capabilities answer per live schema and action. See
runtime/engine/README.md, "HTTP".
*/

export { ACTING_FOR_HEADER, JSON_MEDIA_TYPE, MERGE_PATCH_MEDIA_TYPE, PRECONDITIONS_HEADER, engineApp } from './app.js';
export type { ActingForOption, EngineHttpOptions } from './app.js';
export type { ServedEngine } from '../native/http.js';
export { ENGINE_ERROR_STATUS, engineProblem } from './problems.js';
export { engineCapabilities, engineIdentity, engineIdentityStore } from './identity.js';
export { DEFAULT_HEARTBEAT_MS, DEFAULT_STREAM_PAGE_SIZE, READY_EVENT } from './stream.js';
export type { StreamOptions } from './stream.js';
