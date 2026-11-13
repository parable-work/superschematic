import { Generic } from "superscalar";
import { HttpMethod, publicRoute, rest } from "@superschematic/api";

// How many notes there are, and how many of them are pinned.
export abstract class Digest {
  notes: Generic.Int64;
  pinned: Generic.Int64;
}

export class Digests {
  // The digest, counted from what notes-api answers.
  @rest(HttpMethod.GET, "digest")
  @publicRoute
  getDigest(): Digest {
    throw new Error("schema declaration only");
  }
}
