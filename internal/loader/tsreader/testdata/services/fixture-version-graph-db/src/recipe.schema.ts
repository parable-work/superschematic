import { Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import {
  AutoGenerate,
  JsonField,
  Relation,
  conflictUnit,
  graphMember,
  jsonField,
  key,
  versionGraph,
  versioned
} from "@superschematic/db";

// A recipe: the stable identity its steps and ingredients are versioned under.
@versionGraph({ schemaEpoch: 1 })
export abstract class Recipe {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
}

// One step of a recipe, ordered by position; updatedBy names its row's writer.
@versioned({ retentionDays: 365 })
@graphMember({ graph: Recipe, order: "position" })
export abstract class Step {
  @key
  id: AutoGenerate<Identity.UUID>;
  recipe: Relation<Recipe>;
  position: Generic.Int64;
  instruction: string;

  @conflictUnit("keyed")
  timings: Generic.JSON;

  @conflictUnit("excluded")
  scratch: Nullable<string>;

  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
  updatedAt: Temporal.DateTime;
  updatedBy: Identity.UUID;
}

// An ingredient one step uses.
@versioned({ retentionDays: 365 })
@graphMember({ graph: Recipe, parent: { key: "stepKey", of: Step } })
export abstract class Ingredient {
  @key
  id: AutoGenerate<Identity.UUID>;
  recipe: Relation<Recipe>;
  stepKey: Identity.UUID;
  quantity: string;

  @jsonField
  @conflictUnit("jsonSchema")
  substitutes: JsonField<Generic.JSON>;
}

// A cook's note, threaded under another note.
@versioned
@graphMember({ graph: Recipe, parent: { key: "replyTo", of: Note } })
export abstract class Note {
  @key
  id: AutoGenerate<Identity.UUID>;
  recipe: Relation<Recipe>;
  replyTo: Nullable<Identity.UUID>;
  body: string;
}

// The recipe's cover photo: at most one per ref.
@versioned
@graphMember({ graph: Recipe, singleton: true })
export abstract class Cover {
  @key
  id: AutoGenerate<Identity.UUID>;
  recipe: Relation<Recipe>;
  photoUrl: string;
}
