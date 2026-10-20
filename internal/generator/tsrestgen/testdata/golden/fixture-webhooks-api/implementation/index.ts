// The implementation of the fixture-webhooks-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented, unauthorized } from '@superschematic/http-runtime';
import type {
  AuthenticatorFactory,
  Constructor,
  Deps,
  ManualRoutesConstructor,
  EventImplementation,
  WebhookImplementation,
} from '@schemas/fixture-webhooks-api-api';

/** Builds the implementation of fixture-webhooks-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  event: eventImplementation(deps),
  webhook: webhookImplementation(deps),
  // The verifier of each @hmacVerified provider, which checks the
  // signature of its requests. The scaffold's refuses every request.
  webhookVerifiers: {
    'github': async () => {
      throw unauthorized('fixture-webhooks-api verifies no github signature yet');
    },
    'stripe': async () => {
      throw unauthorized('fixture-webhooks-api verifies no stripe signature yet');
    },
  },
});

/**
 * Builds the authenticator that establishes the end user on the routes
 * that require one. The scaffold's establishes none, so each such route
 * answers 401: verify the request's credential (ctx.bearerToken, or a
 * header) with your identity provider and return its Principal.
 */
export const authenticate: AuthenticatorFactory = () => async () => null;

/**
 * Builds the handlers of the @manualRouteRegistration operations of
 * fixture-webhooks-api from its dependencies. Its type is the generated
 * ManualRoutesConstructor. The router runs each route's auth gate, then
 * hands its handler the Hono context and the RequestContext, and sends the
 * Response the handler returns.
 */
export const createManualRoutes: ManualRoutesConstructor = deps => ({
  async receiveRawGithubEvent() {
    throw notImplemented('webhook.receiveRawGithubEvent');
  },
});

function eventImplementation(deps: Deps): EventImplementation {
  return {
    async getEvent() {
      throw notImplemented('event.getEvent');
    },
  };
}

function webhookImplementation(deps: Deps): WebhookImplementation {
  return {
    async receiveGithubEvent() {
      throw notImplemented('webhook.receiveGithubEvent');
    },
    async receiveStripeEvent() {
      throw notImplemented('webhook.receiveStripeEvent');
    },
  };
}
