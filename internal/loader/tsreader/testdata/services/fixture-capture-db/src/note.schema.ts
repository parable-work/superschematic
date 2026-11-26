import { Identity } from "superscalar";
import { AutoGenerate, key, unique } from "@superschematic/db";

// A note an account keeps, filed under one of its teams.
export abstract class Note {
  @unique
  @key
  id: AutoGenerate<Identity.UUID>;

  account: Identity.UUID;
  team: Identity.UUID;
  title: string;
}
