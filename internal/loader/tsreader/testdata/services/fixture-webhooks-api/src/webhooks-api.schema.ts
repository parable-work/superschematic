import {
  HttpMethod,
  hmacVerified,
  manualRouteRegistration,
  publicRoute,
  rateLimit,
  requirePermission,
  rest,
  webhook
} from "@superschematic/api";

// An event a payment provider posts.
export abstract class PaymentEvent {
  id: string;
  type: string;
}

// An event the service has received.
export abstract class EventReceipt {
  id: string;
  received: boolean;
}

export class WebhookMutations {
  // A Stripe event. Its signature is checked before the rate limit.
  @webhook
  @publicRoute
  @hmacVerified({ provider: "stripe" })
  @rateLimit({ requestsPerMinute: 1 })
  @rest(HttpMethod.POST, "webhooks/stripe")
  receiveStripeEvent(event: PaymentEvent): EventReceipt {
    throw new Error("schema declaration only");
  }

  // A GitHub event. Its signature is checked before the permission.
  @webhook
  @hmacVerified({ provider: "github" })
  @requirePermission(["webhooks.receive"])
  @rest(HttpMethod.POST, "webhooks/github")
  receiveGithubEvent(id: string, action: string): EventReceipt {
    throw new Error("schema declaration only");
  }

  // A GitHub event the service's own route reads.
  @webhook
  @hmacVerified({ provider: "github" })
  @manualRouteRegistration
  @rest(HttpMethod.POST, "webhooks/github/raw")
  receiveRawGithubEvent(payload: string): boolean {
    throw new Error("schema declaration only");
  }
}

export class EventQueries {
  // A received event. Not a webhook, so no verifier runs.
  @rest(HttpMethod.GET, "events/{id}")
  getEvent(id: string): EventReceipt {
    throw new Error("schema declaration only");
  }
}
