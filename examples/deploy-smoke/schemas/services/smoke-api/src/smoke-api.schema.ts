import { Finance, Generic, Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import {
  Authenticated,
  HttpMethod,
  QueryParam,
  behaviorWorker,
  job,
  requirePermission,
  rest,
  source,
  userAdministration,
  userSessions,
  worker,
} from "@superschematic/api";
import { Alert, CheckIn, DrainLineItem, FileReleased, FollowUp, LineItem } from "@smoke/smoke-db";

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

// A file to upload to a line item: what the client declares, which the
// upload's completion holds its bytes to (D154).
export abstract class FileInput {
  name: Validate<string, { minLength: 1; maxLength: 255 }>;
  mediaType: string;
  size: Generic.Int64;
  sha256: Validate<string, { pattern: "^[0-9a-f]{64}$" }>;
}

// Where the client PUTs the file's bytes, with which content type.
export abstract class FileUpload {
  fileId: Identity.UUID;
  url: string;
  contentType: string;
}

export abstract class FileView {
  id: Identity.UUID;
  lineItemId: Identity.UUID;
  name: string;
  size: Generic.Int64;
  contentHash: string;
  objectKey: string;
  state: string;
}

export abstract class FileOnto {
  lineItemId: Identity.UUID;
}

export abstract class Backlink {
  lineItemId: Identity.UUID;
  role: string;
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

  // Enqueues DrainLineItem due five seconds on (D80): on Cloudflare the
  // enqueue wakes the worker's Workflow for that time (D151).
  @rest(HttpMethod.POST, "line-items/{id}/drain-later")
  @requirePermission(["smoke.write"])
  requestDelayedDrain(id: Identity.UUID): DrainRequested {
    throw new Error("schema declaration only");
  }

  // Begins a file's upload to the line item: a URL the client PUTs its
  // bytes to, straight to the bucket (D154).
  @rest(HttpMethod.POST, "line-items/{id}/files")
  @requirePermission(["smoke.write"])
  beginFile(id: Identity.UUID, input: FileInput): FileUpload {
    throw new Error("schema declaration only");
  }

  // Completes it: the bytes read back from the bucket, held to the size
  // and SHA-256 the upload declared.
  @rest(HttpMethod.POST, "line-items/{id}/files/{fileId}/complete")
  @requirePermission(["smoke.write"])
  completeFile(id: Identity.UUID, fileId: Identity.UUID): FileView {
    throw new Error("schema declaration only");
  }

  // Files the file onto another line item, with no second upload; the
  // line item then points at it.
  @rest(HttpMethod.POST, "line-items/{id}/files/{fileId}/file")
  @requirePermission(["smoke.write"])
  fileFile(id: Identity.UUID, fileId: Identity.UUID, input: FileOnto): FileView {
    throw new Error("schema declaration only");
  }

  // The line items that point at this one.
  @rest(HttpMethod.GET, "line-items/{id}/backlinks")
  @requirePermission(["smoke.read"])
  backlinks(id: Identity.UUID): Backlink[] {
    throw new Error("schema declaration only");
  }
}

export abstract class TitleInput {
  title: Validate<string, { minLength: 1; maxLength: 200 }>;
}

export abstract class FollowUpView {
  id: Identity.UUID;
  title: string;
  status: string;
}

export abstract class CheckInView {
  id: Identity.UUID;
  title: string;
  ranAt: Nullable<Temporal.DateTime>;
}

export abstract class AlertView {
  id: Identity.UUID;
  level: string;
  status: string;
}

// The behaviors' rows (D151): each one's behavior worker moves it, under
// stack dev as a process and on Cloudflare at a Cron firing or a wake.
export class Behaviors extends Authenticated {
  // A follow-up, due five seconds on by its Timers deadline.
  @rest(HttpMethod.POST, "follow-ups")
  @requirePermission(["smoke.write"])
  createFollowUp(input: TitleInput): FollowUpView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "follow-ups/{id}")
  @requirePermission(["smoke.read"])
  getFollowUp(id: Identity.UUID): FollowUpView {
    throw new Error("schema declaration only");
  }

  // A check-in with a wake-up five seconds on, by its Schedules.
  @rest(HttpMethod.POST, "check-ins")
  @requirePermission(["smoke.write"])
  createCheckIn(input: TitleInput): CheckInView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "check-ins/{id}")
  @requirePermission(["smoke.read"])
  getCheckIn(id: Identity.UUID): CheckInView {
    throw new Error("schema declaration only");
  }

  // A quiet alert.
  @rest(HttpMethod.POST, "alerts")
  @requirePermission(["smoke.write"])
  createAlert(): AlertView {
    throw new Error("schema declaration only");
  }

  // Sets the alert's level to high, which its Reactions hears.
  @rest(HttpMethod.POST, "alerts/{id}/high")
  @requirePermission(["smoke.write"])
  raiseAlert(id: Identity.UUID): AlertView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "alerts/{id}")
  @requirePermission(["smoke.read"])
  getAlert(id: Identity.UUID): AlertView {
    throw new Error("schema declaration only");
  }
}

// Runs FollowUp's deadlines and the writes its Timers hears (D103, D151).
@behaviorWorker(FollowUp)
export abstract class FollowUpTimers {}

// Runs CheckIn's wake-ups and hands each run to checkInWakeUpsSchedulesRun
// (D104, D151).
@behaviorWorker(CheckIn, { permissions: ["smoke.write"] })
export abstract class CheckInWakeUps {}

// Runs the rule Alert's Reactions holds over its writes (D103, D151).
@behaviorWorker(Alert)
export abstract class AlertReactions {}

// Drains smoke-db's DrainLineItem: a Cloud Run worker pool on gcp, and on
// Cloudflare a Worker that drains it at each firing of a Cron Trigger
// (D92).
@worker({ queue: DrainLineItem, concurrency: 2 })
export abstract class DrainLineItems {}

// Deletes the object a line item's file named once no file names it
// (D154): a worker of FileReleased, which a file's delete enqueues on.
@worker({ queue: FileReleased })
export abstract class ReleaseFiles {}

// Named as a finances API names its own: its deployable,
// smoke-api-sync-all-linked-accounts, is 34 characters, which gcp
// shortens to its service account id's 30 (D97).
@job({ schedule: "0 4 * * *", timeout: "5m" })
export abstract class SyncAllLinkedAccounts {}
