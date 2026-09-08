import { Identity } from "@psgen/scalar-lib";
import { AutoGenerate, Relation, key } from "@psgen/db";

export abstract class Parent {
  @key
  id: AutoGenerate<Identity.UUID>;
}

// Invalid onDelete VALUE: 'BOGUS' is not in the OnDeleteAction union, so TS
// rejects it as a hard type error before the walk runs.
export abstract class Child {
  @key
  id: AutoGenerate<Identity.UUID>;
  parent: Relation<Parent, { onDelete: "BOGUS" }>;
}
