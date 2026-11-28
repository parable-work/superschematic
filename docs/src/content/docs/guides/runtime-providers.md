---
title: Runtime providers
description: Plug a channel adapter, a credential provider, a sandbox, a model provider or a file processor into a running app as a package of its own, and choose one per environment in its config.
sidebar:
  order: 5.68
---

A running app calls five kinds of outside service: something that sends
a message, something that holds credentials, something that runs a
sandboxed job, a model, and something that reads files. The core defines
an interface for each and calls it. Each service it talks to is a
provider: a package of its own that implements one interface. The core
depends on none of them.

| Interface | What the core calls | Type in `@superschematic/http-runtime/providers` |
| --- | --- | --- |
| `credentials` | `list` (titles and sites only), `resolve` into a sealed injection the runner applies | `CredentialProvider` |
| `channel` | `send`, `health`, and the delivery statuses (`status`) and inbound messages (`inbound`) a `@webhook` route received | `ChannelAdapter` |
| `sandbox` | `start`, `cancel`, `status` and `cost` of a run's instance | `SandboxProvider` |
| `model` | `complete` a turn with tools, with its usage, and the `providerState` each completion keeps for the turns after it ([agent runs](/superschematic/guides/agent-runs/#each-turn)) | `ModelProvider` |
| `files` | `extractText` and `preview` | `FileProcessor` |

| Built | Not built yet |
| --- | --- |
| the five interfaces in the TypeScript HTTP runtime, `defineProvider`, and `providersFrom`, which reads the providers an environment configures | |
| the same in the Rust HTTP runtime, and a provider crate whose `Cargo.toml` declares it ([In Rust](#in-rust)) | |
| the same in the Go HTTP runtime's `providers` package, and a Go module in the provider's directory ([In Go](#in-go)) | |
| a provider package's manifest in its `package.json`, which the build checks against its interface's contract; a package listed by its directory or by its npm name, in build-all's dependency graph and its cache key | a TypeScript server that imports an npm-named provider by its name; it imports the directory the name resolved to |
| `@providers` on an API's `@envVars` class, and the API config's `providers`; a generated TypeScript, Go and Rust server that links the packages and builds `Providers` in its `Deps` | |
| the stack's checks: each slot names a provider the API lists, of its interface, with its settings bound; a feature that needs an interface no slot fills is refused, notifications among them ([events and notifications](/superschematic/guides/notifications/)), agents, which need a sandbox and a model ([agent runs](/superschematic/guides/agent-runs/)), and search over attachments' text, which needs a file processor | a check for `credentials`: an agent's credentials are rows of its definition, which the build does not read |
| the file processor's pass over attachments' `pending` (`processFiles`, a generated API's `processPendingFiles`), whose text search indexes ([References and files on any row](/superschematic/guides/database-tables/)), in TypeScript, in Go (`fileprocessing.Process`, a generated API's `ProcessPendingFiles`) and in Rust (`orm::file_processing::process_files`) | a generated Rust API's own call of the pass |
| credentials that never reach the caller: a sealed `Injection`, `useCredential` and the record of each use | |

[D159](https://github.com/parable-work/superschematic/issues/608)
records the design and what is left. `examples/runtime-providers` is the
worked example this page quotes. Its sandbox and its model are stubs, so
it runs with no account: [set up your own
provider](#set-up-your-own-provider) says how to write one for the
service an app uses.

## Write a provider package

A provider package exports a definition and declares the same in its
`package.json`. The definition says which interface it implements, the
settings it reads and the role of its principal, and `create` makes the
provider from those settings:

```ts
import { defineProvider, type ChannelAdapter } from "@superschematic/http-runtime/providers";

export default defineProvider({
  name: "sms-relay-b",
  implements: "channel",
  config: {
    URL: {},
    ACCOUNT: {},
    TOKEN: { secret: true },
  },
  create(settings, context): ChannelAdapter {
    const token = settings.secret("TOKEN"); // a SecretValue, never a string
    return {
      kind: "sms",
      async send(message) { /* POST to settings.text("URL") */ },
      async health() { return { ok: true }; },
    };
  },
});
```

The `package.json` declares it under `superschematic.provider`. The build
reads this, not the code:

```json
{
  "name": "@example-providers/sms-relay-b",
  "superschematic": {
    "provider": {
      "name": "sms-relay-b",
      "implements": "channel",
      "operations": ["send", "health"],
      "config": { "URL": {}, "ACCOUNT": {}, "TOKEN": { "secret": true } }
    }
  }
}
```

The build refuses a manifest that:

- implements no interface the core defines;
- leaves out an operation its interface requires, or lists one it does
  not define (`operations` left out means the required ones);
- has a name that is not lower-case words joined by hyphens, or a config
  key that is not upper-case letters, digits and underscores;
- gives its principal a permission that names a whole area, such as
  `messages`, rather than one capability in it, such as `messages.relay`;
- has a key the manifest does not take.

`checkProviderPackage(packageJson, definition, provider)` holds the two
to each other in the package's own test, so the build and the runtime read
one provider.

## Use providers in an API

The API's config lists the packages it may use, each by its directory
relative to the service's directory or by its npm name:

```ts
export default defineConfig({
  name: "notes-api",
  kind: SchemaKind.API,
  dependencies: [NotesDb],
  providers: ["../../../providers/sms-relay-a", "@example-providers/sms-relay-b"],
  outputs: { /* ... */ },
});
```

An entry that starts with `.` or `/` is a directory, and it starts with
`./` or `../`. Any other entry is a package's npm name, which resolves as
Node.js resolves it: from the first `node_modules` in the service's
directory or one above it that holds the package. The build refuses a
package no `node_modules` holds, naming each place it looked, and one
whose `package.json` gives another name.

A change to a listed package's manifest (its `package.json`, `Cargo.toml`,
`superschematic.json` or `go.mod`) rebuilds the APIs that list it and the
stacks that deploy them, and `.deps.json` lists each provider package
with an edge from the API package that links it.

Its `@envVars` class names a slot per provider it uses with `@providers`.
A slot is a string field whose value, in each environment, names the
provider. The provider reads its settings from the fields named after the
slot: slot `SMS` and key `URL` read `SMS_URL`. A secret setting is a
`Secret<string>` field:

```ts
@envVars
@providers({ SMS: "channel" })
export abstract class NotesApiConfig {
  SMS: Nullable<string>;
  SMS_URL: Nullable<string>;
  SMS_ACCOUNT: Nullable<string>;
  SMS_TOKEN: Nullable<Secret<string>>;
}
```

The load refuses a slot that is no field of the config, a secret, or not
a string, and a slot of an interface none of the listed packages
implements.

## Choose a provider per environment

A stack binds a slot as it binds any field, in the deployable's `env`.
`stack secrets set` enters the secret settings of the provider each slot
names. A secret setting that no named provider reads, of a slot the
environment leaves unset or of a provider it does not name, is no secret
of that environment: `stack secrets set` does not ask for it, and a
deploy does not wait on it. A required `Secret<T>` field stays a secret
of every environment, since the config needs it at start.

```ts
@environment({ target: "local", settings: [{ of: NotesApi, env: { SMS: "sms-relay-a", SMS_URL: "http://127.0.0.1:4310" } }] })
export abstract class Dev {}

@environment({ target: "cloudflare", /* ... */ settings: [{ of: NotesApi, env: { SMS: "sms-relay-b", SMS_URL: "https://relay.example.test", SMS_ACCOUNT: "AC100" } }] })
export abstract class Cloudflare {}
```

Swapping one provider for another is this change and nothing else. Each
deployable's entry in `environment.json` lists the provider every slot
names:

```json
"providers": [
  { "slot": "SMS", "interface": "channel", "provider": "sms-relay-b", "package": "@example-providers/sms-relay-b", "service": "notes-api" }
]
```

The stack refuses an environment whose slot:

- names a provider the API does not list, or one of another interface;
- is bound to a parameter, since the build checks the provider by name;
- names a provider whose setting has no field in the config, a field of
  the wrong secrecy, or no binding in the environment.

## Features that need a provider

A feature that needs a provider registers itself with the interface it
needs, and the stack refuses an environment that runs an API using the
feature while no slot of that interface is set:

```
environment Cloudflare runs server notes-api, whose API notes-api uses
notices, which needs a channel adapter, and the environment configures
none: set SMS to one of sms-relay-a, sms-relay-b in its env (D159)
```

An extension or a core feature registers with
`Registry.RegisterRuntimeFeature`, naming the feature, the interface and
a `Uses` function over the API's schema and the databases it connects to.

## Call a provider at run time

`providersFrom` reads the slots, the loaded config and the definitions
the server links, and answers `Providers`:

```ts
const providers = providersFrom({ slots: { SMS: "channel" }, config: loadEnvConfig(), definitions: [smsRelayA, smsRelayB] });
const sms = await providers.channel("SMS");
await sms.send({ to: "+15550100", body: "Review moved to Thursday", reference: "142" });
```

The generated TypeScript server does this itself. The API package of an
API with slots holds `providers.ts`, which imports each listed package's
default export by its directory, and whose `providersOf(config, log)`
calls `providersFrom` over the API's slots and those definitions; every
entrypoint puts its answer in `deps.providers`, and the notifier sends on
its channel slots, the agent dispatcher runs on its sandbox and model
slots ([agent runs](/superschematic/guides/agent-runs/)). A server's image
holds each package listed by its directory: its Dockerfile's context takes
the directory in, and its last stage copies it beside the workspace. A
listed provider
of a Rust crate alone is no package the server links: a slot that names
it is refused at start.

It refuses at start a config that names a provider the server does not
link, one of another interface, or leaves a setting unset, naming every
problem and never a value. A provider is made on first use. Each call
into it runs within its own principal, of kind `extension`, whose subject
is `extension:<name>` and whose permissions are its role: what it writes
through an ORM opened with no user is recorded as the provider.
`extensionAuthenticator` authenticates a provider that calls the app with
its own key, such as a messaging bridge that connects outward and relays
replies, as that principal. `examples/runtime-providers`'s `chat-bridge`
is one: its loop relays what a messaging network received to the slot's
inbound route with a key that can only relay messages
([a messaging bridge](/superschematic/guides/parties/#a-messaging-bridge)).

## Credentials never reach the caller

A credential provider gives the core no value. `list` answers titles and
sites. `resolve` answers an `Injection`, which holds the value sealed:
no member of it returns the value, its JSON and its string form show the
reference and the target alone, and a provider makes one only from a
`SecretValue`. `useCredential` resolves a reference, hands the value to
the runner that uses it, a browser fill or a request header, and answers
the use, which is what a run record keeps:

```ts
const use = await useCredential({
  provider: await providers.credentials("VAULT"),
  providerName: "vault-broker",
  ref: "vault://ops/reports-login/password",
  target: { kind: "fill", site: "https://reports.example.test", selector: "#password" },
  runner: browser, // fill(selector, value) and setHeader(name, value)
  record: entry => runRecord.credentials.push(entry), // { ref, provider, target, usedAt }
});
```

An injection for another reference or another target is refused before
the runner sees it.

## In Rust

The Rust HTTP runtime's `providers` module has the same five interfaces,
as traits, and the same `providers_from`, `use_credential` and
`ExtensionAuthenticator`. A provider is a crate. Its `Cargo.toml` declares
the provider under `[package.metadata.superschematic.provider]`, with the
keys a `package.json` declares, and its root exports `definition()`:

```toml
[package.metadata.superschematic.provider]
name = "sms-relay-a"
implements = "channel"
operations = ["send", "health", "status", "inbound"]
role = ["messages.relay"]

[package.metadata.superschematic.provider.config]
URL = { description = "The relay's base URL." }
TOKEN = { secret = true }
```

```rust
pub fn definition() -> ProviderDefinition {
    ProviderDefinition::new("sms-relay-a", RuntimeInterface::Channel, |settings, _context| {
        Ok(ProviderInstance::Channel(Arc::new(Adapter {
            base: settings.text("URL")?.to_owned(),
            token: settings.secret("TOKEN")?,
        })))
    })
    .with_setting("URL", ProviderSettingSpec::plain())
    .with_setting("TOKEN", ProviderSettingSpec::secret())
    .with_role(["messages.relay"])
    .with_operations(["status", "inbound"])
}
```

The API's config lists the crate's directory, as it lists a package's;
a directory may hold both, which then declare one provider. A Rust server
refuses a listed provider with no crate. The generated server depends on
each listed crate by path, its `src/providers.rs` holds the slots and
`from_config`, and its entrypoint fills `Deps.providers`, holding each
crate's definition to the manifest the build read:

```rust
let sms = deps.providers.channel("SMS")?;
sms.send(&OutboundMessage { to: "+15550100".into(), body: "Review moved to Thursday".into(), ..Default::default() }).await?;
```

The file processor's pass is `orm::file_processing::process_files`,
over the repositories' `attachments()` on Postgres or on a SQLite file,
which a job runs on a schedule (#603):

```rust
let orm = Repositories::new(&client)?.as_system();
let pass = FileProcessingOptions::new(&deps.providers, deps.uploads.as_ref());
let run = process_files(&orm.attachments()?, &pass).await?;
```

Rust has no ambient caller, so a provider does not run within its
principal: its context carries it, and `deps.providers.principal_of(slot)`
gives it to the app. A provider that writes through the app's
repositories opens them `with_access(AccessCaller::of(Some(&principal)))`.
`examples/runtime-providers/rust` ports a stub of each interface. D185
records the Rust design.

## In Go

A Go server reads the same slots through the Go HTTP runtime's
`providers` package (D184). A Go provider is a Go module in the
provider's directory, the one the API's config lists: its `go.mod` names
the module, and the package at its root exports `Definition()`, which
`providers.MustDefine` checks as `defineProvider` does. The directory
declares the provider once: in its `package.json`, beside the TypeScript
package, or, for a provider of Go alone, in a `superschematic.json` whose
`provider` object is the `package.json`'s `superschematic.provider`.
`examples/runtime-providers/providers` holds a Go module beside each
TypeScript package.

```go
package smsrelayb

func Definition() providers.Definition {
	return providers.MustDefine(providers.Definition{
		Name:       "sms-relay-b",
		Implements: providers.Channel,
		Config:     map[string]providers.SettingSpec{"URL": {}, "ACCOUNT": {}, "TOKEN": {Secret: true}},
		Create: func(ctx context.Context, settings providers.Settings, pc providers.Context) (any, error) {
			token, err := settings.Secret("TOKEN") // a SecretValue, never a string
			// ...
			return &adapter{token: token}, nil
		},
	})
}
```

The provider implements the Go interface of its contract
(`CredentialProvider`, `ChannelAdapter`, `SandboxProvider`,
`ModelProvider` or `FileProcessor`), an optional operation an interface
of its own (`ChannelStatusReader`, `ChannelInboundReader`, and a model's
`ModelEmbedder`). In the package's own test, `providers.ReadManifest(".")`
reads the declaration and `providers.CheckManifest(manifest,
Definition(), provider)` holds it to the code.

The generated server of an API with slots writes `providers.go`, whose
`ProvidersOf(config, logger)` reads the providers its config names, over
the definitions of the modules the API lists, and the entrypoint puts
them in `Deps.Providers`; `go.mod` requires each module and replaces it
with its directory. A Go server is refused a listed provider whose
directory holds no Go module.

```go
sms, err := deps.Providers.Channel(ctx, "SMS")
_, err = sms.Send(ctx, providers.OutboundMessage{To: "+15550100", Body: "Review moved to Thursday"})

use, err := providers.UseCredential(ctx, providers.UseCredentialInput{
	Provider: broker, ProviderName: "vault-broker", Ref: ref,
	Target: providers.HeaderTarget("https://reports.example.test", "Authorization"),
	Runner: runner, // Fill(ctx, selector, value) and SetHeader(ctx, name, value)
})
```

The file processor's pass is `fileprocessing.Process` (#603). An API
whose database declares `@attachments` and whose config names a `files`
slot gets `ProcessPendingFiles` in its `wiring.go`, over `Deps.DB`,
`Deps.Providers` and the bucket `Wiring.Files` names, which a job runs
on a schedule:

```go
run, err := api.ProcessPendingFiles(ctx, deps, api.Wiring{Files: fileprocessing.Options{Bucket: deps.Uploads}})
```

Every call into a provider is made with a context of its own principal,
kind `extension`, and its role, which the server binds to the ORM too
(`orm.WithPrincipal`), so a provider reads what everyone may read.
`providers.ExtensionAuthenticator` authenticates a provider that calls
the app with its key as that principal, and composes with
`session.GrantAuthenticator`. An `Injection` has no exported field, so
no other package makes one that holds a value, and its JSON and every
`fmt` verb print its reference and target alone.

## Set up your own provider

The examples run with no account anywhere. Their sandbox is
`local-sandbox`, which runs each instance as a process on the machine and
isolates nothing, and their model is `stub-model`, whose answers follow
from the turn. The channel adapters, the vault broker and the file
processor talk to stand-ins of their services in the examples' tests. An
app that wants a real service writes a provider package for it: a sandbox
over a container runtime, a model provider over a model API, an adapter
over the service a message goes out through.

No skeleton package ships, since each stub is one: a small package of one
interface that type-checks, in TypeScript, Go and Rust, whose test holds
its manifest to its code. Start from the stub of your interface, under
`examples/runtime-providers`:

| Interface | TypeScript and Go | Rust |
| --- | --- | --- |
| `sandbox` | `providers/local-sandbox` | `rust/local-sandbox` |
| `model` | `providers/stub-model` | `rust/stub-model` |
| `channel` | `providers/sms-relay-a` (statuses and replies by callback), `providers/sms-relay-b` (send only); in TypeScript alone, `providers/mail-relay` (email) and `providers/chat-bridge` (a bridge that connects outward) | `rust/sms-relay-a`, `rust/sms-relay-b` |
| `credentials` | `providers/vault-broker` | `rust/vault-broker` |
| `files` | `providers/text-preview` | `rust/text-preview` |

### The steps

1. **Copy the stub's directory** into your project and rename it. Write
   the provider in the language of the server that links it, or in each:
   a Go server refuses a listed provider whose directory holds no Go
   module, and a Rust server one with no crate.
   - TypeScript: an npm package whose default export is the definition.
     It imports `@superschematic/http-runtime/providers` from the copy the
     server uses (a peer dependency, once the runtime is published), so
     an `Injection` it seals is one the runtime opens.
   - Go: a module whose root package exports `Definition()`, in the same
     directory as the `package.json`, or alone with a
     `superschematic.json`. Drop the stub's `replace` block, which points
     into this checkout.
   - Rust: a crate whose root exports `definition()`. Replace the stub's
     `path` dependency on the runtime, which points into this checkout,
     with the runtime your server uses.
2. **Name it and declare its settings**, in the manifest the build reads
   and in the definition the server links: the `package.json`'s
   `superschematic.provider` (or the `superschematic.json`'s `provider`),
   or the `Cargo.toml`'s `[package.metadata.superschematic.provider]`.
   Give it a name of lower-case words joined by hyphens, `implements` its
   interface, a `config` key per setting, `secret` on a key or a token,
   `optional` on a setting it can do without, and a `role` only when it
   calls the app. Name it and its settings after what they are
   (`container-sandbox`, `URL`, `TOKEN`), not after a service.
3. **Implement the members** of its interface, below. Read a secret
   setting with `settings.secret(...)` (`Secret` in Go), which answers a
   secret value, and reveal it only where the request to the service is
   made. Take the HTTP client as a parameter, as `sms-relay-a` does (a
   `fetch` in TypeScript, an `*http.Client` in Go, a trait object in
   Rust), so a test can put a stand-in of the service behind it.
4. **Test the package** on its own. Copy the stub's test that holds the
   manifest to the code: `checkProviderPackage(packageJson, definition,
   provider)` in TypeScript, as the example's first case does, and
   `providers.ReadManifest(".")` with `providers.CheckManifest` in Go
   (`provider_test.go`). A Rust server holds each crate's `definition()`
   to its manifest as it starts. Then run the stub's own cases with yours
   in its place: in `examples/runtime-providers/test/providers.test.ts`,
   "the local sandbox runs an instance..." and "the stub model calls a
   tool...". Run them against a stand-in of the service, and against the
   service itself only when the test's environment holds a key for it.
5. **List it in the API's providers**, by its directory or its npm name
   ([use providers in an API](#use-providers-in-an-api)). Keep the stub
   listed for the environments that still use it.
6. **Declare its settings in the API's `@envVars` class**: a field per
   setting, named after the slot and the key, a secret one a
   `Nullable<Secret<string>>`. Keep each field nullable: a required
   `Secret<T>` is a secret of every environment, the stub's too.
7. **Name it in one environment.** Set the slot to its name in that
   environment's `env`, with its plain settings, and leave every other
   environment on the stub. `superschematic stack secrets set
   <environment>` asks for its secret settings in that environment alone.
   `superschematic build` of the stack checks the manifest against the
   interface's contract, and the settings' fields and their bindings
   ([choose a provider per environment](#choose-a-provider-per-environment)).
8. **Test it end to end**, as [below](#test-it-end-to-end).

For an API whose config names `SANDBOX` and `MODEL`, as
`examples/agent-orchestration`'s does, steps 5 to 7 come to this:

```ts
providers: ["../../../providers/local-sandbox", "../../../providers/container-sandbox",
            "../../../providers/stub-model", "../../../providers/model-api"],
```

```ts
@envVars
@providers({ SANDBOX: "sandbox", MODEL: "model" })
export abstract class WorkApiConfig {
  SANDBOX: Nullable<string>;
  SANDBOX_WORKDIR: Nullable<string>;       // local-sandbox's
  SANDBOX_URL: Nullable<string>;           // container-sandbox's
  SANDBOX_TOKEN: Nullable<Secret<string>>;
  MODEL: Nullable<string>;
  MODEL_MODEL: Nullable<string>;           // both models'
  MODEL_URL: Nullable<string>;             // model-api's
  MODEL_KEY: Nullable<Secret<string>>;
}
```

```ts
// Dev, on this machine, keeps the stubs.
{ of: WorkApi, env: { SANDBOX: "local-sandbox", MODEL: "stub-model" } }

// Staging names the real ones.
{ of: WorkApi, env: {
  SANDBOX: "container-sandbox", SANDBOX_URL: "https://containers.example.test",
  MODEL: "model-api", MODEL_URL: "https://models.example.test", MODEL_MODEL: "large",
} }
```

```sh
superschematic stack secrets set Staging   # SANDBOX_TOKEN and MODEL_KEY
```

Dev is asked for neither secret, since no provider its slots name reads
them.

### A sandbox provider

The dispatcher starts one instance per run, polls it until it ends, reads
what it cost, and cancels it when the run is paused or its agent's budget
runs out ([agent runs](/superschematic/guides/agent-runs/#start-a-run)).
Its members are `start`, `cancel`, `status` and `cost`: `SandboxProvider`
in TypeScript, the same in Go with a `context.Context` first (`Start`,
`Cancel`, `Status`, `Cost`), and the `SandboxProvider` trait in Rust,
which the definition answers as `ProviderInstance::Sandbox`.

| Member | What it must do |
| --- | --- |
| `start(spec)` | Start a fresh instance of the spec's `image` (a container runtime) or `command` (a process), with `env` as plain variables and `secrets` through the runtime's own secret store, held to `limits`. Answer its `id` and `startedAt`. Fail when it cannot start: the dispatcher revokes the run's token, and Retries starts the run again after its backoff. |
| `cancel(id)` | Stop the instance. Cancelling one that has stopped does nothing. |
| `status(id)` | Answer `starting`, `running`, `succeeded`, `failed` or `cancelled`, with the exit code and a detail. The dispatcher polls it every few seconds and heartbeats the run's Lease while the instance runs. `succeeded` ends the run done, `cancelled` cancelled, and `failed` failed, with the detail. |
| `cost(id)` | Answer its wall seconds and CPU seconds so far, and the amount when the runtime says. The dispatcher reads it after the instance ends, and the agent's Budget counts the wall seconds. |

What the dispatcher puts in the spec:

- `runId`, and the `image` and `command` of the agent's definition;
- `env`: `AGENT_RUN_ID`, `AGENT_ENDPOINT`, `AGENT_NAME`,
  `AGENT_DEFINITION_VERSION`, `AGENT_CREDENTIALS` (references alone), and
  what the app's `sandboxSpec` adds;
- `secrets`: `AGENT_RUN_TOKEN`, and what the app's `sandboxSpec` adds,
  such as the credential broker's token;
- `limits`: `wallSeconds` always, from the definition's limits, and
  `cpuSeconds` and `memoryMb` when the definition sets them.

The instance runs the runtime's side of a run: `runAgentInstance` in
TypeScript, `providers.RunAgentInstance` in Go, `run_agent_instance` in
Rust, so the image holds a program that calls it. It calls
`AGENT_ENDPOINT`, so the instance needs a route out to the app and no
open port. It reports the outcome and exits; the provider reports an exit
of 0 as `succeeded`.

The provider must:

- take the id alone in `cancel`, `status` and `cost`, which may come from
  another process than the one that started it: on Workers the
  dispatcher's queue drains at each Cron firing. Keep nothing in memory a
  later call needs, and ask the runtime by the id. `local-sandbox` keeps
  its processes in memory, which serves one machine alone;
- reach the runtime over HTTP, not a process API, so it runs wherever the
  dispatcher runs, Workers included;
- stop an instance at its `wallSeconds`, and start each run on a fresh
  instance, carrying nothing from one run to the next.

It must never put a secret's value in a log line, an error message, the
instance's name or labels, its plain `env`, or its id. The dispatcher logs
a failed start's message, so an error quotes no request that carried a
secret. Log the secrets' names, as `local-sandbox` does.

The build holds the manifest to the contract: `implements: "sandbox"`,
and the operations `start`, `cancel`, `status` and `cost`, none of them
optional. The TypeScript and Go servers check, when they first make the
provider, that it has each one; in Rust the trait does. The stack refuses
an environment that runs an API whose database declares the agents'
tables while no sandbox slot is set.

```ts
import { defineProvider, type SandboxProvider } from "@superschematic/http-runtime/providers";

export default defineProvider({
  name: "container-sandbox",
  implements: "sandbox",
  config: { URL: {}, TOKEN: { secret: true } },
  create(settings, context): SandboxProvider {
    const url = settings.text("URL");
    const token = settings.secret("TOKEN"); // revealed into the runtime's request alone
    return {
      async start(spec) { throw new Error("not implemented"); }, // run spec.image with spec.env, spec.secrets and spec.limits
      async cancel(id) { throw new Error("not implemented"); },  // stop it; nothing when it has stopped
      async status(id) { throw new Error("not implemented"); },  // the runtime's state of id
      async cost(id) { throw new Error("not implemented"); },    // its wall and CPU seconds
    };
  },
});
```

### A model provider

The dispatcher completes each turn of a run in the app, not in the
sandbox, so the model's key stays in the app
([each turn](/superschematic/guides/agent-runs/#each-turn)). The member
is `complete(turn)`: `ModelProvider` in TypeScript, `Complete(ctx, turn)`
in Go, and the `ModelProvider` trait in Rust, which the definition
answers as `ProviderInstance::Model`. `embed`, for search's re-ranking
(D155), is optional and no operation of the contract, so the manifest
does not list it: an optional member in TypeScript,
`providers.ModelEmbedder` in Go, and the trait's `embed` in Rust, which
answers `None` unless the provider overrides it.

`complete` must:

- build the API's request from `turn.messages` alone: the `system`,
  `user`, `assistant` and `tool` messages in order, each assistant
  message's `toolCalls` before the `tool` messages that answer them by
  `toolCallId`, and its `providerState` passed back as the API requires;
- mark `untrusted` content in the way the API takes it, as data and never
  as instructions: a tool result that carries a party's message or a web
  page;
- offer `turn.tools`, each a name, a description and its input's JSON
  Schema, and use `turn.model`, or a default from a setting (`MODEL`), and
  `turn.maxTokens`;
- answer `content`, the `toolCalls` the model made, each with the API's
  id, `stop` (`end`, `toolUse` or `maxTokens`), and `usage`:
  `inputTokens` and `outputTokens`, which the agent's budget counts, and
  the amount when the API says;
- return in `providerState` what the API needs back on the next turn, as
  JSON, or leave it out;
- fail when the API does: the turn answers `502 model_failed`, its message
  redacted.

It must never put the API key anywhere but the request's header: not in
a message, a log line, an error message, or `providerState`, which the
dispatcher keeps unredacted while the run lives. No credential value
reaches a model's text (ground rule 6): credentials reach the runner in
the sandbox alone. A model provider adds no setting or secret to the
messages, and logs no turn's messages, which hold the task and the tool
results.

The build holds the manifest to the contract: `implements: "model"` and
the operation `complete`. An API whose database declares the agents'
tables needs a model slot set too.

```ts
import { defineProvider, type ModelProvider } from "@superschematic/http-runtime/providers";

export default defineProvider({
  name: "model-api",
  implements: "model",
  config: { URL: {}, KEY: { secret: true }, MODEL: { optional: true } },
  create(settings, context): ModelProvider {
    const url = settings.text("URL");
    const key = settings.secret("KEY"); // revealed into the request's header alone
    const model = settings.optionalText("MODEL");
    return {
      // turn.messages and turn.tools in; content, toolCalls, stop and usage out
      async complete(turn) { throw new Error("not implemented"); },
    };
  },
});
```

### A channel adapter, a credential provider, a file processor

These have stubs that already talk to a stand-in service over HTTP, so
the copy of step 1 changes the requests they make and little else:

- **A channel adapter** answers its `kind` and implements `send` and
  `health`, and `status` and `inbound` when the service posts delivery
  statuses or messages back, which its manifest then lists in
  `operations`. Those reach it at `POST /webhooks/<slot>/status` and
  `/inbound`, the slot in lower case: check the service's signature
  there. A bridge that connects outward has a `role` such as
  `messages.relay` and a key of its own. Every environment that sends
  notices needs one ([events and notifications](/superschematic/guides/notifications/)).
- **A credential provider**'s `list` answers titles and sites and reads
  no value. `resolve` reads the one field and seals it, from a secret
  value, in an `Injection` for the target it was asked for
  (`Injection.fill` and `Injection.header` in TypeScript and Rust,
  `providers.FillInjection` and `HeaderInjection` in Go), and refuses a
  reference outside what its settings allow. No build check covers it.
- **A file processor** answers its `mediaTypes` and implements
  `extractText` and `preview`, each answering null (`nil`, `None`) for a
  file it does not read, which the pass then hands to the next files
  slot. Search over attachments' text needs one.

### Test it end to end

The agent runs' end-to-end tests run the dispatcher over the stubs:
`TestAgentRunsEndToEnd` (`internal/generator/tsgen`), and
`TestGoAgentRunsEndToEnd` and `TestRustAgentRunsEndToEnd`
(`internal/generator`).

- A sandbox provider runs the TypeScript cases in the stub's place.
  `agentsProviders` in `internal/generator/tsgen/agents_test.go` copies
  `local-sandbox.ts` beside the test, and `agents.test.ts` names it in its
  `providersFrom`: point both at your package. Each instance runs the
  test's `command`, `bun agent_runner.ts`, with the spec's env, and calls
  the test's endpoint on loopback, so the instance needs Bun, the runner
  and a route to the test's host. The Go and Rust cases run their
  instances in the test's own process.
- A model provider keeps the stub in these cases, which script each turn
  through its `call <tool> <json>` answers. Hold it to the stub's cases of
  step 4, then start a run in the environment that names it.

A channel adapter's end-to-end proof is the notifier's: name it in one
environment and send a notice there, as `examples/notifications` does over
its relays.
