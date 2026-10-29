import { Identity } from "superscalar";
import { AutoGenerate, Encrypted, key } from "@superschematic/db";

// A bank link whose access token is sealed at rest (D85).
export abstract class PlaidItem {
  @key
  id: AutoGenerate<Identity.UUID>;

  itemId: string;

  accessToken: Encrypted<string>;
}
