import { Identity } from "@psgen/scalar-lib";
import { AutoGenerate, Relation, key } from "@psgen/db";

// Referenced parent table.
export abstract class Parent {
  @key
  id: AutoGenerate<Identity.UUID>;
}

// Child with three relation forms: a bare relation (default onDelete) and two
// with explicit onDelete actions, so one load covers every positive case.
export abstract class Child {
  @key
  id: AutoGenerate<Identity.UUID>;
  parentBare: Relation<Parent>;
  parentRestrict: Relation<Parent, { onDelete: "RESTRICT" }>;
  parentNoAction: Relation<Parent, { onDelete: "NO ACTION" }>;
}
