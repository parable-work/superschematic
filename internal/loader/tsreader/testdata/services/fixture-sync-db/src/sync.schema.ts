import { Finance, Generic, Geo, Identity, Temporal } from "superscalar";
import { Default, Nullable } from "@superschematic/schema";
import { AutoGenerate, enqueueOnChange, index, key, queue, Relation, unique } from "@superschematic/db";

// A bank account a sync books its lines to.
export abstract class BankAccount {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  code: string;

  name: string;
}

// Sent on every write of a line item, whose embedding is computed again:
// every write adds one, so an upsert that leaves a line as it was adds none.
@queue({ retries: 3 })
export abstract class LineItemWritten {
  lineItemId: Identity.UUID;
}

// A transaction a bank sync reads, keyed by the bank's id for it, which a
// line typed in by hand lacks, and by its account and reference. Whatever
// writes it, an insert or an update enqueues LineItemWritten.
@index<LineItem>(["plaidTransactionId"], { unique: true, name: "plaid", where: "plaid_transaction_id IS NOT NULL" })
@index<LineItem>(["account", "reference"], { unique: true, name: "reference" })
@enqueueOnChange<LineItemWritten, LineItem>(LineItemWritten, {
  on: ["insert", "update"],
  message: { lineItemId: "id" },
})
export abstract class LineItem {
  @key
  id: AutoGenerate<Identity.UUID>;

  plaidTransactionId: Nullable<string>;

  account: Relation<BankAccount>;

  reference: string;

  name: string;

  amount: Finance.SignedMoney;

  postedAt: Temporal.DateTime;

  pending: Default<boolean, false>;

  // Where the bank says it was paid: a POINT, which an upsert only if
  // changed compares as its text.
  location: Nullable<Geo.Location>;

  details: Nullable<Generic.JSON>;

  tags: string[];

  createdAt: Temporal.DateTime;

  updatedAt: Temporal.DateTime;

  updatedBy: Identity.UUID;

  deletedAt: Nullable<Temporal.DateTime>;
}
