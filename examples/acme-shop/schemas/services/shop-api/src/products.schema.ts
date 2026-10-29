import { Generic, Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import { Authenticated, HttpMethod, QueryParam, rateLimit, requirePermission, rest, source } from "@superschematic/api";
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
  imageObject: Nullable<string>;
}

export abstract class CreateProductInput {
  sku: Identity.Slug;
  name: Identity.Name;
  priceCents: Validate<Generic.Int64, { min: 0 }>;
}

// The image a browser is about to upload for a product.
export abstract class ProductImageUploadInput {
  contentType: Validate<string, { pattern: "^image/(png|jpeg|webp)$" }>;
}

// Where a browser uploads a product's image: a URL signed for one PUT of
// the content type asked for, which reaches shop-media directly rather than
// through the server (D54).
export abstract class ProductImageUpload {
  objectName: string;
  uploadUrl: string;
  expiresAt: Temporal.DateTime;
}

export class ProductQueries extends Authenticated {
  // At most 120 a minute from one address, which every replica counts in
  // shop-cache, shop-api's rateLimitStore (D75).
  @rest(HttpMethod.GET, "products/{id}")
  @requirePermission(["products.read"])
  @rateLimit({ requestsPerMinute: 120 })
  getProduct(id: Identity.UUID): ProductView {
    throw new Error("schema declaration only");
  }

  // Waits up to waitSeconds, at most 30, for the product to have an image,
  // and returns it as it is then. The replica that records an image wakes
  // the waiter, on whichever replica it is, through shop-cache (D75); the
  // waiter still reads the product now and then, since a wake-up can be
  // lost.
  @rest(HttpMethod.GET, "products/{id}/image")
  @requirePermission(["products.read"])
  waitForProductImage(id: Identity.UUID, waitSeconds: QueryParam<Generic.Int64>): ProductView {
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

  // Signs an upload of the product's image to shop-media and records the
  // object's name on the product.
  @rest(HttpMethod.POST, "products/{id}/image-upload")
  @requirePermission(["products.write"])
  createProductImageUpload(id: Identity.UUID, input: ProductImageUploadInput): ProductImageUpload {
    throw new Error("schema declaration only");
  }
}
