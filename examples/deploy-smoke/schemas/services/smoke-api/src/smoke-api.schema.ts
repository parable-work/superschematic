import { Finance, Generic, Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import {
  Authenticated,
  HttpMethod,
  QueryParam,
  job,
  requirePermission,
  rest,
  source,
  userAdministration,
  userSessions,
  worker,
} from "@superschematic/api";
import { DrainLineItem, LineItem } from "@smoke/smoke-db";

// Registration waits for an administrator's approval; the first
// registrant is the administrator (D81, D88).
@userSessions({
  register: true,
  registration: "approval",
  defaultRoles: ["member"],
  firstUser: { role: "admin", permissions: ["smoke", "identity"] },
})
export abstract class Sessions {}

@userAdministration()
export abstract class Admin {}

@source(LineItem)
export abstract class LineItemView {
  id: Identity.UUID;
  name: string;
  amount: Finance.SignedMoney;
  drainedAt: Nullable<Temporal.DateTime>;
}

export abstract class CreateLineItemInput {
  name: Validate<string, { minLength: 1; maxLength: 200 }>;
  amount: Finance.SignedMoney;
}

export abstract class Totals {
  lines: Generic.Int64;
  spent: Finance.SignedMoney;
  earned: Finance.SignedMoney;
}

export abstract class Neighbour {
  id: Identity.UUID;
  name: string;
  distance: number;
}

export abstract class DrainRequested {
  id: Identity.UUID;
  enqueued: boolean;
}

export class LineItems extends Authenticated {
  // Writes the line item with its name's embedding.
  @rest(HttpMethod.POST, "line-items")
  @requirePermission(["smoke.write"])
  createLineItem(input: CreateLineItemInput): LineItemView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "line-items/{id}")
  @requirePermission(["smoke.read"])
  getLineItem(id: Identity.UUID): LineItemView {
    throw new Error("schema declaration only");
  }

  // Runs smoke-db's declared query lineItemTotals (D86).
  @rest(HttpMethod.GET, "totals")
  @requirePermission(["smoke.read"])
  totals(prefix: QueryParam<string>): Totals {
    throw new Error("schema declaration only");
  }

  // The line items nearest a name's embedding, by cosine (D84).
  @rest(HttpMethod.GET, "nearest")
  @requirePermission(["smoke.read"])
  nearest(name: QueryParam<string>, k: QueryParam<Generic.Int64>): Neighbour[] {
    throw new Error("schema declaration only");
  }

  // Enqueues DrainLineItem for the line item, in a transaction; the
  // worker DrainLineItems sets its drainedAt.
  @rest(HttpMethod.POST, "line-items/{id}/drain")
  @requirePermission(["smoke.write"])
  requestDrain(id: Identity.UUID): DrainRequested {
    throw new Error("schema declaration only");
  }
}

// Drains smoke-db's DrainLineItem: a Cloud Run worker pool on gcp, and on
// Cloudflare a Worker that drains it at each firing of a Cron Trigger
// (D92).
@worker({ queue: DrainLineItem, concurrency: 2 })
export abstract class DrainLineItems {}

// Named as a finances API names its own: its deployable,
// smoke-api-sync-all-linked-accounts, is 34 characters, which gcp
// shortens to its service account id's 30 (D97).
@job({ schedule: "0 4 * * *", timeout: "5m" })
export abstract class SyncAllLinkedAccounts {}
