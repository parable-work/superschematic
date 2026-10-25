import { channel, key } from "@superschematic/memory-store";
import { Identity } from "superscalar";

/**
 * A product changed in shop-db: shop-api publishes it after the commit,
 * and a request waiting for the product, on any replica, reads it again.
 */
@channel()
export abstract class ProductChanged {
  productId: Identity.UUID;
}

/**
 * An upload of a product's image is being signed: one at a time per
 * product, so a browser that sends the request twice gets one URL.
 */
@key({ use: "claim", ttlSeconds: 10 })
export abstract class ImageUploadSigning {
  productId: Identity.UUID;
}
