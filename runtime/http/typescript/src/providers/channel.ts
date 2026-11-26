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
  /** The message's id in the channel's threads, for a channel that threads (D157): an email's Message-ID header, which a reply's In-Reply-To names. */
  readonly messageId?: string;
  /** The id of the message it answers: an email's In-Reply-To header. */
  readonly inReplyTo?: string;
  /** The ids of the messages of its thread before it: an email's References header. */
  readonly references?: readonly string[];
  /** Where a reply goes: the deployment's inbound address, for email (D157). */
  readonly replyTo?: string;
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
  /** The id of the message it replies to, when the service threads replies: an email's In-Reply-To, or the service's id of the text it answers. */
  readonly inReplyTo?: string;
  /** The message's own id in the channel's threads: an email's Message-ID (D157). */
  readonly messageId?: string;
  /** The ids of the messages of its thread before it: an email's References. */
  readonly references?: readonly string[];
  /** The addresses it was sent to: the deployment's inbound address, for an email. */
  readonly to?: readonly string[];
  /** Its subject, for a channel that has one. */
  readonly subject?: string;
  /**
   * What the service found of an email's authentication (D157), each
   * result as the service reports it: pass, fail, softfail, neutral,
   * none, temperror or permerror. The adapter reports; the core decides,
   * and an email whose DMARC did not pass, or whose SPF or DKIM failed, or
   * that reports none, is from an unknown sender.
   */
  readonly authentication?: InboundAuthentication;
  /** The files it came with, which the adapter fetched from its service (a text's photo, an email's attachments). */
  readonly attachments?: readonly InboundAttachment[];
  /**
   * An opt-out or opt-in keyword of the channel the adapter read (D157):
   * out for STOP and its kin, in for START. The core records it on the
   * address's identities and sends nothing to an address that opted out.
   */
  readonly optOut?: 'out' | 'in';
}

/** An email's authentication results, as an inbound adapter's service reports them (D157). */
export interface InboundAuthentication {
  readonly spf?: string;
  readonly dkim?: string;
  readonly dmarc?: string;
}

/** A file an inbound message came with, its bytes fetched by the adapter. */
export interface InboundAttachment {
  readonly name: string;
  readonly mediaType: string;
  readonly bytes: Uint8Array;
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
