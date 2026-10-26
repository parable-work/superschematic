import { Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Relation, key } from "@superschematic/db";

// Referenced parent table.
export abstract class Parent {
  @key
  id: AutoGenerate<Identity.UUID>;
}

// Child with four relation forms: a bare relation (default onDelete), two
// with explicit onDelete actions, and a nullable one that SET NULL clears, so
// one load covers every positive case.
export abstract class Child {
  @key
  id: AutoGenerate<Identity.UUID>;
  parentBare: Relation<Parent>;
  parentRestrict: Relation<Parent, { onDelete: "RESTRICT" }>;
  parentNoAction: Relation<Parent, { onDelete: "NO ACTION" }>;
  parentSetNull: Nullable<Relation<Parent, { onDelete: "SET NULL" }>>;
}
