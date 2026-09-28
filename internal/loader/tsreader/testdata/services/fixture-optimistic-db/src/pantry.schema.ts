import { Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Relation, key, optimistic } from "@superschematic/db";

// A shelf of the pantry. A delete sets deletedAt.
@optimistic
export abstract class Shelf {
  @key
  id: AutoGenerate<Identity.UUID>;
  label: string;
  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
  updatedAt: Temporal.DateTime;
  updatedBy: Identity.UUID;
  deletedAt: Nullable<Temporal.DateTime>;
  deletedBy: Nullable<Identity.UUID>;
}

// How much of one ingredient a shelf holds. A delete removes the row.
@optimistic
export abstract class Stock {
  @key
  id: AutoGenerate<Identity.UUID>;
  shelf: Relation<Shelf>;
  ingredient: string;
  quantity: Generic.Int64;
}
