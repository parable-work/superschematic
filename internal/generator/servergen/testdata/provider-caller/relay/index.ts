// A channel adapter that sends nowhere and calls the app with a key of its
// own, its slot's CALLER_KEY (#744). It imports nothing, so the generated
// server links it from this directory as it is: the definition is the
// shape defineProvider checks.
export default {
  name: 'relay',
  implements: 'channel' as const,
  config: { CALLER_KEY: { secret: true, description: 'The key the relay calls the app with.' } },
  role: ['messages.relay'],
  create: () => ({
    kind: 'relay',
    send: async () => ({ providerMessageId: 'relay-1', state: 'sent' as const }),
    health: async () => ({ ok: true }),
  }),
};
