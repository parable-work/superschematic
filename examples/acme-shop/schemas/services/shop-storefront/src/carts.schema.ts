import { Identity, Temporal } from "superscalar";
import { Validate } from "@superschematic/schema";
import { HttpMethod, publicRoute, requirePermission, rest } from "@superschematic/api";
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
}

export class CartMutations {
  @rest(HttpMethod.POST, "carts/{cartId}/lines")
  @requirePermission(["carts.write"])
  addCartLine(cartId: Identity.UUID, input: AddCartLineInput): CartView {
    throw new Error("schema declaration only");
  }
}
