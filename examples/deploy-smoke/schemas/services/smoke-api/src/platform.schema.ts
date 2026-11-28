import { Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import { Authenticated, HttpMethod, behaviorWorker, requirePermission, rest, worker } from "@superschematic/api";
import { envVars, providers } from "@superschematic/schema-config";
import { Bill, NoticeWork, RunRequest, SearchIndexing } from "@smoke/smoke-db";
import { TitleInput } from "./smoke-api.schema";

// smoke-api's config (D159): SMS is a channel slot, which each environment
// fills with the stub relay (providers/stub-relay). The notifier sends on
// it and the inbound path reads what it receives; an environment that
// fills none is refused, since smoke-db raises events (D153).
@envVars
@providers({ SMS: "channel" })
export abstract class SmokeApiConfig {
  SMS: Nullable<string>;
}

export abstract class BillView {
  id: Identity.UUID;
  title: string;
  nextDueAt: Nullable<Temporal.DateTime>;
}

export abstract class OccurrenceView {
  key: string;
  state: string;
  dueAt: Temporal.DateTime;
  remindAt: Nullable<Temporal.DateTime>;
  remindedAt: Nullable<Temporal.DateTime>;
}

// A bill paid each day, first due a short while on, whose occurrence
// reminds twenty seconds before (D152): the behavior worker BillReminders
// fires the remind deadline and hands its event to the notifier.
export class Bills extends Authenticated {
  // The caller's bill, due daily from a short while on.
  @rest(HttpMethod.POST, "bills")
  @requirePermission(["smoke.write"])
  createBill(input: TitleInput): BillView {
    throw new Error("schema declaration only");
  }

  // Its occurrences, each with when it reminds and when it reminded.
  @rest(HttpMethod.GET, "bills/{id}/occurrences")
  @requirePermission(["smoke.read"])
  occurrences(id: Identity.UUID): OccurrenceView[] {
    throw new Error("schema declaration only");
  }
}

export abstract class RouteWritten {
  id: Identity.UUID;
  name: string;
}

export abstract class PhoneInput {
  // E.164: a plus and up to fifteen digits.
  phone: Validate<string, { pattern: "^\\+[1-9][0-9]{6,14}$" }>;
}

export abstract class Reachable {
  identityId: Identity.UUID;
}

export abstract class DeliveryView {
  channelKind: string;
  state: string;
  providerMessageId: Nullable<string>;
}

// The notifier's rows the smoke writes (D153): routes and preferences are
// rows, so the check writes its own; the inbox is the generated server's
// GET /api/inbox.
export class Notices extends Authenticated {
  // The route of a bill's reminder to its owner, written once.
  @rest(HttpMethod.PUT, "notifications/routes/bill-reminded")
  @requirePermission(["smoke.routes"])
  writeBillRoute(): RouteWritten {
    throw new Error("schema declaration only");
  }

  // The caller's phone, and its preference that a notice goes by SMS as
  // well as to the in-app inbox.
  @rest(HttpMethod.PUT, "notifications/reach")
  @requirePermission(["smoke.write"])
  reachMe(input: PhoneInput): Reachable {
    throw new Error("schema declaration only");
  }

  // How a notice of the caller's went out on each channel.
  @rest(HttpMethod.GET, "notifications/notices/{id}/deliveries")
  @requirePermission(["smoke.read"])
  deliveries(id: Identity.UUID): DeliveryView[] {
    throw new Error("schema declaration only");
  }
}

export abstract class MemoInput {
  title: Validate<string, { minLength: 1; maxLength: 200 }>;
  text: Nullable<string>;
}

export abstract class MemoView {
  id: Identity.UUID;
  title: string;
  text: Nullable<string>;
}

// Memos, which search finds across tables (D155): the generated server's
// GET /api/search, over the index the worker IndexSearch keeps current.
export class Memos extends Authenticated {
  @rest(HttpMethod.POST, "memos")
  @requirePermission(["smoke.write"])
  createMemo(input: MemoInput): MemoView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.PUT, "memos/{id}")
  @requirePermission(["smoke.write"])
  updateMemo(id: Identity.UUID, input: MemoInput): MemoView {
    throw new Error("schema declaration only");
  }
}

export abstract class PayeeInput {
  name: Validate<string, { minLength: 1; maxLength: 200 }>;
  phone: Validate<string, { pattern: "^\\+[1-9][0-9]{6,14}$" }>;
  // The line item its grant names.
  lineItemId: Identity.UUID;
}

export abstract class PayeeView {
  id: Identity.UUID;
  grantId: Identity.UUID;
}

export abstract class TriagedView {
  id: Identity.UUID;
  via: string;
  body: string;
}

// The inbound path's staff side (D157): a payee with a phone and a grant
// on one line item, and the triage of what came from no one the
// deployment knows. The stub relay's callback is the generated server's
// POST /webhooks/sms/inbound.
export class Payees extends Authenticated {
  @rest(HttpMethod.POST, "payees")
  @requirePermission(["smoke.parties"])
  createPayee(input: PayeeInput): PayeeView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "triage")
  @requirePermission(["smoke.triage"])
  triage(): TriagedView[] {
    throw new Error("schema declaration only");
  }
}

// Fires each bill's open, remind, overdue and plan deadlines, and hands its
// reminder to the notifier (D152, D153).
@behaviorWorker(Bill, { permissions: ["smoke.write"] })
export abstract class BillReminders {}

// Runs the notifier: routes each event and sends each delivery (D153). The
// generated server hands each message to the notifier.
@worker({ queue: NoticeWork, concurrency: 2 })
export abstract class Notify {}

// Keeps the search index current from each write that changed a memo's
// text (D155). The generated server hands each message to indexSearch.
@worker({ queue: SearchIndexing, concurrency: 2 })
export abstract class IndexSearch {}

// Takes each run request a payee's text made: as the payee, through its
// grant, files the message onto the line item the grant names (D157).
@worker({ queue: RunRequest, concurrency: 2 })
export abstract class Runs {}
