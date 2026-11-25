# runtime-providers

A notes app whose environments configure its runtime providers (D159): a
channel adapter for SMS, a credential broker, a sandbox for agent runs, a
model provider and a file processor. Each provider is a package of its own
under `providers/`, which declares in its `package.json` the interface it
implements. The core names none of them, and the app's code names none
either: an environment's config picks each one.

| Path | What it is |
|---|---|
| `providers/sms-relay-a` | A channel adapter for an SMS relay that takes JSON with a Bearer token and posts back signed delivery statuses and replies |
| `providers/sms-relay-b` | A second SMS channel adapter, for a relay of another shape: form posts under an account with basic authentication, send-only |
| `providers/vault-broker` | A credential provider over a password manager's service API: items named by secret reference, `vault://<vault>/<item>/<field>`, scoped to the vaults its config names |
| `providers/local-sandbox` | A sandbox provider that runs each run's instance as a local process: a stub, not an isolation boundary |
| `providers/stub-model` | A model provider whose answers follow from the turn |
| `providers/text-preview` | A file processor with no dependency: the text of plain text and Markdown with an SVG preview, and a thumbnail of a greyscale PGM image |
| `schemas/services/notes-db` | DB: notes, and the notices sent about them |
| `schemas/services/notes-api` | API over notes-db. Its config lists the six packages under `providers`, and `NotesApiConfig` names five slots with `@providers`: `SMS`, `VAULT`, `RUNNER`, `MODEL` and `PREVIEWS`, each with the settings fields after it |
| `schemas/services/notes-stack` | The stack: `Dev`, on this machine, sends SMS through sms-relay-a and names every slot; `Cloudflare` sends through sms-relay-b and names no sandbox or model |
| `src/app.ts` | The server's wiring: `notesProviders`, which reads the providers a loaded config names with `providersFrom`, and `sendNotice` and `receiveReplies`, which send through and read from whichever adapter `SMS` names |
| `test/providers.test.ts` | Each provider against a stub service behind a fetch the test gives: the same notice through either relay with only the config changed, signed replies read as the adapter's own principal, the broker's list and a browser fill with no value in any log or record, a sandbox run and a cancel, a tool call, and text and previews from `fixtures/` |
| `test/injection.typecheck.ts` | What the credential types hold, which tsc checks: an `Injection` has no value to read, and no object literal passes for one |

## What it shows

- **Swapping is config.** `sendNotice` is one function. Dev's env names
  `SMS: "sms-relay-a"` and its URL; Cloudflare's names
  `SMS: "sms-relay-b"`, its URL and its account. Nothing else changes, in
  the stack or in the code.
- **The build checks each package.** `superschematic build` of the stack
  reads each provider's manifest, checks it against its interface's
  contract, and writes each environment's providers into its
  `environment.json`. It refuses an environment whose slot names a
  provider the API does not list, one of another interface, or one whose
  settings are unbound; and, once a feature that needs a channel adapter
  registers, one that leaves `SMS` unset.
- **A credential reaches only the runner.** The broker lists titles and
  sites, and resolves an item into an `Injection` that only the runner
  opens. The run record keeps the reference and the time.
- **A provider acts as itself.** Every call into a provider runs within
  its principal, `extension:<name>`, with the narrow role its package
  declares: sms-relay-a's is `messages.relay`.

## Run it

From the repository root, after `make setup`:

```sh
examples/runtime-providers/scripts/check.sh
```

It builds the HTTP runtime, links it into `node_modules`, type-checks the
example and its providers, and runs the test on Node.js and on Bun. The
stack builds with the installed binary:

```sh
cd examples/runtime-providers
superschematic build schemas/services/notes-stack --out /tmp/notes-out
```

`TestRuntimeProvidersExampleStack` and
`TestRuntimeProvidersExampleRefusals` (`cmd/superschematic`) build it in
CI, with a test feature that stands in for notifications.
