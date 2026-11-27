import { Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import {
  AutoGenerate,
  JsonField,
  Relation,
  conflictUnit,
  graphMember,
  index,
  jsonField,
  key,
  queue,
  versionGraph,
  versioned
} from "@superschematic/db";

// Sent for each commit of a recipe's graph, in the transaction that wrote
// it: a worker handles one recipe's commits at a time.
@queue({ key: "root", serialize: true })
export abstract class RecipeCommitted {
  root: Identity.UUID;
  ref: Nullable<Identity.UUID>;
  commit: Identity.UUID;
  sequence: Nullable<Generic.Int64>;
}

// Sent when a recipe's release moves to another commit.
@queue({ key: "root", serialize: true })
export abstract class RecipePublished {
  root: Identity.UUID;
  commit: Identity.UUID;
  sequence: Nullable<Generic.Int64>;
}

// A recipe: the stable identity its steps and ingredients are versioned
// under. Its commits and its releases each enqueue a message.
@versionGraph({ schemaEpoch: 1, snapshotEvery: 3, enqueueOnCommit: RecipeCommitted, enqueueOnRelease: RecipePublished })
export abstract class Recipe {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
}

// One step of a recipe, ordered by position; updatedBy names its row's writer.
@versioned({ retentionDays: 365, exclude: ["scratch"] })
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

// A part of the recipe's write-up, nested under another part. Its rank is
// a string order key, so Arrange places it among its siblings, and the
// parts a recipe has released are kept in a table of their own, with the
// index a reader of them uses.
@versioned
@index<Part>(["parentKey", "rank"])
@graphMember({ graph: Recipe, parent: { key: "parentKey", of: Part }, order: "rank", released: true })
export abstract class Part {
  @key
  id: AutoGenerate<Identity.UUID>;
  recipe: Relation<Recipe>;
  parentKey: Nullable<Identity.UUID>;
  rank: string;
  heading: string;
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

// A utensil the recipe needs, keyed by a plain UUID rather than an
// AutoGenerate one.
@versioned
@graphMember({ graph: Recipe })
export abstract class Utensil {
  @key
  id: Identity.UUID;
  recipe: Relation<Recipe>;
  name: string;
}

// How a tasting went.
export enum Verdict {
  Again = "again",
  Tweak = "tweak",
  Never = "never"
}

// A tasting of the recipe. Its columns hold a value of every class a
// descriptor names.
@versioned
@graphMember({ graph: Recipe })
export abstract class Tasting {
  @key
  id: AutoGenerate<Identity.UUID>;
  recipe: Relation<Recipe>;
  taster: Identity.UserID;
  salty: boolean;
  score: number;
  servings: Generic.Int64;
  tastedOn: Temporal.Date;
  tastedAt: Temporal.DateTime;
  servedAt: Temporal.Time;
  rested: Temporal.Duration;
  verdict: Verdict;
  remarks: Generic.JSON;
  tags: string[];
  helpers: Identity.UUID[];
  bites: Generic.Int64[][];
}
