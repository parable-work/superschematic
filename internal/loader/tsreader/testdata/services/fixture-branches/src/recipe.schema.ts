import { Generic, Identity } from "superscalar";
import { behavior } from "@superschematic/schema";

export abstract class Step {
  instruction: string;
  position: Generic.Int64;
  timings?: Generic.JSON;
}

export abstract class Ingredient {
  stepKey: Identity.UUID;
  quantity: string;
}

export abstract class Cover {
  photoUrl: string;
}

@behavior("Branches", {
  kinds: {
    step: { type: "Step", order: "position", units: { timings: "keyed" }, retentionDays: 365 },
    ingredient: { type: "Ingredient", parent: { key: "stepKey", of: "step" } },
    cover: { type: "Cover", singleton: true }
  },
  sweep: { intervalMs: 3600000, abandonAfter: 2592000000 }
})
export abstract class Recipe {
  title: string;
}
