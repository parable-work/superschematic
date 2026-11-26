/*
The channel adapter (D159): what notifications (D153) send through. An
adapter sends a message and reports its health. One whose service posts
delivery statuses and inbound messages back reads them from the request a
@webhook route received (status, inbound): the route is the app's, so the
service's callback runs through the same operation, permissions and audit
as any other caller. Core keeps only an in-app inbox; email, SMS and
messaging bridges are adapters.
*/

/** Where a delivery stands, as the adapter's service reports it. */
export type DeliveryState = 'queued' | 'sent' | 'delivered' | 'read' | 'failed';

/** A message to send. */
export interface OutboundMessage {
  /** The recipient's address on the channel: a phone number, an email address, a chat handle. */
  readonly to: string;
  readonly body: string;
  /** A subject, for a channel that shows one. */
  readonly subject?: string;
  /** The reference number a reply names to say what it answers (D153). */
  readonly reference?: string;
  /** A key the adapter passes to its service so a retried send sends once. */
  readonly idempotencyKey?: string;
}

/** What send answers. */
export interface SendResult {
  /** The service's id of the message, which its statuses and replies name. */
  readonly providerMessageId: string;
  readonly state: DeliveryState;
}

/** One delivery status a service reported. */
export interface DeliveryStatus {
  readonly providerMessageId: string;
  readonly state: DeliveryState;
  readonly at: Date;
  /** Why a delivery failed, in the service's words. */
  readonly detail?: string;
}

/** One message a service received for the app. */
export interface InboundMessage {
  /** The sender's address on the channel. */
  readonly from: string;
  readonly body: string;
  readonly receivedAt: Date;
  readonly providerMessageId: string;
  /** The id of the message it replies to, when the service threads replies. */
  readonly inReplyTo?: string;
}

/** A request a @webhook route received from the adapter's service, as the route read it. */
export interface WebhookRequest {
  /** The request's headers, names in lower case. */
  readonly headers: Readonly<Record<string, string>>;
  /** The raw body, which a signature covers. */
  readonly body: string;
}

/** What health answers. An adapter that is not ok is skipped, and the others carry the alert that it is down. */
export interface ChannelHealth {
  readonly ok: boolean;
  readonly detail?: string;
}

/** A channel adapter. */
export interface ChannelAdapter {
  /** The kind of channel it sends on: "sms", "email", "chat". A channel kind's trust level is data (D153). */
  readonly kind: string;
  send(message: OutboundMessage): Promise<SendResult>;
  health(): Promise<ChannelHealth>;
  /** The delivery statuses a callback request carries; a service that reports none leaves it out. */
  status?(request: WebhookRequest): Promise<readonly DeliveryStatus[]>;
  /** The messages an inbound request carries; a send-only service leaves it out. */
  inbound?(request: WebhookRequest): Promise<readonly InboundMessage[]>;
}
