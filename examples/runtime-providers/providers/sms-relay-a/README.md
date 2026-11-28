# sms-relay-a

A channel adapter (D159) for an SMS relay that takes JSON. It sends with
`POST <URL>/v1/messages` and a Bearer token (`TOKEN`, a secret), from
`SENDER` when set. The relay posts delivery statuses and replies to the
app's `@webhook` routes, signed with HMAC-SHA256 of the body under the
same token; `status` and `inbound` check the signature and read them.

Its principal's role is `messages.relay`: a relay that calls the app with
its own key may relay messages and do nothing else.

An SMS API that takes JSON and signs its webhooks is the shape this
adapter could front. The example's test talks to a stub.
