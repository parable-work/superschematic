import { Nullable, Validate } from "@superschematic/schema";

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

// A drawing made of lists of lists: grid rows of cells, polygons as lists
// of points and batches of sample vectors.
export abstract class Drawing {
  // Cell labels, one inner list per grid row.
  labels: string[][];

  // Cell shades, one inner list per grid row.
  shades: Shade[][];

  // Polygons, each a list of its points.
  polygons: Array<Array<Point>>;

  // Sample vectors in batches of at most 64; absent before sampling.
  samples: Nullable<Validate<number[][], { listMax: 64 }>>;
}
