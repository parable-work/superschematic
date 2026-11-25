import { Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Mergeable, Relation, Workflow, index, key, unique } from "@superschematic/db";

// A label people put on documents. Two labels that turn out to be one are
// merged (D147): everything that pointed at one points at the other. A
// label may sit under another. A delete sets deletedAt.
export abstract class Label implements Mergeable {
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
  parent: Nullable<Relation<Label, { onDelete: "SET NULL" }>>;
  deletedAt: Nullable<Temporal.DateTime>;
  deletedBy: Nullable<Identity.UUID>;
}

// A document labels go on.
export abstract class Document {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}

// A label on a document, once per document: a merge of two labels a
// document carries deletes the one that pointed at the label merged away.
@index<DocumentLabel>(["document", "label"], { unique: true })
export abstract class DocumentLabel {
  @key
  id: AutoGenerate<Identity.UUID>;
  document: Relation<Document>;
  label: Relation<Label>;
}

// A person following a label, once while the row is live. A delete sets
// deletedAt, so a merge that finds a person following both labels stamps
// the one that pointed at the label merged away.
@index<Subscription>(["subscriber", "label"], { unique: true })
export abstract class Subscription {
  @key
  id: AutoGenerate<Identity.UUID>;
  subscriber: string;
  label: Relation<Label>;
  deletedAt: Nullable<Temporal.DateTime>;
  deletedBy: Nullable<Identity.UUID>;
}

// A note about a label, which says who changed it last and when: a merge
// stamps both on the notes it repoints.
export abstract class LabelNote {
  @key
  id: AutoGenerate<Identity.UUID>;
  label: Relation<Label>;
  text: string;
  updatedAt: Temporal.DateTime;
  updatedBy: Nullable<Identity.UUID>;
}

// A label's code in another system, once per label without regard to case:
// its unique index is over an expression that reads the relation's column.
@index<LabelCode>(["label", "code"], { unique: true, name: "label_code_lower", expression: "(CAST(label_id AS text) || ':' || lower(code))" })
export abstract class LabelCode {
  @key
  id: AutoGenerate<Identity.UUID>;
  label: Relation<Label>;
  code: string;
}

// A customer, merged when two records are one person. Its status is a
// Workflow, so its rows are deleted through the behaviors' core, and a
// delete removes the row.
export abstract class Customer implements Mergeable {
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
  status: Workflow<{
    states: ["active", "closed"];
    transitions: [{ from: "active"; to: "closed" }];
  }>;
}

// One profile per customer: a merge keeps the profile of the customer that
// stays.
export abstract class CustomerProfile {
  @key
  id: AutoGenerate<Identity.UUID>;
  @unique
  customer: Relation<Customer>;
  bio: string;
}

// An order of a customer.
export abstract class Purchase {
  @key
  id: AutoGenerate<Identity.UUID>;
  customer: Relation<Customer>;
  total: number;
}
