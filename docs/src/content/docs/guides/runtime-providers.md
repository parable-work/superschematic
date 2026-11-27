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
| a provider package's manifest in its `package.json`, which the build checks against its interface's contract | a provider package named by its npm name; the config lists directories |
| `@providers` on an API's `@envVars` class, and the API config's `providers` | a generated TypeScript server that links the packages and builds `Providers` in its `Deps`; a Rust server and a Go server do |
| the stack's checks: each slot names a provider the API lists, of its interface, with its settings bound; a feature that needs an interface no slot fills is refused, notifications among them ([events and notifications](/superschematic/guides/notifications/)), and agents, which need a sandbox and a model ([agent runs](/superschematic/guides/agent-runs/)) | a check for `files` (attachments) and `credentials` (agent runs' credentials): those features register as they are built |
| credentials that never reach the caller: a sealed `Injection`, `useCredential` and the record of each use | |

D159 in [docs/DECISIONS.md](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md)
records the design and what is left. `examples/runtime-providers` is the
worked example this page quotes.

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

The API's config lists the packages it may use, each a directory
relative to the service's directory:

```ts
export default defineConfig({
  name: "notes-api",
  kind: SchemaKind.API,
  dependencies: [NotesDb],
  providers: ["../../../providers/sms-relay-a", "../../../providers/sms-relay-b"],
  outputs: { /* ... */ },
});
```

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
`stack secrets set` enters the secret settings:

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

It refuses at start a config that names a provider the server does not
link, one of another interface, or leaves a setting unset, naming every
problem and never a value. A provider is made on first use. Each call
into it runs within its own principal, of kind `extension`, whose subject
is `extension:<name>` and whose permissions are its role: what it writes
through an ORM opened with no user is recorded as the provider.
`extensionAuthenticator` authenticates a provider that calls the app with
its own key, such as a messaging bridge that connects outward and relays
replies, as that principal.

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

Every call into a provider is made with a context of its own principal,
kind `extension`, and its role, which the server binds to the ORM too
(`orm.WithPrincipal`), so a provider reads what everyone may read.
`providers.ExtensionAuthenticator` authenticates a provider that calls
the app with its key as that principal, and composes with
`session.GrantAuthenticator`. An `Injection` has no exported field, so
no other package makes one that holds a value, and its JSON and every
`fmt` verb print its reference and target alone.
