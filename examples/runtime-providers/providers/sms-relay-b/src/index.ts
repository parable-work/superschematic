// A second SMS channel adapter (D159), for a relay of another shape: form
// posts to <URL>/accounts/<ACCOUNT>/send with basic authentication, and no
// callbacks. An environment swaps sms-relay-a for it by its config alone:
// the slot's value and the settings this one reads.
import { defineProvider, type ChannelAdapter } from '@superschematic/http-runtime/providers';

/** How the adapter reaches the relay; globalThis.fetch unless a test gives another. */
export interface SmsRelayBOptions {
  readonly fetch?: typeof fetch;
}

/** The definition, over options.fetch. */
export function smsRelayB(options: SmsRelayBOptions = {}) {
  return defineProvider({
    name: 'sms-relay-b',
    implements: 'channel',
    config: {
      URL: { description: "The relay's base URL." },
      ACCOUNT: { description: 'The account messages are sent under.' },
      TOKEN: { secret: true, description: "The account's auth token." },
    },
    create(settings): ChannelAdapter {
      const base = settings.text('URL').replace(/\/+$/u, '');
      const account = settings.text('ACCOUNT');
      const token = settings.secret('TOKEN');
      const call = options.fetch ?? globalThis.fetch;
      const authorization = () => `Basic ${btoa(`${account}:${token.reveal()}`)}`;
      return {
        kind: 'sms',
        async send(message) {
          const form = new URLSearchParams({ To: message.to, Body: message.body });
          const response = await call(`${base}/accounts/${encodeURIComponent(account)}/send`, {
            method: 'POST',
            headers: { authorization: authorization(), 'content-type': 'application/x-www-form-urlencoded' },
            body: form.toString(),
          });
          if (!response.ok) throw new Error(`sms-relay-b: the relay answered ${response.status}`);
          const sent = (await response.json()) as { sid: string };
          return { providerMessageId: sent.sid, state: 'sent' };
        },
        async health() {
          const response = await call(`${base}/accounts/${encodeURIComponent(account)}`, { headers: { authorization: authorization() } });
          return response.ok ? { ok: true } : { ok: false, detail: `the relay answered ${response.status}` };
        },
      };
    },
  });
}

/** The definition a server links, over the platform's fetch. */
export default smsRelayB();
