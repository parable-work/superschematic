// Tool arguments that name generated types: enums and object types alone,
// optional, in lists, lists of lists and maps, in an input type next to
// scalars and primitives; enums as a body argument, as query parameters
// and as a path parameter; and maps as body arguments. The SDK generator
// tests add a union.
import { Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { HttpMethod, QueryParam, rest } from "@superschematic/api";

// The temperature of a color.
export enum Tone {
  Warm = "warm",
  Cool = "cool"
}

// A color sample: an object with enum fields.
export abstract class Swatch {
  tone: Tone;
  alternates: Tone[];
  label: Nullable<string>;
}

// A palette derived from another: a recursive object.
export abstract class Palette {
  name: string;
  parent: Nullable<Palette>;
}

export abstract class PaintInput {
  tone: Tone;
  maybeTone: Nullable<Tone>;
  tones: Tone[];
  toneRows: Tone[][];
  maybeTones: Nullable<Tone[]>;
  swatch: Swatch;
  maybeSwatch: Nullable<Swatch>;
  swatches: Swatch[];
  swatchRows: Swatch[][];
  toneByName: Record<string, Tone>;
  tonesByName: Record<string, Tone[]>;
  swatchByName: Record<string, Swatch>;
  labels: Record<string, string>;
  ref: Identity.UUID;
  at: Temporal.DateTime;
  maybeAt: Nullable<Temporal.DateTime>;
  extra: Generic.JSON;
  palette: Palette;
  count: number;
  tags: string[];
}

export abstract class PaintView {
  id: Identity.UUID;
}

export class PaintMutations {
  // An input type.
  @rest(HttpMethod.POST, "paint")
  paint(input: PaintInput): PaintView {
    throw new Error("schema declaration only");
  }

  // A body argument that is an enum.
  @rest(HttpMethod.PUT, "paint/{id}/tone")
  setTone(id: Identity.UUID, tone: Tone): PaintView {
    throw new Error("schema declaration only");
  }

  // A body argument that is a map of enums.
  @rest(HttpMethod.PUT, "paint/{id}/tone-names")
  nameTones(id: Identity.UUID, toneByName: Record<string, Tone>): PaintView {
    throw new Error("schema declaration only");
  }

  // A body argument that is a map of lists.
  @rest(HttpMethod.PUT, "paint/{id}/labels")
  setLabels(id: Identity.UUID, labelsByLocale: Record<string, string[]>): PaintView {
    throw new Error("schema declaration only");
  }
}

export class PaintQueries {
  // Query parameters that are enums.
  @rest(HttpMethod.GET, "paint")
  listPaint(tone: QueryParam<Nullable<Tone>>, tones: QueryParam<Tone[]>): PaintView[] {
    throw new Error("schema declaration only");
  }

  // A path parameter that is an enum.
  @rest(HttpMethod.GET, "paint/tones/{tone}")
  paintByTone(tone: Tone): PaintView[] {
    throw new Error("schema declaration only");
  }
}
