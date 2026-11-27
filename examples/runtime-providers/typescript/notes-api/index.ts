// The implementation of the notes-api API: the scaffold the stack's build
// writes where the naming file's [implementation_paths] typescript template
// puts it, committed so that a build finds it and writes nothing into the
// example. The example runs no server: test/providers.test.ts drives the
// providers without one.

import type { AuthenticatorFactory, Constructor } from '@notes/notes-api-api';

/** notes-api's one route, getNote, is a @tableOperation, which the build serves, so the implementation has no method. */
export const create: Constructor = () => ({});

/**
 * Builds the authenticator that establishes the end user on the routes
 * that require one. The scaffold's establishes none, so getNote answers
 * 401: verify the request's credential (ctx.bearerToken, or a header) with
 * your identity provider and return its Principal.
 */
export const authenticate: AuthenticatorFactory = () => async () => null;
