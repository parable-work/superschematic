/*
@superschematic/engine/server is what a server on the Rust engine (D58)
opens and mounts, without the TypeScript engine: a generated TypeScript
server whose API's database is an engine database imports it alone.

- openEngineInBackground opens the engine off the JavaScript thread on
  its database, Postgres in a stack, retrying until the database answers,
  and runs a setup on it before it hands it out (EngineBackground): a
  server deploys its database's engine document, a runner starts the
  engine's runner.
- engineApp and engineMcp serve its routes, its event stream and its MCP
  endpoint on Hono, through the HTTP runtime: 503 unavailable until it
  opens. With an API's identity service and identityRoutes.sessions false
  they authenticate as the API's routes do, and the API serves the user
  model's routes; with its service authenticator a verified service stands
  in for an end user (D37).
- engineRouteSpecs are its routes under a mount, which a host's CORS
  matches beside its APIs' operations.

Its runtime imports are the addon's facade (src/native) and the HTTP
runtime; nothing of src/behaviors, src/registry or src/storage loads.
*/

export {
  DEFAULT_RETRY_MAX_MS,
  DEFAULT_RETRY_MIN_MS,
  EngineBackground,
  EngineNotOpenError,
  openEngineInBackground,
} from './background.js';
export type { BackgroundOptions } from './background.js';
export { AsyncNativeEngine, openEngineAsync } from '../native/async.js';
export type { DeployedSchema, DeployOptions, EngineHealth, Promised } from '../native/async.js';
export type { NativeEngineOptions, NativePostgresOptions } from '../native/options.js';
export {
  nativeEngineApp as engineApp,
  nativeEngineMcp as engineMcp,
  engineRouteSpecs,
  engineRoutes,
  engineUnavailable,
} from '../native/http.js';
export type { ServedEngine } from '../native/http.js';
export type { EngineRoute } from '../native/engine.js';
export type { EngineHttpOptions } from '../http/app.js';
export type { EngineMcpOptions } from '../mcp/app.js';
export type { Principal, PrincipalService } from '../access.js';
