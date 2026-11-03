// Type-level checks of @superschematic/memory-store (D75). Nothing imports
// this file: `bun run typecheck`, which `make ts` runs, checks it, and fails
// on an @ts-expect-error that no longer errors.
import { channel, key } from "../src/index";

abstract class StockLevel {
  productId: string;
  onHand: number;
}

@channel()
export abstract class StockChanged {
  productId: string;
}

@channel({ payload: StockLevel, name: "stock.samples" })
export abstract class StockSamples {
  productId: string;
}

@key({ use: "claim", ttlSeconds: 600 })
export abstract class ReceiptSent {
  orderId: string;
}

@key({ use: "counter", maxTtlSeconds: 86400, name: "views" })
export abstract class ProductViews {
  productId: string;
  day: string;
}

// @ts-expect-error a channel keeps no message, so it takes no TTL
@channel({ ttlSeconds: 60 })
export abstract class TimedChannel {
  id: string;
}

// @ts-expect-error every key expires: it sets ttlSeconds or maxTtlSeconds
@key({ use: "counter" })
export abstract class Forever {
  id: string;
}

// @ts-expect-error a key sets one of ttlSeconds and maxTtlSeconds, not both
@key({ use: "claim", ttlSeconds: 60, maxTtlSeconds: 120 })
export abstract class Both {
  id: string;
}

// @ts-expect-error a key's use is counter or claim
@key({ use: "cache", ttlSeconds: 60 })
export abstract class Cached {
  id: string;
}

// @ts-expect-error a key holds a count or a claim, never a value
@key({ use: "counter", ttlSeconds: 60, payload: StockLevel })
export abstract class Valued {
  id: string;
}

// @ts-expect-error a payload is a class, not a string
@channel({ payload: "StockLevel" })
export abstract class Named {
  id: string;
}
