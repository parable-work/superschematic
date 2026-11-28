# chat-bridge

A channel adapter (D159) for a messaging network whose inbound side is a
bridge. The bridge is a process that connects outward: to the network,
for what it receives, and to the app's inbound route, to relay it. Its
host needs no open port.

| Part | What it does |
|---|---|
| `chatBridge()` | The adapter the app links, kind `chat`. `send` posts to `<URL>/v1/messages` with the network's `TOKEN`. `inbound` reads what the bridge relayed, once the request bears `CALLER_KEY`. |
| `runBridge()` | The bridge's loop: each message the network received, posted to `<api>/webhooks/<slot>/inbound` with the relay key. |
| `pollNetwork()` | The bridge's connection to the network: a long poll of `<URL>/v1/events`, which yields each message. |

Its principal's role is `messages.relay`. The relay key is the slot's
`CALLER_KEY` (`<SLOT>_CALLER_KEY`, a `Secret<string>` of the API's
config, #744), and the generated server resolves it to that principal
(`extensionAuthenticator`, outermost), so the key may relay messages and
do nothing else. Senders are handles: a channel identity of
kind `handle` names a party or a user on the network (D157).

A chat network's bot API with a gateway connection, such as a team chat
service's, is the shape this adapter could front. The example's tests
talk to a stub.
