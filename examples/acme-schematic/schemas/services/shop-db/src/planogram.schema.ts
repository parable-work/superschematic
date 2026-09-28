import { Generic, Identity, Temporal } from "superscalar";
import { AutoGenerate, Relation, conflictUnit, graphMember, key, versionGraph, versioned } from "@superschematic/db";

// A shop's shelf plan: which products face out on which bay. The plan is a
// version graph: a shop edits a change set of the published plan and merges
// it back. The loader adds PlanogramRef, PlanogramCommit, PlanogramPatch and
// their enums; the ORM adds db.PlanogramGraph().
@versionGraph()
export abstract class Planogram {
  @key
  id: AutoGenerate<Identity.UUID>;

  shop: Identity.UUID;

  name: Identity.Name;
}

// One bay of shelving, left to right by position.
@versioned({ retentionDays: 90 })
@graphMember({ graph: Planogram, order: "position" })
export abstract class Bay {
  @key
  id: AutoGenerate<Identity.UUID>;

  planogram: Relation<Planogram>;

  position: Generic.Int64;

  label: string;

  // Shelf heights in centimetres, by shelf name. Two change sets that move
  // different shelves merge without a conflict.
  @conflictUnit("keyed")
  shelfHeights: Generic.JSON;

  createdAt: Temporal.DateTime;
  updatedAt: Temporal.DateTime;
}

// A product facing out on a bay. Deleting the bay removes its facings.
@versioned({ retentionDays: 90 })
@graphMember({ graph: Planogram, parent: { key: "bayKey", of: Bay }, order: "position" })
export abstract class Facing {
  @key
  id: AutoGenerate<Identity.UUID>;

  planogram: Relation<Planogram>;

  bayKey: Identity.UUID;

  position: Generic.Int64;

  sku: Identity.Slug;

  // How many units wide the product faces out.
  width: number;
}
