import { Identity } from "superscalar";
import { Nullable, jsonField } from "@superschematic/schema";
import { AutoGenerate, key } from "@superschematic/db";

// The state of one board cell.
export enum CellState {
  Empty = "empty",
  Filled = "filled"
}

// A board coordinate, stored inside the board's JSON columns.
@jsonField
export abstract class BoardPoint {
  x: number;
  y: number;
}

// A game board whose columns are lists of lists.
export abstract class Board {
  @key
  id: AutoGenerate<Identity.UUID>;

  // Cell labels, one inner list per row.
  labels: string[][];

  // Cell states, one inner list per row.
  states: CellState[][];

  // Walls, each a list of the points it runs through.
  walls: BoardPoint[][];

  // Scores per round, one inner list per player; absent before the first round.
  scores: Nullable<number[][]>;
}
