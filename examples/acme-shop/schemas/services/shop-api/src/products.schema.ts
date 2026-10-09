import { Generic, Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
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

  // Signs an upload of the product's image to shop-media and records the
  // object's name on the product.
  @rest(HttpMethod.POST, "products/{id}/image-upload")
  @requirePermission(["products.write"])
  createProductImageUpload(id: Identity.UUID, input: ProductImageUploadInput): ProductImageUpload {
    throw new Error("schema declaration only");
  }
}
