import { Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable, fenced } from "@superschematic/schema";
import { AutoGenerate, Relation, key, unique } from "@superschematic/db";

// Who carries a delivery. It has no fence: a write of it advances nothing.
export abstract class Courier {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;
}

// A parcel at the desk. Its one fence lists no covers, so every update of
// a row advances version by one, whatever it writes: a writer that read
// version 4 writes where version is 4, and knows no other write came
// between. An upsert by trackingCode updates a row the same way.
@fenced<Parcel>({ field: "version" })
export abstract class Parcel {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  trackingCode: string;

  weightGrams: Generic.Int64;

  note: Nullable<string>;

  version: Default<Generic.Int64, 0>;
}

// A delivery two parties write, neither waiting on the other. The planner
// writes the address and the courier, a relation, covered as the key it
// stores, and advances planVersion; the courier's device writes attempts
// and proof, and advances runVersion. A write of one party's fields leaves
// the other's counter as it was, so neither fails the other's compare.
// No fence covers note or updatedAt: a write of them alone advances
// neither counter. The courier is nullable, set and cleared by the
// planner's writes alone: its delete action is RESTRICT, so a courier a
// delivery names is not deleted. A SET NULL would have the database clear
// the column itself, an update that advances no fence, and the build
// refuses a fence that covers such a relation.
@fenced<Delivery>({ field: "planVersion", covers: ["address", "courier"] })
@fenced<Delivery>({ field: "runVersion", covers: ["attempts", "proof"] })
export abstract class Delivery {
  @key
  id: AutoGenerate<Identity.UUID>;

  address: string;

  courier: Nullable<Relation<Courier, { onDelete: "RESTRICT" }>>;

  planVersion: Default<Generic.Int64, 0>;

  attempts: Default<Generic.Int64, 0>;

  proof: Nullable<string>;

  runVersion: Default<Generic.Int64, 0>;

  note: Nullable<string>;

  updatedAt: Temporal.DateTime;
}

// A depot parcels wait in. A delete sets deletedAt, which the fence
// covers, so the delete advances revision as a rename does; a write of
// capacity alone does not.
@fenced<Depot>({ field: "revision", covers: ["name", "deletedAt"] })
export abstract class Depot {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  capacity: Generic.Int64;

  revision: Default<Generic.Int64, 0>;

  deletedAt: Nullable<Temporal.DateTime>;
}
