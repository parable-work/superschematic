import { allowUnknownFields, Nullable, strictJSON } from "@superschematic/schema";
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

// A Plaid item event. A provider adds keys to its payloads over time, so a
// webhook's input drops the keys its type does not declare (D87).
export abstract class PlaidItemEvent {
  webhook_type: string;
  webhook_code: string;
  item_id: string;
  error: Nullable<PlaidError>;
}

// The error a Plaid event may carry, which drops the keys it does not
// declare wherever it is decoded.
@allowUnknownFields
export abstract class PlaidError {
  error_code: string;
}

// A ledger event, a contract closed to new keys: @strictJSON keeps a
// webhook's input refusing a key its type does not declare.
@strictJSON
export abstract class LedgerEvent {
  id: string;
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

  // A Plaid event, unsigned in this fixture.
  @webhook
  @publicRoute
  @rest(HttpMethod.POST, "webhooks/plaid")
  receivePlaidEvent(event: PlaidItemEvent): EventReceipt {
    throw new Error("schema declaration only");
  }

  // A ledger event, whose type refuses the keys it does not declare.
  @webhook
  @publicRoute
  @rest(HttpMethod.POST, "webhooks/ledger")
  receiveLedgerEvent(event: LedgerEvent): EventReceipt {
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
