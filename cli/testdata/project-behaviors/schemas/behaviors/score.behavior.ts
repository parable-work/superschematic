// A behavior the project declares by its class that a field carries
// (D103's step 5): its value field is the anchor field's column.
import { declaresBehavior, valueField, type Integer } from "@superschematic/db";

/** A score on the row that only its operations move. */
@declaresBehavior({
  name: "notes.Score",
  crate: { crate: "notes-rules", path: "crates/rules", register: "notes_rules::Score" },
})
export abstract class Score<C extends {}> {
  /** The score. */
  @valueField score: number;

  /**
   * Adds to the score.
   * @writes
   */
  abstract add(params: { points: Integer }): { score: number };
}
