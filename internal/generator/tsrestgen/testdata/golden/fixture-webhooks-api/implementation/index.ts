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
