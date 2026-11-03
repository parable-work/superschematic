import { Contact, Embedding, Finance, Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import { AutoGenerate, User as UserTrait, UserRole, index, key, queue, seedRoles, unique } from "@superschematic/db";

// The user model (D50), which the API signs its users in with.
export abstract class User implements UserTrait<{ login: "email"; name: "name" }> {
  @key id: AutoGenerate<Identity.UUID>;
  @unique email: Contact.Email;
  name: Identity.Name;
  createdAt: Temporal.DateTime;
}

// An approved registrant holds member, which reads and writes at once
// (D88); the first registrant is the administrator.
@seedRoles([{ name: "member", permissions: ["smoke.read", "smoke.write"] }])
export abstract class Role implements UserRole {
  @key id: AutoGenerate<Identity.UUID>;
  @unique name: Identity.Slug;
  permissions: string[];
}

// One message waiting per line item, and one claim per line item at a
// time, as a finances API's sync queue holds one per Plaid item (D80).
@queue({ key: "lineItemId", onDuplicate: "skip", serialize: true, retries: 3, backoff: "2s", maxBackoff: "1m" })
export abstract class DrainLineItem {
  lineItemId: Identity.UUID;
}

// A line item, its name's embedding beside it, which an hnsw index finds
// the nearest of by cosine (D84). drainedAt is when the worker drained it.
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
