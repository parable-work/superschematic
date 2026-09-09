import { Identity } from "superscalar";
import { AutoGenerate, Relation, key } from "@superschematic/db";

export abstract class Parent {
  @key
  id: AutoGenerate<Identity.UUID>;
}

// Excess/typo KEY alongside a valid one. A lone typo key would be caught by TS
// (TS2559 "no properties in common" fires because RelationOptions is all
// optional). But with a valid `onDelete` present the object DOES share a
// property, so TS2559 does not fire, and type-literals in type-argument
// position get no excess-property check -- so `onDlete` reaches the walk and the
// Go relationConfigFromTypeNode default branch is the sole guard that rejects it.
export abstract class Child {
  @key
  id: AutoGenerate<Identity.UUID>;
  parent: Relation<Parent, { onDelete: "RESTRICT"; onDlete: "RESTRICT" }>;
}
