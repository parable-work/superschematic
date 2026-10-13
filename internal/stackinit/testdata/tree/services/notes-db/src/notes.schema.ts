import { Identity } from "superscalar";
import { key } from "@superschematic/db";

export abstract class Note {
  @key
  id: Identity.UUID;

  body: string;
}
