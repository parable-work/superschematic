import { Identity } from "superscalar";
import { key } from "@superschematic/db";

export abstract class Product {
  @key
  id: Identity.UUID;

  name: string;
}
