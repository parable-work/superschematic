import { HttpMethod, requireService, rest } from "@superschematic/api";

export abstract class IndexResult {
  indexed: number;
}

// Only a server with a calls edge to notes-index invokes it, so stack init
// leaves notes-index internal.
@requireService()
export class IndexMutations {
  @rest(HttpMethod.POST, "index/{id}")
  indexNote(id: string): IndexResult {
    throw new Error("schema declaration only");
  }
}
