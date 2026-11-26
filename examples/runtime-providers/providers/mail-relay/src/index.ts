// A channel adapter for a mail relay that takes JSON (D159, D153). It
// sends with POST <URL>/v1/mail and a Bearer token, from the address its
// config names, and reports no delivery: mail that the relay accepted is
// sent, as most relays leave it. Notifications send email through it,
// password reset codes among them (D81).
import { defineProvider, type ChannelAdapter } from '@superschematic/http-runtime/providers';

/** How the adapter reaches the relay; globalThis.fetch unless a test gives another. */
export interface MailRelayOptions {
  readonly fetch?: typeof fetch;
}

/** The definition, over options.fetch. */
export function mailRelay(options: MailRelayOptions = {}) {
  return defineProvider({
    name: 'mail-relay',
    implements: 'channel',
    config: {
      URL: { description: "The relay's base URL." },
      TOKEN: { secret: true, description: "The relay's API token." },
      FROM: { description: 'The address mail is sent from.' },
    },
    role: ['messages.relay'],
    create(settings): ChannelAdapter {
      const base = settings.text('URL').replace(/\/+$/u, '');
      const token = settings.secret('TOKEN');
      const from = settings.text('FROM');
      const call = options.fetch ?? globalThis.fetch;
      return {
        kind: 'email',
        async send(message) {
          const response = await call(`${base}/v1/mail`, {
            method: 'POST',
            headers: {
              authorization: `Bearer ${token.reveal()}`,
              'content-type': 'application/json',
              ...(message.idempotencyKey ? { 'idempotency-key': message.idempotencyKey } : {}),
            },
            body: JSON.stringify({ from, to: message.to, subject: message.subject ?? '', text: message.body }),
          });
          if (!response.ok) throw new Error(`mail-relay: the relay answered ${response.status}`);
          const sent = (await response.json()) as { id: string };
          return { providerMessageId: sent.id, state: 'sent' };
        },
        async health() {
          const response = await call(`${base}/v1/health`, { headers: { authorization: `Bearer ${token.reveal()}` } });
          return response.ok ? { ok: true } : { ok: false, detail: `the relay answered ${response.status}` };
        },
      };
    },
  });
}

/** The definition a server links, over the platform's fetch. */
export default mailRelay();
