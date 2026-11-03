// Type test, checked by `bun run typecheck` (tsconfig.test.json) and never
// run: @behaviorOperations takes a class, the table, and goes on a class
// with no members, whose @auth, @requirePermission, @publicRoute, service
// clause and rate limit apply to every route the set serves. @requireOwnership
// has no class form (D103).
import {
  auth,
  behaviorOperations,
  publicRoute,
  rateLimit,
  requireOwnership,
  requirePermission,
  requireService
} from "@superschematic/api";

declare abstract class Order {
  id: string;
}

@behaviorOperations(Order)
@requirePermission(["orders.write"])
export abstract class OrderActions {}

@behaviorOperations(Order)
@auth
@requireService({ from: [] })
@rateLimit({ requestsPerMinute: 60 })
export abstract class SignedInOrderActions {}

@behaviorOperations(Order)
@publicRoute
export abstract class PublicOrderActions {}

// @ts-expect-error the table is a class, not its name
@behaviorOperations("Order")
export abstract class NamedByString {}

@behaviorOperations(Order)
// @ts-expect-error @requireOwnership has no class form
@requireOwnership
export abstract class OwnedOrderActions {}

export class Methods {
  // @ts-expect-error a class decorator, not a method's
  @behaviorOperations(Order)
  transition(): void {}

  @auth
  @requirePermission(["orders.read"])
  read(): void {}

  @publicRoute
  open(): void {}
}
