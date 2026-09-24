import { Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import {
  HttpMethod,
  QueryParam,
  bodyLimit,
  docs,
  icon,
  manualRouteRegistration,
  mcp,
  publicRoute,
  rateLimit,
  requirePermission,
  rest,
  timeout
} from "@superschematic/api";

export enum CartStatus {
  Open = "open",
  CheckedOut = "checked_out"
}

export abstract class CartLine {
  sku: Identity.Slug;
  quantity: number;
}

export abstract class CartView {
  id: Identity.UUID;
  status: CartStatus;
  lines: CartLine[];
  updatedAt: Temporal.DateTime;
}

export abstract class AddCartLineInput {
  sku: Identity.Slug;
  quantity: Validate<number, { min: 1; max: 99 }>;
}

export abstract class StorefrontHealth {
  status: string;
}

// A liveness probe: @publicRoute opens it to callers the authenticator does
// not know.
export class StorefrontProbes {
  @mcp({ hidden: true, reason: "An infrastructure probe, not a shopper action." })
  @rest(HttpMethod.GET, "health")
  @publicRoute
  getHealth(): StorefrontHealth {
    throw new Error("schema declaration only");
  }
}

// Reads need carts.read; the TypeScript router answers 401 to a caller the
// authenticator does not resolve and 403 to one without the permission.
export class CartQueries {
  @docs({
    title: "Get a cart",
    description: "Returns one shopping cart with its lines.",
    capability: "storefront.carts.get",
    lifecycle: "active",
    visibility: "public",
    audience: "shoppers",
    replayMode: "read_only"
  })
  @icon("receipt")
  @mcp({ handle: "get_cart" })
  @rest(HttpMethod.GET, "carts/{cartId}")
  @requirePermission(["carts.read"])
  getCart(cartId: Identity.UUID): CartView {
    throw new Error("schema declaration only");
  }

  @mcp({ hidden: true, reason: "Staff list carts in the back office; a model reads one with get_cart." })
  @rest(HttpMethod.GET, "carts")
  @requirePermission(["carts.read"])
  listCarts(statuses: QueryParam<Nullable<Validate<CartStatus[], { listMin: 0; listMax: 2 }>>>): CartView[] {
    throw new Error("schema declaration only");
  }

  // The event stream is a server-sent event body the JSON router cannot
  // express: the router applies the gate, then hands the request to the
  // app's own handler.
  @mcp({ hidden: true, reason: "A server-sent event stream is not a tool result." })
  @rest(HttpMethod.GET, "carts/{cartId}/events")
  @manualRouteRegistration
  @requirePermission(["carts.read"])
  streamCartEvents(cartId: Identity.UUID): CartView {
    throw new Error("schema declaration only");
  }
}

@rateLimit({ requestsPerMinute: 30 })
@bodyLimit({ megabytes: 1 })
export class CartMutations {
  @docs({
    title: "Add a cart line",
    description: "Adds a product to a cart, or raises its quantity.",
    capability: "storefront.carts.lines.add",
    lifecycle: "active",
    visibility: "public",
    audience: "shoppers"
  })
  @icon("box")
  @mcp({ handle: "add_cart_line" })
  @rest(HttpMethod.POST, "carts/{cartId}/lines")
  @requirePermission(["carts.write"])
  @timeout({ seconds: 5 })
  addCartLine(cartId: Identity.UUID, input: AddCartLineInput): CartView {
    throw new Error("schema declaration only");
  }
}
