import { Identity } from "superscalar";
import {
  Authenticated,
  HttpMethod,
  QueryParam,
  docs,
  icon,
  mcp,
  requirePermission,
  rest,
  source
} from "@superschematic/api";
import { Product } from "@acme/shop-db";

// The public projection of the Product table.
@source(Product)
export abstract class ProductView {
  id: Identity.UUID;
  sku: Identity.Slug;
  name: Identity.Name;
  priceCents: number;
  inStock: boolean;
  variants: string[][];
}

export abstract class CreateProductInput {
  sku: Identity.Slug;
  name: Identity.Name;
  priceCents: number;
  // One inner list of option values per variant.
  variants: string[][];
}

// Every route in an Authenticated set sits behind Config.AuthMiddleware. With
// the apikey provider the caller identifies itself with an X-API-Key header;
// the generated APIKeyMiddleware resolves it to a principal before the route
// runs, and @requirePermission is checked against the principal's roles.
//
// @docs gives each operation its OpenAPI summary and description. The acme
// extension accepts only its own audiences and writes the record under
// x-acme-docs.
//
// @mcp classifies each operation for MCP: a visible tool takes its name and
// description from @docs and its icon from @icon; a hidden one says why it
// is not a tool. The acme extension requires a classification on every
// operation of this API and restricts @icon to its icon set.
export class ProductQueries extends Authenticated {
  @docs({
    title: "Get a product",
    description: "Returns one product from the catalog.",
    capability: "catalog.products.get",
    lifecycle: "active",
    visibility: "public",
    audience: "shoppers",
    replayMode: "read_only"
  })
  @icon("tag")
  @mcp({ handle: "get_product" })
  @rest(HttpMethod.GET, "products/{id}")
  @requirePermission(["products.read"])
  getProduct(id: Identity.UUID): ProductView {
    throw new Error("schema declaration only");
  }

  @mcp({ hidden: true, reason: "The storefront lists products; a model reads one with get_product." })
  @rest(HttpMethod.GET, "products")
  @requirePermission(["products.read"])
  listProducts(inStock: QueryParam<boolean>): ProductView[] {
    throw new Error("schema declaration only");
  }
}

export class ProductMutations extends Authenticated {
  @docs({
    title: "Create a product",
    description: "Adds a product to the catalog.",
    capability: "catalog.products.create",
    lifecycle: "active",
    visibility: "internal",
    audience: "staff"
  })
  @icon("box")
  @mcp({ handle: "create_product" })
  @rest(HttpMethod.POST, "products")
  @requirePermission(["products.write"])
  createProduct(input: CreateProductInput): ProductView {
    throw new Error("schema declaration only");
  }

  // A list of lists as a body argument, and so as a tool argument: the
  // tool's variants parameter is an array of arrays of strings.
  @docs({
    title: "Replace a product's variants",
    description: "Replaces the option values of every variant of one product.",
    capability: "catalog.products.variants",
    lifecycle: "active",
    visibility: "internal",
    audience: "staff"
  })
  @icon("tag")
  @mcp({ handle: "replace_variants" })
  @rest(HttpMethod.PUT, "products/{id}/variants")
  @requirePermission(["products.write"])
  replaceVariants(id: Identity.UUID, variants: string[][]): ProductView {
    throw new Error("schema declaration only");
  }
}
