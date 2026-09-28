import { Generic, Identity } from "superscalar";
import { Validate } from "@superschematic/schema";
import { Authenticated, HttpMethod, QueryParam, requirePermission, rest, source } from "@superschematic/api";
import { Product } from "@acme/shop-db";

// What the API returns for a product: the columns of the Product table a
// caller may see.
@source(Product)
export abstract class ProductView {
  id: Identity.UUID;
  sku: Identity.Slug;
  name: Identity.Name;
  priceCents: Generic.Int64;
  inStock: boolean;
}

export abstract class CreateProductInput {
  sku: Identity.Slug;
  name: Identity.Name;
  priceCents: Validate<Generic.Int64, { min: 0 }>;
}

export class ProductQueries extends Authenticated {
  @rest(HttpMethod.GET, "products/{id}")
  @requirePermission(["products.read"])
  getProduct(id: Identity.UUID): ProductView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "products")
  @requirePermission(["products.read"])
  listProducts(inStock: QueryParam<boolean>): ProductView[] {
    throw new Error("schema declaration only");
  }
}

export class ProductMutations extends Authenticated {
  @rest(HttpMethod.POST, "products")
  @requirePermission(["products.write"])
  createProduct(input: CreateProductInput): ProductView {
    throw new Error("schema declaration only");
  }
}
