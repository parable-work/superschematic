import { Identity } from "superscalar";
import { Authenticated, HttpMethod, QueryParam, requirePermission, rest, source } from "@superschematic/api";
import { Product } from "@acme/shop-db";

// The public projection of the Product table.
@source(Product)
export abstract class ProductView {
  id: Identity.UUID;
  sku: Identity.Slug;
  name: Identity.Name;
  priceCents: number;
  inStock: boolean;
}

export abstract class CreateProductInput {
  sku: Identity.Slug;
  name: Identity.Name;
  priceCents: number;
}

// Every route in an Authenticated set sits behind Config.AuthMiddleware. With
// the apikey provider the caller identifies itself with an X-API-Key header;
// the generated APIKeyMiddleware resolves it to a principal before the route
// runs, and @requirePermission is checked against the principal's roles.
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
