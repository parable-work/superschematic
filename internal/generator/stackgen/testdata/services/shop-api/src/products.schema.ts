import { HttpMethod, rest } from "@superschematic/api";

export abstract class ProductView {
  id: string;
  name: string;
}

// Open to any caller, so a server that calls shop-api reaches an operation
// it may invoke.
export class ProductQueries {
  @rest(HttpMethod.GET, "products/{id}")
  getProduct(id: string): ProductView {
    throw new Error("schema declaration only");
  }
}
