import { Contact, Embedding, Finance, Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import { AutoGenerate, Reactions, Schedules, Timers, User as UserTrait, UserRole, Workflow, attachments, index, key, queue, references, seedRoles, unique } from "@superschematic/db";

// The user model (D50), which the API signs its users in with.
export abstract class User implements UserTrait<{ login: "email"; name: "name" }> {
  @key id: AutoGenerate<Identity.UUID>;
  @unique email: Contact.Email;
  name: Identity.Name;
  createdAt: Temporal.DateTime;
}

// An approved registrant holds member, which reads and writes at once
// (D88); the first registrant is the administrator. A payee's grant holds
// payee, which reads the line item it names (D156, D157).
@seedRoles([
  { name: "member", permissions: ["smoke.read", "smoke.write"] },
  { name: "payee", permissions: ["smoke.read"] },
])
export abstract class Role implements UserRole {
  @key id: AutoGenerate<Identity.UUID>;
  @unique name: Identity.Slug;
  permissions: string[];
  // The rows a grant of the role reaches from its target; none here.
  reaches: Nullable<string[]>;
}

// One message waiting per line item, and one claim per line item at a
// time, as a finances API's sync queue holds one per Plaid item (D80).
@queue({ key: "lineItemId", onDuplicate: "skip", serialize: true, retries: 3, backoff: "2s", maxBackoff: "1m" })
export abstract class DrainLineItem {
  lineItemId: Identity.UUID;
}

// An object a line item's file named, released once its file is gone or
// its upload expired (D154); the build fills its fields, and the worker
// ReleaseFiles deletes the object once no file names it.
@queue()
export abstract class FileReleased {}

// A line item, its name's embedding beside it, which an hnsw index finds
// the nearest of by cosine (D84). drainedAt is when the worker drained it.
// It holds files of at most 1 MiB, which a client uploads straight to the
// bucket, and points at other line items, as a file filed onto one does
// (D154).
@references<LineItem>({ tables: [LineItem] })
@attachments<LineItem>({ released: FileReleased, maxFileBytes: 1048576 })
@index<LineItem>(["name"])
@index<LineItem>(["embedding"], { method: "hnsw", ops: "vector_cosine_ops" })
export abstract class LineItem {
  @key id: AutoGenerate<Identity.UUID>;
  createdAt: Temporal.DateTime;
  name: Validate<string, { minLength: 1; maxLength: 200 }>;
  // Signed minor units, a debit negative.
  amount: Finance.SignedMoney;
  embedding: Validate<Embedding.Vector, { dimensions: 3 }>;
  drainedAt: Nullable<Temporal.DateTime>;
}

// The behaviors a behavior worker runs on Workers (D151), each on a table
// of its own, each of which smoke-api runs a @behaviorWorker of.

// A follow-up falls due five seconds after it is made: a Timers deadline,
// which the behavior worker fires, moves it to due. On Cloudflare the
// create wakes the worker's Workflow, so it moves within seconds of its
// time, not at the next Cron firing.
export abstract class FollowUp implements Timers<FollowUp, {
  deadlines: { due: { start: { create: true }; afterMs: 5000; then: { transition: "due" } } };
  permission: "smoke.write";
}> {
  @key id: AutoGenerate<Identity.UUID>;
  title: Validate<string, { minLength: 1; maxLength: 200 }>;
  status: Workflow<{ states: ["open", "due"]; transitions: [{ from: "open"; to: "due" }] }>;
}

// A check-in takes a wake-up five seconds on (Schedules): its run is an
// event the behavior worker hands to smoke-api's handler, which records
// when it ran.
export abstract class CheckIn implements Schedules<CheckIn, {
  max: 2;
  minMs: 1000;
  kinds: ["after"];
  outcomes: ["told"];
  permission: "smoke.write";
}> {
  @key id: AutoGenerate<Identity.UUID>;
  title: Validate<string, { minLength: 1; maxLength: 200 }>;
  ranAt: Nullable<Temporal.DateTime>;
}

// An alert is raised when its level changes to high (Reactions): the
// table's trigger writes the change, and the behavior worker runs the rule
// that hears it, whoever wrote the row.
export abstract class Alert implements Reactions<Alert, {
  rules: [{ when: { changes: { field: "level"; to: "high" } }; then: { transition: "raised" } }];
}> {
  @key id: AutoGenerate<Identity.UUID>;
  level: Validate<string, { minLength: 1; maxLength: 20 }>;
  status: Workflow<{ states: ["quiet", "raised"]; transitions: [{ from: "quiet"; to: "raised" }] }>;
}
