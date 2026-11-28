import { Identity } from "superscalar";
import { AutoGenerate, key } from "@superschematic/db";

// A search a person saved, by its query: the database's own table beside
// the environment's search index the build adds.
export abstract class SavedSearch {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  query: string;
}
