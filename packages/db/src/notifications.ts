/*
Events, notifications and channels (D153): the events a table's rows raise,
the notifier's tables, and the table of channel identities.
*/

/** How urgent an event is: sent now and acknowledged, sent now outside quiet hours, batched into a digest, or history alone. */
export type Urgency = "urgent" | "normal" | "digest" | "record";

/** The options of @event. */
export interface EventOptions<T> {
  /** The event's kind: dotted lower-camel words, ticket.assigned. A behavior's event is its use's member and its kind: recurrence.missed. */
  readonly kind: string;
  /** The fields of the row its payload carries, as the row holds them when it is raised. */
  readonly payload?: readonly (keyof T & string)[];
  /** The field a notice of it is titled by. */
  readonly title?: keyof T & string;
  /** Its default urgency, normal unless set; a route may override it. */
  readonly urgency?: Urgency;
  /** What it means, for whoever edits the routes. */
  readonly description?: string;
}

/**
 * Declares an event a table's rows raise (D153), repeated per kind.
 *
 * ```ts
 * @event<Ticket>({ kind: "ticket.assigned", payload: ["title", "priority"], title: "title" })
 * @event<Ticket>({ kind: "ticket.overdue", title: "title", urgency: "urgent" })
 * export abstract class Ticket { ... }
 * ```
 *
 * An operation or a handler raises one with the notifier's raise, in the
 * transaction of the change. The notifier routes it by the route rows,
 * holds each recipient to the record's audience when it sends, and
 * delivers it through the channels each recipient's preferences name.
 */
export function event<T>(_options: EventOptions<T>): ClassDecorator {
  return () => {};
}

/** The parts of the notifier, each a class the build fills. */
export type NotifierPart =
  | "inbox"
  | "deliveries"
  | "routes"
  | "preferences"
  | "subscriptions"
  | "channelTrust"
  | "replies"
  | "dispatch";

/**
 * Marks a class as one of the notifier's tables (D153), whose fields the
 * build fills: the inbox, one notice per recipient and event; the
 * deliveries, one row per recipient, channel and attempt; the routes; the
 * preferences; the subscriptions (digests and briefs); the trust of each
 * channel kind; the replies, the audit of what came back; and the dispatch
 * queue, a @queue class the notifier's worker drains.
 *
 * ```ts
 * @notifier("inbox") export abstract class Notice {}
 * @notifier("deliveries") export abstract class Delivery {}
 * @notifier("routes") export abstract class NoticeRoute {}
 * @queue() @notifier("dispatch") export abstract class NoticeWork {}
 * ```
 */
export function notifier(_part: NotifierPart): ClassDecorator {
  return () => {};
}

/**
 * Marks the class that is the table of channel identities (D153), whose
 * fields the build fills: an address on a channel, its kind (email, phone
 * or handle), the principal it resolves to, and how it was confirmed: by
 * a code sent to it and replied with, or entered by hand at a lower trust.
 * A reply from a verified identity runs as its principal.
 *
 * ```ts
 * @channelIdentities()
 * export abstract class ChannelIdentity {}
 * ```
 */
export function channelIdentities(): ClassDecorator {
  return () => {};
}
