import { Identity } from "@psgen/scalar-lib";
import { key } from "@psgen/db";

export abstract class NotATable {
  @key
  id: Identity.UUID;
}
