import { Identity } from "superscalar";
import { AutoGenerate, Relation, key } from "@superschematic/db";

export abstract class Parent {
  @key
  id: AutoGenerate<Identity.UUID>;
}

// SET NULL on a required relation: the foreign key column is NOT NULL, so
// the action could never run. The loader refuses it and names the fix.
export abstract class Child {
  @key
  id: AutoGenerate<Identity.UUID>;
  parent: Relation<Parent, { onDelete: "SET NULL" }>;
}
