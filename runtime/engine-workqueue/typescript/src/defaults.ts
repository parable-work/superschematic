/*
Lease's defaults, in a module that imports nothing, so the worker
(worker/), which must not load the engine's server side, reads the same
values the behavior applies.
*/

/** How long a lease lasts after its acquire or its last heartbeat when the config gives no ttlMs. */
export const DEFAULT_TTL_MS = 60000;
