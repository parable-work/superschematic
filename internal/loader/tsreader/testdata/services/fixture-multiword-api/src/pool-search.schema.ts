import { Identity } from "superscalar";
import { HttpMethod, rest } from "@superschematic/api";

export abstract class PoolSearchIndex {
  id: Identity.UUID;
  name: Identity.Name;
}

export abstract class RebuildPoolSearchIndexInput {
  name: Identity.Name;
}

// Both sets resolve to the two-word namespace `pool-search`.
export class PoolSearchQueries {
  @rest(HttpMethod.GET, "pool-search/indexes/{id}")
  getIndex(id: Identity.UUID): PoolSearchIndex {
    throw new Error("schema declaration only");
  }
}

export class PoolSearchMutations {
  @rest(HttpMethod.POST, "pool-search/indexes")
  rebuildIndex(input: RebuildPoolSearchIndexInput): PoolSearchIndex {
    throw new Error("schema declaration only");
  }
}
