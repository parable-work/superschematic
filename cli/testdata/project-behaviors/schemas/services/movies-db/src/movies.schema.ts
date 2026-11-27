import { Identity } from "superscalar";
import { key, type Behavior, type BehaviorField } from "@superschematic/db";
import { Rating } from "../../../behaviors/rating.behavior";
import { Score } from "../../../behaviors/score.behavior";

// A table that implements the project's own behavior, a class's, as it
// would implement one of the core's, and carries another on a field
// (D103's step 5).
export abstract class Movie implements Behavior<typeof Rating, Movie, { maxStars: 5; clearPermission: "movies.moderate" }> {
  @key id: Identity.UUID;
  title: string;
  popularity: BehaviorField<typeof Score, {}>;
}
