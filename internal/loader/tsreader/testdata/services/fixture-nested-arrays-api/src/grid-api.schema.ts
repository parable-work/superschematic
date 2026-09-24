import { Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { HttpMethod, QueryParam, rest } from "@superschematic/api";

// The shade of one grid cell.
export enum Shade {
  Light = "light",
  Dark = "dark"
}

// A point on a plane.
export abstract class Point {
  x: number;
  y: number;
}

// Request body: a grid to store.
export abstract class SaveGridInput {
  // Cell labels, one inner list per row.
  labels: string[][];

  // Cell shades, one inner list per row.
  shades: Shade[][];

  // Polygons, each a list of its points.
  polygons: Point[][];

  // Weight vectors in batches; absent when the grid is unweighted.
  weights: Nullable<number[][]>;
}

// Response: a stored grid.
export abstract class GridView {
  id: Identity.UUID;
  labels: string[][];
  shades: Shade[][];
  polygons: Point[][];
  weights: Nullable<number[][]>;
}

export class GridMutations {
  // Store a grid from a request body.
  @rest(HttpMethod.POST, "grids")
  saveGrid(input: SaveGridInput): GridView {
    throw new Error("schema declaration only");
  }

  // Replace a grid's labels; the body argument is a list of lists.
  @rest(HttpMethod.PUT, "grids/{id}/labels")
  replaceLabels(id: Identity.UUID, labels: string[][]): GridView {
    throw new Error("schema declaration only");
  }
}

export class GridQueries {
  @rest(HttpMethod.GET, "grids/{id}")
  getGrid(id: Identity.UUID): GridView {
    throw new Error("schema declaration only");
  }

  // One grid's labels as a bare list of lists, at most `limit` rows.
  @rest(HttpMethod.GET, "grids/{id}/labels")
  gridLabels(id: Identity.UUID, limit: QueryParam<Nullable<number>>): string[][] {
    throw new Error("schema declaration only");
  }
}
