import { Identity } from "superscalar";
import { key } from "@superschematic/db";

export abstract class NotATable {
  @key
  id: Identity.UUID;
}
