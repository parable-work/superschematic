import { Identity, Temporal } from "superscalar";
import { Validate } from "@superschematic/schema";
import { FileDownload, FileUpload, HttpMethod, publicRoute, requirePermission, rest } from "@superschematic/api";
import { Price } from "@acme/shop-common";

export enum CartStatus {
  Open = "open",
  CheckedOut = "checked_out"
}

export abstract class CartLine {
  sku: Identity.Slug;
  quantity: number;
  unitPrice: Price;
}

export abstract class CartView {
  id: Identity.UUID;
  status: CartStatus;
  lines: CartLine[];
  total: Price;
  updatedAt: Temporal.DateTime;
}

export abstract class AddCartLineInput {
  sku: Identity.Slug;
  quantity: Validate<number, { min: 1; max: 99 }>;
}

// A CSV of lines to add to a cart: a header row, then one sku,quantity
// row a line, at most 64 KiB (D81).
export abstract class ImportCartLinesInput {
  lines: FileUpload<{ maxBytes: 65536; contentTypes: ["text/csv"]; extensions: [".csv"] }>;
}

export abstract class StorefrontHealth {
  status: string;
}

export class StorefrontProbes {
  @rest(HttpMethod.GET, "health")
  @publicRoute
  getHealth(): StorefrontHealth {
    throw new Error("schema declaration only");
  }
}

export class CartQueries {
  @rest(HttpMethod.GET, "carts/{cartId}")
  @requirePermission(["carts.read"])
  getCart(cartId: Identity.UUID): CartView {
    throw new Error("schema declaration only");
  }

  // A cart's lines as CSV, the form importCartLines reads, with each
  // line's unit price.
  @rest(HttpMethod.GET, "carts/{cartId}/export")
  @requirePermission(["carts.read"])
  exportCart(cartId: Identity.UUID): FileDownload<{ contentType: "text/csv; charset=utf-8"; filename: "cart.csv" }> {
    throw new Error("schema declaration only");
  }
}

export class CartMutations {
  @rest(HttpMethod.POST, "carts/{cartId}/lines")
  @requirePermission(["carts.write"])
  addCartLine(cartId: Identity.UUID, input: AddCartLineInput): CartView {
    throw new Error("schema declaration only");
  }

  // Adds each line of a CSV to a cart, as addCartLine adds one.
  @rest(HttpMethod.POST, "carts/{cartId}/lines/import")
  @requirePermission(["carts.write"])
  importCartLines(cartId: Identity.UUID, input: ImportCartLinesInput): CartView {
    throw new Error("schema declaration only");
  }
}
