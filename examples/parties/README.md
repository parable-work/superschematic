# parties

A jobs app whose contractors are outside the user table, and who send in
their reports by text or email, photos attached (D157). The
[Outside parties and inbound messages](https://parable-work.github.io/superschematic/guides/parties/)
guide covers it in full.

| Path | What it is |
|---|---|
| `schemas/services/jobs-db` | DB, Postgres: `Contact` is a `@party`, a principal that acts through its grants (`Grant`, D156); `Job` is what a grant names and `Report` what a contractor writes on one, with its files; `Conversation` and `Message` are the conversations, a message holding references and files; `RunRequest` is the queue a granted contact's message puts a run request on; the notifier's tables (D153) and `ChannelIdentity`, the addresses everyone is reached at and writes from |
| `schemas/services/jobs-api` | API over `jobs-db`: `JobsApiConfig` names two channel slots, `SMS` and `MAIL`, and the deployment's `INBOUND_ADDRESS`; `Notify` drains the notifier's queue and `Runs` the run requests |
| `server/server.ts` | The wiring: the inbound path over the providers the config names, the relays' callbacks, sending to a contact, the timeline and triage, and the workers' handlers |

The channels are the SMS relay and the mail relay of
`examples/runtime-providers`. The SMS relay fetches a text's photo and
reads STOP and START; the mail relay passes on the SPF, DKIM and DMARC
results of the mail it received.

## What it shows

- **One path for every inbound message.** A relay's callback goes to
  `inbound.inbound(slot, request)`. The sender's address resolves through
  the channel identities. A user's text is a reply, which the notifier
  runs as the user. A text from a contact with an active grant is a row of
  the contact's conversation, with its photo, and a run request
  `{ trigger: "inbound", party, message, grant }`. A contact with no grant
  is on its timeline, and the message is held in triage. An unknown number
  is held in triage.
- **A grant that ended reaches nothing.** Once a contact's grants have all
  ended, its number may belong to someone else. The same text is held in
  triage as an unknown sender's until the number is verified again.
- **Email proves its sender or is no one's.** An email whose DMARC did not
  pass, or whose SPF or DKIM failed, is treated as unknown. A reply
  threads to the conversation its `In-Reply-To` names, which also tells
  apart two contacts who share an address.
- **Messages are rows.** `Runs` stands in for the agent dispatcher, which
  `examples/agent-orchestration` hands its run requests to (D158): it
  writes a report on the granted job as the contact and files the message
  onto it. The photo joins the report with no second upload,
  and the job and the report each list the message in their backlinks.
- **Sending to a contact is a permission.** `parties.message` lets the
  office write, held to the contact's identity, a rate limit and an
  approval policy. A contact who texts STOP is sent nothing after.

## Where it runs

`TestPartiesExampleLoads` (`internal/loader/conversations`) loads these
schemas. The same scenario, under the fixture name `fixture-parties-db`,
runs end to end against Postgres in `TestPartiesEndToEnd`
(`internal/generator/tsgen`), through the two relays over stand-in
services.

The inbound path is the TypeScript runtime's, on Postgres: the Go and
Rust servers have no inbound path or party principals yet, and the
generated server does not build the inbound path into its `Deps`, which
`server/server.ts` wires.
