import { Generic, Identity } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import {
  FilterBy,
  HttpMethod,
  SearchQuery,
  SortBy,
  paginated,
  publicRoute,
  requirePermission,
  rest,
  source,
  tableOperation
} from "@superschematic/api";
import { Product } from "@acme/shop-db";

// A product as the cart's catalog shows it: the columns of shop-db's
// Product table a shopper may see.
@source(Product)
export abstract class CatalogProduct {
  id: Identity.UUID;
  sku: Identity.Slug;
  name: Identity.Name;
  priceCents: Generic.Int64;
  inStock: boolean;
}

// What staff may change of a product: a member left out leaves its column
// as it is.
@source(Product)
export abstract class UpdateCatalogProductInput {
  name: Nullable<Identity.Name>;
  priceCents: Nullable<Validate<Generic.Int64, { min: 0 }>>;
  inStock: Nullable<boolean>;
}

// The catalog a shopper fills a cart from. The build serves each of these
// operations from the Product table through the views above (D106):
// shop-cart's implementation has no method for them.
export class CatalogProducts {
  // The products, by name unless the caller sorts them, a page at a time:
  // q keeps those whose name holds each of its words, and filter those in
  // stock, or at a price.
  @rest(HttpMethod.GET, "catalog/products")
  @publicRoute
  @paginated({ defaultLimit: 20, maxLimit: 100, defaultSort: "name" })
  @tableOperation()
  listCatalogProducts(
    q: SearchQuery<Validate<string, { maxLength: 100 }>>,
    sort: SortBy<CatalogProduct, ["name", "priceCents"]>,
    filter: FilterBy<CatalogProduct, ["inStock", "priceCents"]>
  ): CatalogProduct[] {
    throw new Error("schema declaration only");
  }

  // One product; an id no product has is not found.
  @rest(HttpMethod.GET, "catalog/products/{id}")
  @publicRoute
  @tableOperation()
  getCatalogProduct(id: Identity.UUID): CatalogProduct {
    throw new Error("schema declaration only");
  }

  // Changes the members the body sets, and answers the product as the
  // change left it.
  @rest(HttpMethod.PATCH, "catalog/products/{id}")
  @requirePermission(["products.write"])
  @tableOperation()
  updateCatalogProduct(id: Identity.UUID, input: UpdateCatalogProductInput): CatalogProduct {
    throw new Error("schema declaration only");
  }
}
