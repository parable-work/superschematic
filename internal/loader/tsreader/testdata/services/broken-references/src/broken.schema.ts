import { Identity } from "superscalar";
import { RefersTo } from "@superschematic/api";

// The target of a reference is a schema type, not a scalar.
export abstract class OrderLine {
  productId: RefersTo<Identity.UUID, Identity.UUID>;
}
