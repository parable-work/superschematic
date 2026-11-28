// A stub SMS channel adapter for the deploy smoke (D159): it stands where
// an SMS relay's adapter would, so the smoke runs the notifier (D153) and
// the inbound path (D157) on every target with no relay to sign up for.
//
// send reaches no phone: it answers the message delivered at once, under an
// id of its own, so the delivery row the notifier writes shows the stub
// had it. inbound reads the JSON a relay would post to
// /webhooks/sms/inbound, { messages: [{ id, from, text }] }, which the
// smoke's check posts itself. The stub checks no signature: anyone who
// reaches the route may post as any number. It is for a deployment the
// smoke makes and destroys; a real relay's adapter checks the relay's
// signature first (examples/runtime-providers/providers/sms-relay-a).
//
// It imports nothing, so the server links it wherever it runs: the shapes
// below are the runtime's ChannelAdapter's, which the server's providers.ts
// checks it against when it starts.

interface OutboundMessage {
  readonly to: string;
  readonly body: string;
  readonly idempotencyKey?: string;
}

interface WebhookRequest {
  readonly headers: Readonly<Record<string, string>>;
  readonly body: string;
}

interface InboundText {
  readonly id?: string;
  readonly from?: string;
  readonly text?: string;
  readonly at?: string;
}

let sent = 0;

const stubRelay = Object.freeze({
  name: 'stub-relay',
  implements: 'channel' as const,
  role: ['messages.relay'],
  create() {
    return {
      kind: 'sms',
      async send(message: OutboundMessage) {
        sent += 1;
        return { providerMessageId: `stub-${message.idempotencyKey ?? sent}`, state: 'delivered' as const };
      },
      async health() {
        return { ok: true };
      },
      async inbound(request: WebhookRequest) {
        const body = JSON.parse(request.body) as { messages?: InboundText[] };
        return (body.messages ?? []).map((message, i) => {
          if (typeof message.from !== 'string' || typeof message.text !== 'string') throw new Error('stub-relay: an inbound message needs from and text');
          return {
            providerMessageId: message.id ?? `stub-inbound-${Date.now()}-${i}`,
            from: message.from,
            body: message.text,
            receivedAt: message.at ? new Date(message.at) : new Date(),
          };
        });
      },
    };
  },
});

export default stubRelay;
