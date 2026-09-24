// Type test, checked by `bun run typecheck` (tsconfig.test.json) and never
// run: an extension's authoring package adds its invocation policy key to
// @mcp by augmenting MCPToolOptions, and the object @mcp takes accepts that
// key with its values and nothing else.
import { mcp } from "@superschematic/api";
import type { MCPConfig } from "@superschematic/api";

declare module "@superschematic/api" {
  interface MCPToolOptions {
    readonly review?: "never" | "on-write" | "always";
  }
}

export class Checked {
  @mcp({ handle: "get_order" })
  getOrder(): void {}

  @mcp({ handle: "delete_order", invocationPolicy: "ask" })
  deleteOrder(): void {}

  @mcp({ handle: "refund_order", review: "always", _meta: { ui: { resourceUri: "ui://orders/refund" } } })
  refundOrder(): void {}

  // @ts-expect-error not one of the extension's values
  @mcp({ handle: "cancel_order", review: "sometimes" })
  cancelOrder(): void {}

  // @ts-expect-error not one of the core's values
  @mcp({ handle: "ship_order", invocationPolicy: "always" })
  shipOrder(): void {}

  // @ts-expect-error a hidden operation takes no policy
  @mcp({ hidden: true, reason: "Staff console only.", review: "never" })
  exportOrders(): void {}

  // @ts-expect-error an unknown key
  @mcp({ handle: "list_orders", confirm: "always" })
  listOrders(): void {}
}

export const visible: MCPConfig = { handle: "get_order", review: "on-write" };
