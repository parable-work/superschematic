// The MemoryStore kind's decorators (D75): a MemoryStore service's schema
// declares each channel and key the store carries with `@channel` or
// `@key` on an abstract class. The class is a declaration, not a type: its
// fields, in order, are the parts of the declaration's name, each a
// required scalar (a string, an integer, a boolean, an enum, or a scalar
// whose wire form is a string or an integer). Each decorator does nothing
// at run time; superschematic reads the arguments statically, and the
// types here refuse in the editor what they can of what the loader refuses
// again.
//
// ```ts
// import { channel, key } from "@superschematic/memory-store";
// import type { Identity } from "superscalar";
// import { StockLevel } from "@acme/shop-common";
//
// /** A product's stock changed; a waiter rereads it. */
// @channel()
// export abstract class StockChanged { productId!: Identity.UUID }
//
// /** Stock samples for a product page. */
// @channel({ payload: StockLevel })
// export abstract class StockSamples { productId!: Identity.UUID }
//
// /** One receipt email per order. */
// @key({ use: "claim", ttlSeconds: 600 })
// export abstract class ReceiptSent { orderId!: Identity.UUID }
// ```
//
// A declaration's name is its class's name in kebab case (`StockChanged`
// is `stock-changed`) unless `name` sets it, for a name that must outlive
// a rename of the class. The name `rate-limit` is the core's, which every
// store declares for the shared rate limit.

/** A class named as a value: an object type a dependency of the store's schema declares. */
export type PayloadClass = abstract new (...args: never[]) => unknown;

/**
 * The options of `@channel`. A channel carries a wake-up or a sample that
 * a replica can lose; it keeps no message, so it has no TTL.
 */
export type ChannelOptions = {
  /** The object type each message holds, imported from a dependency of the store's schema. Unset, the channel is a signal, whose name is the whole message. */
  readonly payload?: PayloadClass;
  /** The declaration's name, one segment: a lowercase letter, then lowercase letters, digits, `.`, `_` and `-`. Unset is the class's name in kebab case. */
  readonly name?: string;
  /** A channel keeps no message, so it takes no TTL. */
  readonly ttlSeconds?: never;
  /** A channel keeps no message, so it takes no TTL. */
  readonly maxTtlSeconds?: never;
};

/** What a key is for: a counter counts, and a claim lets one caller do a piece of work once. */
export type KeyUse = "counter" | "claim";

/** The members every key's options share. */
type KeyCommon = {
  /** counter or claim. */
  readonly use: KeyUse;
  /** The declaration's name, one segment: a lowercase letter, then lowercase letters, digits, `.`, `_` and `-`. Unset is the class's name in kebab case. */
  readonly name?: string;
  /** A key holds a count or a claim, never a value. */
  readonly payload?: never;
};

/**
 * The options of `@key`. Every key expires: it sets exactly one of
 * `ttlSeconds`, its TTL from the write that creates it, and
 * `maxTtlSeconds`, the longest TTL a call may give it, for a key whose life
 * depends on the call. Both are whole seconds from 1 to 604800 (7 days).
 */
export type KeyOptions =
  | (KeyCommon & {
      /** The key's TTL from the write that creates it, in seconds: 1 to 604800. */
      readonly ttlSeconds: number;
      readonly maxTtlSeconds?: never;
    })
  | (KeyCommon & {
      /** The longest TTL a call may give the key, in seconds: 1 to 604800. Its definition's ForTTL takes the TTL. */
      readonly maxTtlSeconds: number;
      readonly ttlSeconds?: never;
    });

const noop: ClassDecorator = () => {};

/** Declares a channel of the store: its class's fields are the parts of its name. */
export function channel(_options?: ChannelOptions): ClassDecorator {
  return noop;
}

/** Declares a key of the store, a counter or a claim: its class's fields are the parts of its name. */
export function key(_options: KeyOptions): ClassDecorator {
  return noop;
}
