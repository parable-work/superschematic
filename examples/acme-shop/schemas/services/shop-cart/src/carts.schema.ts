import { Generic, Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import { Authenticated, HttpMethod, QueryParam, publicRoute, requirePermission, rest } from "@superschematic/api";

// One line of a cart: a product, how many, and, once the checkout holds
// it, the price it was held at.
export abstract class CartLine {
  productId: Identity.UUID;
  quantity: Generic.Int64;
  // pending until the checkout starts, then held (Holds.items).
  state: string;
  unitPriceCents: Nullable<Generic.Int64>;
}

// A shopper's cart: shop-db's Checkout, in cart until shop-orders starts
// it.
export abstract class CartView {
  id: Identity.UUID;
  // cart, held, placed, expired or cancelled.
  status: string;
  lines: CartLine[];
  // The counter and the slot the shopper collects it in, once chosen.
  collectionPointId: Nullable<Identity.UUID>;
  pickupAt: Nullable<Temporal.DateTime>;
}

export abstract class AddCartLineInput {
  productId: Identity.UUID;
  quantity: Validate<Generic.Int64, { min: 1; max: 99 }>;
}

export abstract class ChoosePickupInput {
  collectionPointId: Identity.UUID;
  startsAt: Temporal.DateTime;
}

// A free fifteen-minute slot at a counter.
export abstract class PickupSlot {
  collectionPointId: Identity.UUID;
  startsAt: Temporal.DateTime;
  endsAt: Temporal.DateTime;
}

// A shopper's carts: each is their own, and another's is not found.
export class Carts extends Authenticated {
  @rest(HttpMethod.POST, "carts")
  @requirePermission(["orders.write"])
  openCart(): CartView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "carts/{id}")
  @requirePermission(["orders.read"])
  getCart(id: Identity.UUID): CartView {
    throw new Error("schema declaration only");
  }

  // Adds units of a product, to its line when the cart has one.
  @rest(HttpMethod.POST, "carts/{id}/lines")
  @requirePermission(["orders.write"])
  addCartLine(id: Identity.UUID, input: AddCartLineInput): CartView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.PUT, "carts/{id}/pickup")
  @requirePermission(["orders.write"])
  choosePickup(id: Identity.UUID, input: ChoosePickupInput): CartView {
    throw new Error("schema declaration only");
  }
}

// The pickup slots anyone may read: each counter's free slots over the
// days from `from`, at most seven, by CollectionPoint's findAvailability.
export class PickupSlots {
  @rest(HttpMethod.GET, "pickup-slots")
  @publicRoute
  listPickupSlots(
    from: QueryParam<Temporal.DateTime>,
    days: QueryParam<Nullable<Validate<number, { min: 1; max: 7 }>>>
  ): PickupSlot[] {
    throw new Error("schema declaration only");
  }
}
