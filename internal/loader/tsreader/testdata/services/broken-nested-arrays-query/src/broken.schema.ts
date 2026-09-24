import { HttpMethod, QueryParam, rest } from "@superschematic/api";

export class GridQueries {
  // A list of lists has no query-string form.
  @rest(HttpMethod.GET, "grids")
  listGrids(rows: QueryParam<string[][]>): string {
    throw new Error("schema declaration only");
  }
}
