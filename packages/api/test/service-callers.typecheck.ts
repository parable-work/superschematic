// Type test, checked by `bun run typecheck` (tsconfig.test.json) and never
// run: @requireService and @allowService take a list of service handles, as
// service() in @superschematic/schema-config builds them, on a class or a
// method, and nothing else in from.
import { allowService, auth, requireService } from "@superschematic/api";
import { SchemaKind, service } from "@superschematic/schema-config";

const ShopOrders = service({ name: "shop-orders", kind: SchemaKind.API });

@requireService({ from: [ShopOrders] })
export class Checked {
  @requireService()
  reindex(): void {}

  @auth
  @allowService({ from: [ShopOrders] })
  release(): void {}

  @requireService({ from: [] })
  restock(): void {}

  // @ts-expect-error a service name in a string, not a handle
  @requireService({ from: ["shop-orders"] })
  byName(): void {}

  // @ts-expect-error a plain object, not a handle
  @allowService({ from: [{ name: "shop-orders", kind: "API" }] })
  byObject(): void {}

  // @ts-expect-error an unknown key
  @requireService({ services: [ShopOrders] })
  unknownKey(): void {}
}
