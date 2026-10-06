# Stack model

This is the design of the stack model: how a schema tree declares what
runs where, how superschematic resolves the wiring between the services in
it, and how it deploys them. The Stack IR, the resource graph, the
registry specs and the resolver are built (section 12); the rest is design.
D30 in `docs/DECISIONS.md` records the decisions; this document is the
design they point at.

Cite sections by number, as source comments cite `docs/extension-model.md`.
Add a section at the end of its group rather than renumbering.

## 1. Goal

The end state: an engineer has a GCP project and a schema tree of DB and API
services. They enter a project id, a region and, optionally, a domain, and
type in the values of their secrets when asked. superschematic generates and
drives everything else: the cloud resources, the server entrypoints and
images, the database migrations, the deploy order and the CI that runs it.

Principles:

- A fact is declared once. Wiring that follows from the schemas (an API's
  database, the address of a service another one calls) is derived, never
  restated.
- A reference is a value the loader resolves: a service handle (the
  `service({...})` sentinel), a class or a type. Never a name in a string.
- Schemas are static. superschematic reads them and does not run them.
  Everything downstream is a pure function of the schemas and the values an
  environment sets, so it can be checked, diffed and simulated in CI without
  cloud credentials (section 10).
- Mechanisms belong in the core, and platforms and tools in registrations
  (D10). Cloud Run is the first platform and Pulumi the first provisioner;
  neither is special.

Not in scope:

- A general infrastructure language. The resource vocabulary covers what
  deployables need. Anything else is written by hand against generated
  bindings (section 6.6).
- Schema migrations. They belong to `sqlgen` and are designed on their own,
  for Postgres and SQLite. Section 8.4 states what this model needs from
  them.
- Moving the distribution built on the source tree onto this model. Its
  deploy family informed the design; section 16 lists what was kept and what
  was not.

## 2. Three tiers

```
schemas        DB schema (tables)     API schema (operations)     @envVars (settings)
                     |                        |
deployables    database  <--- edge ---    server    --- edge --->   server
                     |                        |
environments   a target (gcp, local, ...) and the values only a person can decide
```

- **Schemas** exist today.
- **Deployables** are the units that run. A database hosts DB schemas and a
  server serves API schemas. Each has needs and provides, and the schemas
  already say most of them (section 3).
- **Environments** place deployables on a target and set the values only a
  person can decide (section 4). A resolver wires every need to something
  that provides it in that environment (section 5).

## 3. Deployables

### 3.1 Kinds

| Kind | Hosts | Provides | Needs |
| --- | --- | --- | --- |
| database | one or more DB schemas | a SQL connection per hosted schema | nothing |
| server | one or more API schemas | an HTTP endpoint per served API | a connection to each served API's database; the address of each service it calls |

Jobs, scheduled jobs, buckets, queues and static sites come later. Each
lands the same way: a deployable kind, a platform per target that realizes
it (section 6.1), and the edges it takes part in.

### 3.2 Defaults

With nothing declared, each API service in a stack is one server and each
DB service is one database. A deployable is declared only to change that:

- to run several APIs in one process;
- to host several DB schemas on one database.

A declared deployable only groups. It declares no needs: a server's edges
are the union of its APIs' edges, so grouping APIs never restates one.

### 3.3 Edges

An edge is a need met by something that provides it. v1 has two kinds:

| Edge | From | To | Derived from |
| --- | --- | --- | --- |
| sql | server | database | the database each served API already names: its `authDb`, or its one DB-kind dependency, as `resolveUpstreamAuth` in `internal/generator/dispatch.go` reads it |
| http | server | server | `calls` in the config of each API the calling server serves |

`calls` is the one wiring fact a person writes, because no schema says that
one API's implementation calls another API. It sits in the API service's
config next to `authDb`, because both describe what the implementation
needs, and the implementation belongs to the API (section 8.5):

```ts
// schemas/services/shop-orders/schema.config.ts
import { ShopApi } from "@acme/shop-api";
import { ShopDb } from "@acme/shop-db";
import { defineConfig, SchemaKind } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-orders",
  kind: SchemaKind.API,
  authDb: ShopDb,
  calls: [ShopApi],
  outputs: { /* ... */ },
});
```

It is written once, as a handle: the callee's imported sentinel, or
`service({ name, kind })` (D34). The config field, the SDK client in the
implementation's `Deps`, the URL, the invoker grant and the network rule
all follow from it. A call between two APIs that one server serves stays an
HTTP call to the server's own address.

`calls` is also a build dependency, since the caller's generated `Deps`
imports the callee's SDK (section 8.5), and `build --with-deps` builds the
callees with the caller. The build plan orders outputs, not whole
services: only a caller's API server reads its callees, so each callee's
SDK builds before the servers that call it, and the caller's other outputs
need nothing of the callee.

- Where the calls form no cycle, each callee builds whole before its
  caller, as `dependencies` and `authDb` order a tree.
- Two APIs that call each other form a cycle between services, which is
  not an error. The tree is then ordered by `dependencies` and `authDb`
  alone. A caller whose callee comes after it builds in two steps: its
  base outputs (the types, SQL, ORM and SDKs) in its place, and its API
  server once its callees' base outputs are built (`buildplan.Steps`).
  `build-all --parallel` names the steps `shop-api (base)` and `shop-api
  (server)`.
- A cycle of `dependencies` and `authDb` still cannot build, and its error
  names each edge (`a depends on b, b authenticates against a`).

Building is not deploying: two servers that call each other still have no
callee-first rollout (section 5.3), and resolution refuses them
(`call-cycle`, section 6.10) unless one server serves both APIs.

The build cache still stores a service as one entry, written after its
last step. A caller's key adds each callee's key, taken without the
callee's own calls, so two APIs that call each other hash without a cycle.
What a caller reads of a callee is its SDK and its types, which that key
covers.

### 3.4 Bindings in the generated config

Each edge adds a typed field to the server's generated config. The env
loaders `envgen` writes for Go, Rust and TypeScript, and the
`values-schema.json` beside them, gain:

- a database field per sql edge. It holds a connection the edge's connector
  fills (a Cloud SQL connector configuration on GCP, a connection string
  locally), not a string the application parses;
- a service field per http edge. It holds the callee's base URL, the
  source of the service credential and the headers that carry it (section
  9.2);
- a service-auth field on a server that an http edge reaches. It holds
  what the server's `ServiceAuthenticator` checks: each inbound edge's
  issuer, keys and audience, and the deployable each caller identity is
  (section 9.2).

An API's database field comes from its `authDb`, or its one DB-kind
dependency, as its sql edge does (section 3.3).

The Go loader has the first two. The API package's `EnvConfig` embeds the
`@envVars` type and adds a field per edge, a `stackconfig.Database` or a
`stackconfig.Service` from the Go HTTP runtime, which `LoadEnvConfig`
reads. `values-schema.json` lists each derived field in
`x-superschematic.envVars` with `derived` (the edge kind), `service` and
`variables`, and each of its variables as an optional string property
that the platform sets, not a deployment's values. The TypeScript and Rust
loaders read no derived field yet (section 12), and the service-auth field
waits for the connectors that write it.

What a connector derives for each edge kind has a contract, in
`ir/derived_value.go`. Resolution checks every connector's value against
it and refuses one that breaks it with a `lowering` failure that names the
member at fault:

| Edge | Value | Members |
| --- | --- | --- |
| sql | `ir.DatabaseConnection` | `url`, a connection string; or `cloudSql`, a Cloud SQL connector configuration: `instance` (the instance connection name), `database` and `user` (the IAM database user) |
| http | `ir.ServiceEndpoint` | `url`, the callee's base URL; and an optional `credential`: its `source` (`google-id-token`, `token-file` or `signed-token`, the runtimes' sources of section 9.6), the settings that source reads (`audience`, `tokenFile`, `issuer`, `key`), and the `headers` that carry it, which include `Service-Authorization` |

A member holds a string or a reference to an output or a parameter. A
credential's `source` and `headers` are literals, and a member the
contract lacks, or that the credential's source does not read, is
refused.

In environment variables, a derived field is one variable per member:
the field's name, an underscore and the member's path in upper snake case,
with a list joined by commas (`SHOP_DB_DATABASE_URL`,
`SHOP_DB_DATABASE_CLOUD_SQL_INSTANCE`, `SHOP_API_SERVICE_URL`,
`SHOP_API_SERVICE_CREDENTIAL_HEADERS`). `ir.DerivedVariables` encodes a
value that way for platforms, and the generated loaders read it back.
Each variable is a plain string a platform can set from an output
reference, or from its secret store for a member such as a signing key.

Not taken: the value as one JSON document in one variable. Every
provisioner would have to render references inside a JSON string, and no
member of it could come from a secret store.

Field and variable names follow a naming-file rule over the callee's
service name, with the core's rule as the default (D7, D8). The
`[derived_fields]` table holds a template per edge kind, `database` and
`service`, in which `{SERVICE}` is the DB or called API service's name in
upper snake case. They default to `{SERVICE}_DATABASE` and
`{SERVICE}_SERVICE`. envgen and the resolver (`stack.Input.FieldNames`)
name the fields by the same templates.

A server's own `@envVars` type holds only the application's settings. The
loader refuses an `@envVars` field whose name collides with a derived one:
the derived field's name, or that name followed by an underscore. The
second form covers each of its variables, and any member a later contract
adds. The resolver applies the same rule (`field-collision`).

The generated entrypoint (section 8.1) reads these fields, so application
code never names an environment variable. `examples/acme-shop/go/example_test.go`
connecting with `os.Getenv("DATABASE_URL")` is the code this replaces.

## 4. Authoring

### 4.1 The Stack kind

A stack is a service of the core kind `Stack`, whose decorators come from
`@superschematic/stack`. The stack takes its service's name. Its schema
files declare the stack, any deployables that differ from the defaults, and
the environments, a class each. A sketch over acme-shop:

```ts
// schemas/services/shop-stack/schema.config.ts
import { defineConfig, SchemaKind } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-stack",
  kind: SchemaKind.Stack,
  outputs: {},
});
```

```ts
// schemas/services/shop-stack/src/stack.schema.ts
import { ShopApi } from "@acme/shop-api";
import { ShopDb } from "@acme/shop-db";
import { ShopOrders } from "@acme/shop-orders";
import { environment, server, stack } from "@superschematic/stack";

@stack({ deploy: [ShopApi, ShopOrders], expose: [ShopApi] })
export abstract class Shop {}

@server({ serves: [ShopApi, ShopOrders] })
export abstract class Backend {}

@environment({ target: "local" })
export abstract class Dev {}

@environment({
  target: "gcp",
  gcp: { project: "acme-staging", region: "us-east1" },
  domain: "staging.acme.dev",
  dns: { cloudflare: { zone: "acme.dev" } },
})
export abstract class Staging {}

@environment({
  target: "gcp",
  gcp: { project: "acme-prod", region: "us-east1" },
  domain: "acme.dev",
  settings: [
    { of: ShopDb, tier: "db-custom-2-7680", highAvailability: true },
    { of: Backend, minInstances: 1, env: { LOG_LEVEL: "warn" } },
  ],
})
export abstract class Production {}

@environment({
  parameters: ["pr"],
  settings: [{ of: Backend, env: { PREVIEW_ID: { parameter: "pr" } } }],
})
export abstract class Preview extends Staging {}
```

- **`@stack`** declares the stack, once per schema. **`deploy`** names
  the entry points, API and DB services. Everything they reach through
  `authDb`, DB dependencies and `calls` joins the stack, so `shop-db`
  needs no mention.
- **`expose`** names what is reachable from outside the environment, an
  API's handle or an `@server` class. Everything else is internal, and
  reachable only along its edges.
- **`@server`** declares a deployable only to change a default. Here it
  runs both APIs in one process in place of their two default servers. Its
  edges are its APIs' edges: shop-db through `authDb`, and shop-api
  through shop-orders' `calls` (section 3.3). **`@database`** (`hosts`)
  puts several DB schemas on one database in the same way.
- **`target`** picks a target (section 6.3). The target's values sit under
  its name, `gcp` here, and are checked against the schema the target
  registers. An environment that inherits its target names it again to
  change one of them.
- **`domain`** is where exposed servers are reached, and **`dns`** places
  its records on a DNS platform (section 6.9), named as its one key.
  Production omits `dns` and gets the target's default, Cloud DNS.
- **`settings`** sets values per deployable. `of` is a service handle or
  an `@server` or `@database` class. `platform` places the deployable on
  another platform than the target's, and every key but `of`, `platform`
  and `env` is a platform setting, which the loader checks against that
  platform's settings schema. `env` binds the server's `@envVars` fields,
  each to a literal or to `{ parameter }`, a parameter of the environment
  that the deploy run supplies. tsc checks the same in the editor
  (section 4.3).
- **`Preview extends Staging`** inherits Staging's values, and `parameters`
  makes it a family of environments, one per value (section 5.4). An
  `@environment` class extends only another `@environment` class.

Every class of the schema carries one of the four decorators and holds no
fields. The kind's verification checks that, and that each class a
declaration names is an `@server` or `@database` class of the schema, so
the data forms are held to it too. Each decorator writes a declaration on
its class's `TypeDef` (`Stack`, `Server`, `Database` or `Environment`),
and `ir.StackOf` assembles them into the `ir.Stack` the resolver reads,
its deployables and environments in name order.

Every declaration has the JSON and YAML data forms every schema has. A
class is a type, and its declaration the key the decorator writes. A
handle is written `{name, kind}`, and a settings `of` or an `expose` entry
`{service: {name, kind}}` or `{deployable: Backend}`. The target's values
are `values`, the DNS platform `{platform, values}`, an env value `{value}`
or `{parameter}`, and the parent the type's `extends`:

```yaml
types:
  Production:
    name: Production
    role: EmbeddedStruct
    environment:
      target: gcp
      values: { project: acme-prod, region: us-east1 }
      domain: acme.dev
      settings:
        - of: { service: { name: shop-db, kind: DB } }
          values: { tier: db-custom-2-7680, highAvailability: true }
        - of: { deployable: Backend }
          values: { minInstances: 1 }
          env: { LOG_LEVEL: { value: warn } }
```

A stack is named by no other service, so it has no sentinel, and its
schema files import its siblings' sentinels, which `build` writes first.
Its one generator, `stack` (`internal/generator/stackgen`), loads each
service the stack reaches, resolves every environment (section 6.10) and
writes each to `<output-root>/stack/<stack>/<environment>/environment.json`.
A resolve check fails the build and names the stack, the environment and
each problem with its code. The output is keyed by the service's name, so
build-all cleans, stores and restores it from the config alone.

`internal/generator/stackgen/testdata` holds the stack `stack/stacktest`
builds by hand, on its fake target, written in TypeScript and in YAML over
services shaped like acme-shop's. Its `environment.json` goldens are
stacktest's.

### 4.2 Secrets

A `Secret<T>` field of a server's `@envVars` is a secret in every
environment. A secret is identified by the type that declares the field
and the field's name, not by the server that reads it. The IR already
records where an inherited field was declared (`FieldDef.InheritedFrom`,
`ir/types.go:312`).

So a value declared once is stored once:

```ts
export abstract class PaymentsSecrets {
  STRIPE_KEY: Secret<string>;
}

@envVars export abstract class ShopApiConfig extends PaymentsSecrets {
  LOG_LEVEL: Default<LogLevel, "info">;
}

@envVars export abstract class OrdersConfig extends PaymentsSecrets {}
```

- In each environment, `PaymentsSecrets.STRIPE_KEY` is one secret. Every
  server whose config includes the field gets an accessor grant to it:
  here, the servers of shop-api and shop-orders.
- Two servers whose config is the same type share all its secrets with no
  further declaration.
- The platform stores the secret (Secret Manager on GCP, a gitignored file
  locally) under a name derived from the declaring type and the field.

A value is entered with `superschematic stack secrets set <environment>`,
which prompts for every secret in the environment that has no value, or
with one secret named (`PaymentsSecrets.STRIPE_KEY`). A value is never
written into a file. Resolution checks that every secret field has a
binding; a cloud preview (section 10) checks that a value exists.

Credentials a platform generates are not secrets in this sense, and nobody
enters them: a database password where IAM authentication is unavailable,
or an edge's key pair (section 6.2).

Not taken:

- Secret classes declared in the stack and bound to each server's field,
  which is a second declaration plus a binding per server.
- One secret per field name across servers, which makes a string the
  identity: `API_TOKEN` means different things to different servers.
- One secret per server and field, which enters and rotates a shared value
  once per server.

### 4.3 Typed authoring

tsc checks what the loader checks, so a mistake shows in the editor where
it is typed. The loader stays the source of truth and runs every check
again; the types are the early warning. Nothing here changes how
superschematic reads a schema, because the walker evaluates decorator
arguments as data either way. The TypeScript form's load reports tsc's
diagnostics too, so a settings value tsc refuses also fails `build` at the
line that holds it.

- **Handles carry their kind and config type.** `ServiceHandle<K, C>` in
  `@superschematic/schema-config` has two phantom type parameters, the
  kind as a string and the config type, with defaults, so a bare
  `ServiceHandle` is any handle. An API's generated `service.generated.ts`
  writes both: `service<"API", ShopApiConfig>({ name: "shop-api", kind:
  SchemaKind.API })`, with `ShopApiConfig` imported as a type. The second
  names the API's `@envVars` class, so the sentinel is written after the
  service loads; the sentinel sweep, which reads only configs, keeps the
  type a build wrote. A DB or General handle has no config type, nor has
  an API without a TypeScript `@envVars` class, and `service()` infers the
  kind from its argument (`kind: SchemaKind.DB` gives `ServiceHandle<"DB">`).
  No person writes either parameter. `calls` takes `ServiceHandle<"API">`,
  so tsc refuses a DB handle there.
- **Targets type their own values and settings.** `@superschematic/stack`
  declares an empty `Targets` interface. Each target's authoring package
  augments it with the target's environment values and a settings type per
  deployable kind, the way D16 types each behavior's config:

  ```ts
  declare module "@superschematic/stack" {
    interface Targets {
      gcp: { values: GcpValues; server: CloudRunSettings; database: CloudSqlSettings };
    }
  }
  ```

  `target` takes a key of `Targets`, so a target no installed package
  augments is refused, and the values under the target's name take its
  `values` type.
- **`@environment` infers each settings element.** Its signature has two
  `const` type parameters: the target, inferred from `target` alone, and
  the `settings` tuple. Each element is checked by its `of`:
  - an API handle takes the target's `server` settings, and an `env`
    typed from the handle's config type;
  - a DB handle takes the target's `database` settings and no `env`;
  - an `@server` or `@database` class takes either kind's settings and an
    `env` of any field, since tsc cannot see what a declared deployable
    serves;
  - an element that names a `platform`, which may be another target's,
    takes any settings key, and so does every element of an environment
    that names no target. Such an environment sets no target values.

  A key an element does not take is refused, as `env` keys are. The keys
  of `env` are the config's fields, `Secret<T>` fields left out so a
  literal for one fails, and each takes its field's type unwrapped, or
  `{ parameter }`. `Default<T, V>`, `Validate<T, C>` and the other wrappers
  in `packages/schema/src/wrappers.ts` share a phantom base,
  `Wrapped<T>`, through which a mapped type takes T back, and `Nullable`
  is dropped. A string enum field also takes its values as strings
  (`LOG_LEVEL: "warn"`).

`@ts-expect-error` fixtures pin the behavior:
`packages/schema-config/src/service-handle.typecheck.ts` for handles, and
`packages/stack/test/environment.typecheck.ts`, which augments `Targets`
with a target of its own, for the rest. `make ts` runs tsc over both.

Not taken:

- Checks in the loader only, which leaves every mistake to `superschematic
  build`.
- Settings as class fields with type-level literals, such as
  `shopApi: Settings<typeof ShopApi, {...}>`, after `Relation<Product,
  {...}>`. The walker would have to resolve `typeof` on a handle, and a
  field type cannot see the decorator's `target`, so platform settings
  would stay unchecked.

## 5. Resolution

### 5.1 Inputs and output

The resolver reads the Stack schema, the IR of every service it references,
and one environment. It writes the resolved environment to
`<output-root>/stack/<stack>/<environment>/environment.json`:

- the deployables, each with its platform and settings;
- the edges, each with its connector;
- a binding for every config field of every server, from one of four
  sources:

| Source | Example | Decided by |
| --- | --- | --- |
| literal | `LOG_LEVEL = "warn"` | the environment |
| secret | `STRIPE_KEY` | the platform stores it; a person enters the value |
| derived | the shop-db connection | the edge's connector, asked once |
| parameter | `pr` | the deploy run |

- the resource graph the platforms and connectors lower it to (section 6.4).

The resolved environment is the only input that renderers and provisioners
read. None of them recomputes an address or a name.

### 5.2 Checks

Resolution fails on:

- a required config field with no binding and no default;
- an `env` key that is not a field of the server's `@envVars`, or a literal
  for a `Secret<T>` field;
- a handle whose kind does not match the service it names;
- a deployable its platform cannot realize, such as a Go server on a
  platform that runs only TypeScript;
- an edge that no connector realizes between the two platforms;
- an exposed deployable that is not a server;
- a target's policy rule over the resource graph, such as "production
  databases are highly available" or "nothing is public unless exposed".

### 5.3 Deploy order

The order comes from the graph:

1. infrastructure;
2. the migrations' `expand` steps, which the running servers survive;
3. servers, callees before callers, so a new caller never meets an old
   callee;
4. the migrations' `contract` steps, once no server of the previous
   version runs (D27);
5. exposure.

The provisioner applies in that order (section 6.5).

### 5.4 Parameterized environments

An environment with `parameters` is a family. Each member extends its
parent: it shares the parent's infrastructure (a Cloud SQL instance, a
VPC) and creates its own deployables, named with the parameter's value.
Each platform says how it parameterizes a deployable: a Cloud Run service
suffixed `-pr123`, or a database `shop_db_pr123` on the parent's instance.
A parameter value is never written into a generated file. The provisioner
supplies it at run time.

## 6. Plug-in interfaces

Five registrations keep platforms and tools independent of each other and
of the core:

- a deployable is placed on a **platform**;
- an edge between two placed deployables is realized by a **connector**;
- a **target** names a platform for each deployable kind;
- a **DNS platform** holds an environment's domain records (section 6.9);
- a **provisioner** turns the resulting resource graph into running
  resources.

### 6.1 Platform

A platform realizes one deployable kind on one runtime: Cloud Run servers,
Cloud SQL databases, local processes, a local Postgres container. It
registers a `PlatformSpec`:

- `Kind`, the deployable kind, and what it accepts: `Languages` for a
  server platform, spelt as `outputs.api.language` spells them (`GO`,
  `TYPESCRIPT`, `RUST`), or `Dialects` for a database platform
  (`postgres`, `sqlite`), in order of preference;
- `Settings`, the JSON Schema of its settings (`minInstances`, `tier`);
- `NameOf` and `AddressOf`, how it names and addresses a deployable in an
  environment. Under a parameter the name references the parameter
  (`{"$concat": ["shop-api-", {"$parameter": "pr"}]}`), and an address
  usually references an output of one of the deployable's nodes;
- `Lower`, a pure function from the environment and the resolved
  deployable, bindings included, to the deployable's resources and, for an
  exposed server, the DNS records it needs (section 6.9).

A resource a platform leaves without a phase gets the default of its
producer: rollout for a server's own resources, infrastructure for a
database's.

### 6.2 Connector

A connector realizes one edge kind between two platforms: Cloud Run to
Cloud SQL over sql, Cloud Run to Cloud Run over http. It registers a
`ConnectorSpec` with the edge kind, its `From` and `To` platforms and a
pure `Connect`, which returns the resources the edge needs (an IAM grant, a
Cloud SQL connection on the service) and the value of the derived binding.
One connector serves an edge kind between two platforms; a second is
refused. An http edge between two APIs one server serves runs from the
server to itself, and its connector derives the server's own address.

A generic connector covers a pair of platforms on different providers that
no specific connector serves, such as a Cloudflare Worker calling a Cloud
Run server. It reaches the callee at its exposed address, and it
authenticates with a key pair generated for the edge:

- the private key goes into the caller's secret store;
- the public key goes into the callee's config;
- the caller signs a short-lived token with the key, and the callee's
  `ServiceAuthenticator` (section 9.2) verifies it.

This works between any two platforms and needs no long-lived cloud
credential, such as a service account key, on the other provider. It lands
with the second target (section 14), when there is a second provider to
mix with.

### 6.3 Target

A target is a named bundle, registered as a `TargetSpec`, of:

- a platform for each deployable kind;
- the schema of its environment values (`project` and `region` for
  `gcp`);
- its default DNS platform (section 6.9);
- the provisioner that applies its environments, whose state backend its
  bootstrap creates (section 6.5);
- the schema of the properties of each resource type its platforms,
  connectors and DNS platform emit (section 6.4);
- policy rules over the resource graph, each a named check over the
  resolved environment.

`gcp` is Cloud Run, Cloud SQL, Secret Manager, Cloud Build with Artifact
Registry, and a load balancer. `local` is processes, one Postgres container
per environment and a gitignored secrets file (section 8.3). The core
registers `local`, so every binary has it. Its resource types are the
core's own `local` provider's (a container, a database, an edge's key pair
and a process), not a Pulumi package's, since its own provisioner is the
only one that applies them.

Every deployable records its own placement in the IR from the start; the
environment's target is only the default. A `settings` entry can place one
deployable on another target's platform, for example a TypeScript server on
Cloudflare Workers in an otherwise gcp environment. Until the generic
connector lands (section 6.2), only edges a specific connector serves
resolve. In v1 those are same-target edges, and any other edge is a resolve
error that names the pair: "no connector from cloudflare.workers to
gcp.cloudrun over http". The provisioner already runs several providers in
one program, so adding mixing later changes connectors, not the IR or the
provisioner.

### 6.4 Resource graph

The resource graph is the contract between platforms and connectors on one
side and provisioners on the other. A node has an id, a resource type,
properties and dependencies. A property may reference another node's output
or a parameter.

In `environment.json` a reference is an object with one reserved key:
`{"$output": {"resource": "shop-api.service", "name": "uri"}}`,
`{"$parameter": "pr"}`, or `{"$concat": [...]}` for strings and references
joined. A node also records its phase (infrastructure, rollout or
exposure), whether it is `inherited` from the parent environment
(section 5.4), and its `owners`: the deployables, edges or DNS that
produced it. An output's name may be a path into the output, names joined
by dots with list indexes in brackets: the gcp target reads a certificate
authorization's record as `dnsResourceRecords[0].data`. Two producers that return the same node share it, and two that
return different nodes under one id fail. A node's `dependsOn` holds the
dependencies its producer named and every node its properties reference.

Resource types and properties use Pulumi package schemas as their
vocabulary (`gcp:cloudrunv2/service:Service`,
`kubernetes:apps/v1:Deployment`):

- they are published, machine-readable and versioned, and cover GCP,
  Kubernetes, Cloudflare and the other clouds;
- Kubernetes types map one to one onto apiVersion and kind, and the
  providers bridged from Terraform map one to one onto Terraform resource
  types. A provisioner other than Pulumi therefore translates names, not
  structures.

A target pins each provider schema's version and checks in the schemas of
the types it uses. A Go tool with a `-check` mode keeps them current, as
`internal/tools/scalarcatalog` does for the scalar catalog, so properties
validate offline. The gcp target pins pulumi-gcp in
`extensions/gcp/schemas/pulumi-gcp.json`: the release, the digest of each
upstream file the schemas come from, and the types its platforms,
connectors and DNS platform emit. `extensions/gcp/internal/tools/providerschemas`
writes one file per type beside it, and CI runs it with `-check`. The
target registers each file as the JSON Schema of its type's properties,
with every object type closed, since Pulumi refuses an unknown property.

Each pinned file also records the type's Terraform name and any property
renames, taken from the bridged provider's published mapping:

```json
{
  "token": "gcp:cloudrunv2/service:Service",
  "terraform": {
    "type": "google_cloud_run_v2_service",
    "renames": {
      "invokerIamDisabled": "invoker_iam_disabled",
      "template.containers.envs": "env"
    }
  },
  "inputProperties": { "...": "..." },
  "requiredInputs": ["location", "template"],
  "types": { "gcp:cloudrunv2/ServiceTemplate:ServiceTemplate": { "...": "..." } }
}
```

- `inputProperties` and `requiredInputs` are the type's own, and `types`
  holds every object and enum type they reach. Descriptions are left out.
- A rename's key is the property's path: the Pulumi names from the
  resource down, joined with dots, through lists and objects alike.
- The mapping is `bridge-metadata.json`, which the bridge publishes beside
  `schema.json`. Its alias table names the Terraform type each token is
  the current name of, and every list and block field. A property's
  Terraform name is its snake case, except a list the bridge pluralized
  (`env` is `envs`), which a list field names. The tool fails on a list no
  field names rather than guess. Pulumi's full mapping (`pulumi package
  get-mapping terraform gcp`) agreed with every path of the first pin, but
  it needs the provider's plugin, so the tool does not read it.

The Pulumi provisioner uses the token as it is. A Terraform-family
provisioner such as OpenTofu uses `terraform.type` and `renames`, so adding
one is a lookup, not a translation layer. A round-trip test, from Pulumi
names to Terraform and back over every pinned type, lands with that
provisioner. Kubernetes types need no mapping, because a token is an
apiVersion and a kind.

Not taken:

- Terraform provider schemas as the vocabulary. They are the most widely
  shared: OpenTofu speaks them, and Pulumi and Crossplane both generate
  providers from them. But the first provisioner would run them through
  Pulumi's bridge for arbitrary Terraform providers, a less mature path
  than its native GCP provider, and Kubernetes fits them poorly.
- A vocabulary of superschematic's own, which re-models every cloud
  resource it uses and needs a mapping per resource per provisioner.

### 6.5 Provisioner

A provisioner takes a resource graph to running resources and back:

- `Render(environment, dir)` writes the tool's program for the
  environment's resource graph where a person can read it;
- `Plan`, `Apply` and `Destroy` run with credentials, against a state
  backend the target's bootstrap created;
- `Outputs` reads the applied graph's outputs. They feed the bindings
  (section 6.6) and the deploy manifest (section 11.2).

A provisioner registers a `ProvisionerSpec` that holds an implementation of
the `Provisioner` interface: `Render(environment, dir)`, then `Plan`,
`Apply`, `Destroy` and `Outputs`. Each run takes a request that carries the
resolved environment, the parameter values of the run, the rendered
program's directory and the state backend: its URL and its secrets
provider. `Apply` applies one step of the deploy order, so the deploy runs
image builds and migrations between steps. `Render` takes the whole
environment, not only its graph, because the program exports every output
the environment references, a deployable's address included.

Pulumi is the first provisioner:

- **Driven from Go.** superschematic drives Pulumi through the Automation
  API, so it can put image builds, migration jobs and readiness checks
  between steps in deploy order, and run previews in CI.
- **Rendered as Pulumi YAML.** The program is one resource per graph node,
  with its properties, `dependsOn` and `${node.output}` references. It needs
  no compile step and diffs cleanly in review. `pulumi convert` turns it
  into Go or TypeScript for anyone who ejects.
- **State in the project.** State lives in a GCS bucket with a Cloud KMS
  secrets provider, both created by bootstrap, so no Pulumi Cloud account is
  needed.

`extensions/pulumi` builds it. The program, `Pulumi.yaml`:

- belongs to a project named after the stack (`shop`). Each run of an
  environment is a stack of it: `staging`, or `preview.pr-123` for a member
  of a parameterized environment;
- declares each parameter as a string of the project's config, which the
  run sets, so one program serves every member and no value reaches the
  file;
- keys each node by its ID, with every character other than a letter, a
  digit, `_` and `-` turned into `-`, and names it by the ID, so state is
  keyed on the ID. An output reads `${shop-api-service.uri}`, and may be a
  property path (`dnsResourceRecords[0].data`). Literal strings escape `$`;
- gives a node the `version` option for the plugin version the
  distribution pins for its package (`gcp`), which is the version of the
  provider schema the target checks properties against (section 6.4);
- reads an inherited node through a stack reference to the parent
  environment's stack;
- exports every node's `id` and every output the environment references,
  each as `<node>.<output>`.

The driver opens a local workspace over the rendered directory, with the
request's backend and secrets provider: `gs://` and `gcpkms://` for the gcp
target, `file://` and a passphrase in tests. It refuses a directory whose
program is not the one the environment renders, and it fails with a clear
error when the `pulumi` CLI is not on PATH.

- `Plan` previews the whole program and maps each step to a change: create,
  update, replace or delete.
- `Apply` runs `up` with the step's nodes as targets. A targeted update
  deletes any resource the program dropped that depends on a target, so
  those resources are targets too. The last step that holds nodes runs `up`
  over the whole program, which deletes whatever else the graph dropped.
- A member first checks that the parent's stack exports every output it
  reads. A stack reference would read a missing one as null.
- `Destroy` deletes the resources and removes the stack.
- `Outputs` leaves out secret outputs, and the unknowns of a step not yet
  applied.

The CLI keeps each stack's settings file beside the program. A fresh
checkout has none, so the driver writes the request's secrets provider into
it. Its integration test runs every operation against a `file://` backend
with the `random` provider, which needs no credentials. CI installs the CLI
at `PULUMI_VERSION` in `tools.env`, the version of the Go SDK the module
requires.

Later provisioners are registrations: OpenTofu over the same graph, or
Kubernetes manifests that a GitOps controller applies, for Kubernetes
targets.

### 6.6 Bindings

After apply, the provisioner's outputs become a generated, typed binding
for code outside the stack. It is a Go package, with TypeScript later, that
has one value per environment and one field per deployable
(`shopstack.Staging.ShopAPI.Address`,
`shopstack.Staging.ShopAPI.Account.Email`). Hand-written Pulumi programs
read it over Pulumi stack references; scripts and CI read it over the
outputs file.

`extensions/pulumi/bindings` is the generator. It reads each environment's
`environment.json` and its outputs file, `outputs.json`, which holds what
`Outputs` read for one run by node ID and output name. It writes two
packages:

- **The values** (`shopstack`). `Environment` has a field per deployable,
  and a value per applied environment (`Staging`). A deployable's field
  holds its name and address in the environment, and a field per node it
  owns with that node's outputs. A node an edge owns sits with the edge's
  server; DNS records sit in a field of their own. An output is a string, a
  bool, a float64 or `any`, typed by the values the outputs files hold. A
  parameterized environment has no value: each member is a run of its own.
- **The stack references** (`shopstackpulumi`). The same types over Pulumi
  outputs, and a function per environment that reads one over a stack
  reference to its stack. A member's function takes the parameter values:
  `shopstackpulumi.Preview(ctx, "123")`.

Generate the binding again after an apply. Its goldens and a compile test
that builds a program against both packages are in the generator's
`testdata`.

The binding is the escape hatch. A resource the vocabulary lacks is written
by hand, in a program of its own, and references the stack's resources
through the binding rather than through a copied name. An environment can
name such a program, and the provisioner applies it after the stack. That
last part is not built: the Stack IR has no field for the program yet.

### 6.7 Registry surface

There are five specs, registered like the others in section 3 of
`docs/extension-model.md`. The core registers one target, `local`, with
its platforms, connectors and provisioner (section 8.3, and D30, amended:
the core registers the local target); every other is an extension's.

- `RegisterPlatform(PlatformSpec)` refuses a malformed or repeated name, an
  unknown deployable kind, a server platform without languages or a
  database platform without dialects (or either with the other's list), an
  unknown or repeated language or dialect, a settings schema that does not
  compile, and a missing `NameOf`, `AddressOf` or `Lower`.
- `RegisterConnector(ConnectorSpec)` refuses a malformed or repeated name,
  an unknown edge kind, a missing platform or `Connect`, and a second
  connector for one edge kind between the same two platforms.
- `RegisterTarget(TargetSpec)` refuses a malformed or repeated name, an
  unknown deployable kind, a values or resource type schema that does not
  compile, a resource type another target registered with a different
  schema, and a policy rule without a name or a check, or with a repeated
  name.
- `RegisterDNSPlatform(DNSPlatformSpec)` refuses a malformed or repeated
  name, the reserved name `manual`, a values schema that does not compile
  and a missing `Lower`.
- `RegisterProvisioner(ProvisionerSpec)` refuses a malformed or repeated
  name and a missing implementation.

A name is lowercase words joined by dots or hyphens (`gcp.cloudrun`).
`Finalize` checks that each connector joins registered platforms of the
kinds its edge joins. It checks that each platform a target names is
registered and of the kind it places, and that a DNS platform or
provisioner the target names is registered; a target may name neither.

The acceptance test is D10's. `stack/stacktest` imports only the public
`registry`, `stack` and `ir` packages, registers a fake target with its
platforms, connectors, DNS platform and provisioner, and resolves a stack
over the acme-shop services. Its golden `environment.json` files are under
`stack/stacktest/testdata/golden`.

### 6.8 Targets after Cloud Run

| Target | Platforms | Connectors | What is specific to it |
| --- | --- | --- | --- |
| GKE | Kubernetes servers; Cloud SQL databases | GKE to Cloud SQL through Workload Identity and the Cloud SQL proxy; server to server through a Service and a NetworkPolicy derived from the edge | only the Cloud SQL connector; the Kubernetes server platform is shared |
| Hosted Kubernetes (EKS, AKS, DOKS and others) | Kubernetes servers; the cloud's managed Postgres, or an in-cluster operator | a database connector per cloud; the shared Kubernetes connector between servers | the database platform and its connector |
| Cloudflare | Workers for TypeScript servers (the generated TypeScript server uses Hono, which runs on Workers); D1 for SQLite-dialect databases | Hyperdrive to a Postgres database on another target; service bindings between Workers | everything, but through the same specs |

Resolution refuses a Go or Rust server on Workers, by the languages the
platform declares (section 5.2).

### 6.9 DNS

An environment with a `domain` places the domain's records on a DNS
platform. DNS is a platform kind of its own rather than part of a target,
because a domain's DNS often lives with a different provider than its
compute.

Exposure produces records in a neutral shape (name, type, value): the host
of each exposed server, and the records its certificate needs for
validation. The DNS platform lowers them to its provider's resources, in the
same provisioner run as the rest of the environment.

A DNS platform registers a `DNSPlatformSpec`: the JSON Schema of an
environment's values for it (a zone) and a pure `Lower` from the records
to resources. It is a spec of its own, not a `PlatformSpec`, because it
lowers records rather than a deployable.

v1 has two DNS platforms:

- **Cloud DNS**, the gcp target's default. It writes into the managed zone
  in the environment's project that holds the domain.
- **Cloudflare DNS.** It writes into the named zone, with an API token the
  engineer enters at bootstrap. Records are DNS-only by default; proxying
  through Cloudflare is a setting.

An environment whose domain has no DNS platform the provisioner can write
gets `manual`, and `stack plan` prints the records to create.

Being a platform kind makes DNS the first mix of providers in v1, before
compute can mix (section 6.3). DNS records are not edges, so this needs no
connector.

### 6.10 How resolution drives the specs

`stack.Resolve` (`internal/stack`) is a pure function of the registry, a
stack, the facts of the services it references and one environment. The
facts come through a plain struct, `stack.Service`: name, kind, `authDb`,
`dependencies`, `calls`, the API language, the SQL dialects and the
`@envVars` fields with their `Secret`, `Default` and `InheritedFrom`. It
works in stages and stops at the end of the first stage that fails, so
every model check reports before anything is connected or lowered:

1. It merges the environment's `extends` chain: values and settings merge
   key by key, the parent first, and parameters add up.
2. It collects the services the stack reaches, makes the default
   deployables (section 3.2), places each deployable on its settings
   platform or its target's, and checks that the platform runs its
   language or dialect and accepts its settings.
3. It asks each platform for the deployable's name and address, derives
   the edges and finds their connectors, numbers each server's rollout
   wave from its calls, and binds every config field.
4. It calls each edge's `Connect` in edge order. When every one succeeds,
   it calls each deployable's `Lower` in name order, then the DNS
   platform's `Lower`, and merges their nodes. Every value a platform,
   connector or DNS platform returns is read through its JSON form, so
   what resolution accepts is what `environment.json` reads back.
5. It runs the graph checks of validation level 3: every dependency and
   referenced output names a node, every referenced parameter is
   declared, there is no cycle, every node's properties validate against
   the schema a registered target holds for its type, and every inherited
   node is a node of the same type in the parent environment, which it
   resolves for the check.
6. It orders the deploy (section 5.3). A node lands in its phase, or in a
   later step when one of its dependencies does. A server's own rollout
   nodes must land in its wave, and a database's nodes in infrastructure,
   before its migration. Migrate steps name the databases and hold no
   node, so the migration runner slots in there. No step applies an
   inherited node: the parent environment owns it.
7. It runs the target's policy rules.

Each failure carries a code, and `internal/stack/errors.go` lists them
all. The checks of section 5.2 have one each: `unbound-field`,
`unknown-env-key`, `secret-literal`, `kind-mismatch`, `unrealizable`,
`no-connector`, `expose-not-server` and `policy`, and the check section
9.3 adds has `unreachable-edge`. Malformed declarations,
unknown names and values that fail a schema have their own codes. So do
three failures section 5.2 does not list:

- an API with several DB dependencies and no `authDb`
  (`ambiguous-database`);
- a cycle of calls between servers, which no callee-first order serves
  (`call-cycle`);
- a config field that two types declare for one server, or that a derived
  field takes (`field-collision`).

Errors from a platform, connector or DNS platform are `lowering`, and the
graph checks are `graph`.

`stack.Write` puts the result at
`<output-root>/stack/<stack>/<environment>/environment.json`. Fields come
in the order the IR declares them and map keys are sorted, so a wiring
change reads as a diff.

## 7. The gcp target

`extensions/gcp` builds this section, apart from bootstrap (section 7.3)
and image builds: the target, its Cloud Run and Cloud SQL platforms, their
connectors, the Cloud DNS platform, the policy rules and the pinned
provider schemas (section 6.4), at pulumi-gcp 9.37.1. Its golden
environments resolve the acme-shop stack of section 4.1 in a staging, a
production and a parameterized preview environment.

### 7.1 What the engineer enters

- `project` and `region`, which are required, and `production: true` for
  an environment the production defaults (section 7.5) and policy rules
  (section 7.6) apply to;
- `domain`, which is optional, and its DNS platform: Cloud DNS by default,
  or Cloudflare with a zone and an API token (section 6.9). Cloud DNS
  writes into the managed zone that holds the domain. The zone is named
  after the domain with its dots as hyphens unless `zone` names it, and
  lives in the environment's project unless `project` names another;
- secret values, through `stack secrets set`.

Bootstrap reads the GitHub repository from the git remote.

### 7.2 Realization

| Stack concept | gcp |
| --- | --- |
| database | a Cloud SQL Postgres instance with IAM database authentication on, which refuses a connection that does not come through a Cloud SQL connector, and a database per hosted schema; a migration job |
| server | a Cloud Run service with its own service account, which holds the Cloud Trace agent role; the config in environment variables, a derived field as one variable per member of its value |
| sql edge | `roles/cloudsql.client` and `roles/cloudsql.instanceUser` for the server's account, held to the edge's instance by an IAM condition; an IAM database user; the Cloud SQL connection, which the connector derives (instance connection name, database, IAM user) and the service mounts |
| http edge | `roles/run.invoker` on the callee for the caller's account; the callee's `run.app` URL in the caller's config, with a Google ID token for that URL as the service credential (section 9.2) |
| internal server | internal-only ingress, with Cloud Run's invoker check on; callers also send the token in `X-Serverless-Authorization`, which the check reads |
| calling server | Direct VPC egress for all its traffic through the environment's network: a VPC, a subnet with Private Google Access, and Cloud NAT so the internet stays reachable |
| exposure | a global external Application Load Balancer per exposed server, with a Google-managed certificate from Certificate Manager on a host under the domain, authorized by a DNS record, and the records written by the environment's DNS platform (section 6.9); the service takes traffic from the load balancer only, with the invoker check off. Without a domain, the `run.app` URL, open to all traffic |
| secret | a Secret Manager secret named `<Stack>-<Type>-<FIELD>`, an accessor grant to each reading server's account, and an environment variable that references its latest version |
| image | built by Cloud Build, pushed to the Artifact Registry repository named after the stack and deployed by digest; the graph holds the image's repository path, and the deploy pins the digest it built |
| parameter | names suffixed with the parameter and its value (`shop-api-pr123`); a database per value (`shop_db_pr123`) on the parent's instance, whose secrets and network the member also inherits |

A caller reaches every callee at its `run.app` URL, exposed or not, from
inside the VPC. Cloud Run counts a request from a VPC as internal, which
an internal server's ingress requires and a server behind a load balancer
accepts, so no caller waits on a load balancer the exposure step applies
last. A call to an API the same server serves stays on loopback, with no
grant and no credential.

Each exposed server gets a load balancer of its own. A platform lowers one
deployable, so it cannot write the host rules of a load balancer the
environment's exposed servers would share; sharing one waits for a
lowering that sees the whole environment.

Every node sets its `project`, so the provisioner needs no provider
configuration, and the network lives in the environment's graph rather
than in bootstrap, since the edges decide whether there is one.

### 7.3 Bootstrap

`superschematic stack bootstrap <environment>` runs once with owner
credentials (application default credentials), and is safe to run again:

1. It enables the APIs the target's platforms use.
2. It creates the state bucket and the KMS key directly, since Pulumi needs
   them before it can run.
3. It applies a bootstrap graph through the provisioner:
   - an Artifact Registry repository named after the stack, in the
     environment's region;
   - a `deployer` service account and a read-only `planner` one;
   - Workload Identity Federation for the repository the git remote names.
4. When the environment's DNS platform is Cloudflare, it asks for an API
   token scoped to the zone's DNS, and stores it in Secret Manager where
   only the `deployer` and `planner` accounts can read it.

### 7.4 Database connections

Where the server's language has a Cloud SQL connector (Go, TypeScript),
connections use IAM database authentication through it, so there is no
password. Otherwise the platform generates a password into Secret Manager
and uses the Cloud SQL mount Cloud Run provides. The server's database
field is the same either way (section 3.4).

The connector form is built. A Rust server's sql edge fails to lower until
the derived value has a password form. An IAM database user starts with no
privileges in its database; granting them belongs to the migration job
(section 8.4), which is not built.

### 7.5 Defaults

The target sets defaults that `settings` can override:

- one service account per server;
- deletion protection on production databases (`deletionProtection`);
- a zonal instance unless `highAvailability` is set, on the
  `db-custom-1-3840` tier of the Enterprise edition (`tier`), running
  Postgres 16, the version CI tests against (`version`), with backups on
  and point-in-time recovery in production;
- one CPU, 512 MiB and no minimum instances per server (`cpu`, `memory`,
  `minInstances`, `maxInstances`, `concurrency`);
- logs to Cloud Logging, and traces to Cloud Trace through the entrypoint's
  OpenTelemetry setup.

### 7.6 Policy rules

- `production-databases-highly-available`: in an environment whose values
  set `production`, every Cloud SQL instance it creates is regional.
- `nothing-public-unless-exposed`: nothing admits the public on behalf of
  anything but an exposed server. It refuses an internal server's service
  that takes outside traffic or turns its invoker check off, a load
  balancer's address or forwarding rule, a grant to `allUsers` or
  `allAuthenticatedUsers`, and an instance that authorizes `0.0.0.0/0`.

## 8. Generated build and runtime

### 8.1 Server entrypoint

A generator per server language, Go first, writes a `main` that:

- loads the generated config, derived fields included;
- connects the ORM from each database field;
- builds an SDK client for each `calls` edge, with the platform's service
  credentials (section 9.2);
- builds the auth middleware from the API's auth provider;
- mounts every served API on one router, with health and readiness
  endpoints;
- sets up OpenTelemetry and graceful shutdown.

The engineer writes the implementation of each served API, and nothing else
(section 8.5). The entrypoint calls each implementation's constructor with
its `Deps`. A mismatch between the code and the generated signature fails
to compile at level 2 of section 10.

### 8.2 Container image

A generated Dockerfile per server language builds the entrypoint and the
implementation together. For Go, that is a multi-stage build to a static
binary on a distroless base; TypeScript and Rust have their own
equivalents.

### 8.3 Local stack

`superschematic stack dev [<stack-service-dir>] [--environment <name>]`
runs an environment on the `local` target (`internal/stack/local`):

1. It builds the stack service and every service the stack reaches, each
   with its dependencies, the stack last.
2. It reads the environment the build resolved: `--environment`, or the
   stack's one environment on the local target.
3. It applies the deploy order (section 5.3) through the local provisioner,
   then stays in the foreground until Ctrl-C or until a server exits.
4. It stops the servers, callers first, then the container, which keeps
   its data for the next run. `--remove-database` removes the container
   and its data instead.

| Stack concept | local |
| --- | --- |
| database | one Postgres container per environment, `superschematic-<stack>-<environment>-postgres`, from `postgres:16-alpine` unless the `postgresImage` value names another; it publishes its port on 127.0.0.1 only and trusts every connection. A database per hosted DB schema, named after it in snake case (`shop_db`) |
| migration | each run plans with `sqlmigrate` from the model the database recorded (`superschematic-migrate status --model`) to the schema's model, and applies the plan with `superschematic-migrate`, expand and contract back to back, since no server of the previous version runs. The runner is on `PATH`, or where `SUPERSCHEMATIC_MIGRATE` says |
| server | a Go process built with `go build` (with `-mod=mod`) from its entrypoint module at `<output-root>/server/<stack>/<server>` (section 8.1). Its environment is its bindings, a derived field as one variable per member (section 3.4), and `PORT`, with nothing of the shell's but `PATH`, `HOME` and a few like them. It is ready once it answers `/readyz`, and each of its lines is printed with its name in front |
| sql edge | `postgres://postgres@127.0.0.1:<port>/<database>?sslmode=disable` |
| http edge | the callee's `http://127.0.0.1:<port>`, with a `signed-token` credential (D37): `iss` and `sub` the caller's deployable, `aud` the callee's, signed with an Ed25519 key pair per calling and called server. A call between two APIs one server serves stays on loopback with no credential |
| secret | a line `<Type>.<FIELD>=<value>` in `<schemas-root>/.superschematic/local/<stack>/<environment>/secrets.env` |
| port | a server's `port` setting and the `postgresPort` value, else a hash of the stack, the environment and the server: 20000 to 22767 for a server and 30000 to 32767 for Postgres, the same from run to run |

`<schemas-root>/.superschematic` holds what belongs to one machine: each
local environment's secrets file and the key pairs of its edges. It
ignores itself in git, and nothing in it reaches the output root.
`environment.json` and the rendered program name a secret by its ID and a
private key by a reference to its key pair node's `privateJwk` output,
which the provisioner reads when it starts the caller.

The provisioner renders `local.json` into
`<output-root>/program/<stack>/<environment>`: the containers, databases,
migrations and servers it runs. Beside it are the models `stack dev` writes
for it (`models/<service>.json`), the plans it applies
(`migrations/<service>.plan.json`) and the binaries it builds (`bin/`).
Each server runs in a process group of its own, so Ctrl-C reaches `stack
dev` first, which sends each server SIGTERM, callers first, and SIGKILL
ten seconds later.

Policy rules refuse what a local environment cannot hold: a domain
(`local-no-domain`), parameters (`local-no-parameters`), and two listeners
on one port (`local-distinct-ports`). The platform runs Go servers only,
until the TypeScript and Rust entrypoints exist.

Not built:

- The callee's half of service auth. Which config field gives a callee
  its verification keys is for the connectors and the generated entrypoint
  to define together, in one change; the key pair node's `publicJwk`
  output is what the local connector will put there. Until then the
  entrypoint does not start a server whose API has a service clause.
- Key rotation: a local key pair lasts until its file is removed.
- Restarting a server that exits: `stack dev` stops the environment.

The resolver is the same, so local and cloud differ only in their platforms
and connectors.

### 8.4 What the model needs from migrations

Migrations belong to `sqlgen`, for Postgres and SQLite, and D27 designs
them: `superschematic migrate plan` and the `superschematic-migrate` runner
(reference page "Schema migrations"). The stack model needs:

- an offline plan from the previously deployed schema to the new one. The
  baseline comes from the deploy manifest (section 11.2), so CI can show the
  SQL without a database;
- hazards on each step: destructive, rewriting or locking, or incompatible
  with the server version that is running. The deploy gate refuses a
  hazard the pull request has not acknowledged;
- an apply step a job can run: a Cloud Run job on GCP, the container
  locally;
- a check that a plan does not drop or retype a column that an `@source`
  view of a deployed API reads.

### 8.5 Where the implementation lives

The unit of implementation is the API service, not the server. Each API
service has one implementation per language, at a conventional location,
found with no declaration:

- **Location.** The naming file's `[implementation_paths]` table holds a
  path template per language, from the repository root (the parent of the
  schemas root), in which the service name fills `{service}`. `go`
  defaults to `go/{service}`. A distribution changes the template, not
  each service.
- **Scaffold.** When the package is missing, superschematic writes it
  once: an `implementation.go` whose `New` builds `Implementations` with a
  struct per namespace, each method returning the API package's
  not-implemented error, which answers 501. It never writes into a
  directory that holds a Go file, so the package is the engineer's from
  then on. `build --scaffold` and `build-all --scaffold` write it. A
  service the cache would restore builds again when its implementation is
  missing, since the cache stores outputs, not the scaffold.
- **Signature.** The API generator writes `Deps` and the constructor's
  signature in `deps.go`: `type Constructor func(deps Deps)
  (Implementations, error)`, which the scaffold asserts with `var _
  api.Constructor = New`. TypeScript and Rust get the equivalent later
  (section 12). `Deps` is typed and filled by the entrypoint:

  ```go
  type Deps struct {
      Config  EnvConfig              // the API's @envVars settings and derived fields (section 3.4)
      DB      orm.DatabaseInterface  // the ORM of its authDb, or of its one DB-kind dependency
      ShopApi *shopapisdk.ShopApiSDK // a Go SDK client per calls entry, with service credentials
      Logger  *zap.Logger
  }
  ```

  A field is left out when the API has nothing for it: `Config` without
  settings or edges, `DB` without a database. The logger is zap's, as the
  generated `Config`'s is, so the entrypoint passes one logger to both. A
  dependency must generate what `Deps` imports, its Go types (and so its
  ORM) for the database and its Go SDK for a callee, and the build refuses
  one whose config does not. The generated `Config` and `RegisterRoutes`
  do not change.

The scaffold is opt-in until the entrypoint lands. Nothing imports the
package before the generated `main` does (section 8.1), and a build of a
tree whose Go code lives elsewhere, such as `examples/acme-shop`, would
gain a stub package beside it. The entrypoint scaffolds each API a stack's
servers serve.

A server that serves several APIs calls each one's constructor with that
API's `Deps`, built from the server's shared connections and clients.

Not taken:

- A package path named on each server, such as `@server({ go:
  "example.com/acme/orders" })`. That is a string per server, and default
  servers would still need a convention.
- A `main` the engineer writes, calling a generated `Run(impl)`. That brings
  hand wiring back, and the server still has to name its main package.

## 9. End-user auth and service auth

These are two concepts, with separate credentials, context values and
checks. D37 in `docs/DECISIONS.md` records the decisions in this section.

### 9.1 End-user auth

End-user auth is what exists today: the auth providers of section 8 of
`docs/extension-model.md`, the `Authorization` header, the end-user
principal and `@requirePermission`. It answers who the person is, and this
model leaves it unchanged. Permissions belong to end users only; no service
holds one (section 9.7).

### 9.2 Service auth

Service auth answers which deployable is calling, at two layers:

- **Admission, at the platform.** Only a caller with an edge reaches the
  callee at all. Connectors derive this from edges: `roles/run.invoker` on
  Cloud Run, a NetworkPolicy on Kubernetes, a service binding on Workers.
  An exposed server admits every caller at this layer, since browsers call
  it.
- **Identity, in the application.** The callee knows the calling deployable
  as a `ServiceCaller`, separate from the end-user principal. The HTTP
  runtimes' `ServiceAuthenticator` establishes it (section 9.5), and it
  verifies the credential's signature on every platform, whether or not the
  platform admitted the call.

The service credential is a short-lived JWT on every v1 platform, so one
verifier in each runtime reads all of them. It knows JWTs and keys, not
clouds; what is specific to a platform is data the connector writes into
the callee's config (section 3.4).

| Platform | The caller sends | Lifetime | The callee checks |
| --- | --- | --- | --- |
| Cloud Run | a Google ID token whose audience is the callee's URL, from the metadata server | 1 hour; fetched again 5 minutes before it expires | RS256 against Google's keys; `iss` `https://accounts.google.com` or `accounts.google.com`; `aud`; `exp`; the caller's service account by its unique id in `sub` |
| Kubernetes | a projected service account token whose audience is the callee, read from the file the kubelet keeps current | 10 minutes, the shortest Kubernetes allows; the kubelet replaces it at 80% of that, and the caller reads the file again each minute | the signature against the issuer's keys; `iss`; `aud`; `exp`; the caller's service account in `sub` |
| local, and the generic connector (section 6.2) | a token the caller signs with the edge's Ed25519 key | 5 minutes | the signature against the edge's public keys; `iss`; `aud`; `exp` |

The callee's config holds, for each inbound edge, the issuer, the keys or
where to fetch them, the audience, the claim that names the caller, and the
deployable each caller identity is, with the APIs it serves. An identity
the config does not list is no caller, whatever signed its token. The
Kubernetes platform reads the issuer's keys from the API server's
`/openid/v1/jwks`, which default RBAC lets any service account read, with
the server's own token.

**Headers.** The credential travels in `Service-Authorization: Bearer
<token>` on every platform, and the `ServiceAuthenticator` reads only that
header. `Authorization` stays the end user's. On Cloud Run the caller also
sends the same token in `X-Serverless-Authorization` to a callee whose
invoker check is on, which is every server that is not exposed:

- Cloud Run admits a call by the ID token in `X-Serverless-Authorization`
  when the header is present, and in `Authorization` otherwise. The
  platform header is what lets an internal server take the end user's
  `Authorization` at all.
- Cloud Run removes that token's signature before the request reaches the
  container, so the application cannot verify that copy. It verifies the
  copy in `Service-Authorization` instead.
- An exposed server's invoker check is off, because browsers call it. There
  Cloud Run checks nothing, and its documentation does not say what it does
  to `X-Serverless-Authorization`, so callers do not send it and the
  application ignores it.

So the application verifies again on Cloud Run: on an internal server it
checks what the platform already checked, at the cost of one header; on an
exposed one it is the only check.

The generic connector's token:

- is a compact JWS with the header `{"alg": "EdDSA", "kid": <the key's
  RFC 7638 thumbprint>, "typ": "JWT"}`. RFC 9864 renames the algorithm
  `Ed25519`, which the Go and Rust JWT libraries do not read yet, so the
  caller writes `EdDSA` and the callee accepts both;
- carries `iss` and `sub`, the caller's deployable name in the
  environment; `aud`, the callee's; `iat`; `exp`, 5 minutes after `iat`;
  and `jti`, for logs;
- is accepted when its `kid` is one of the edge's keys, the key belongs to
  the deployable `iss` names, `aud` is the callee, and `exp` is in the
  future and at most 5 minutes after `iat`, with 60 seconds of leeway for
  clocks. The caller signs a new token when the one it holds has less than
  a minute left.

Each edge has two key slots. Each slot is replaced every 180 days, the two
offset by 90 days, by a `Rotating` node of the `time` provider in the
resource graph; the period is a connector setting. The callee accepts both
public keys and the caller signs with the younger private key. Callees
deploy before callers (section 5.3), so a callee holds a new public key
before any caller signs with it. A key pair is a credential the platform
generates, not a secret a person enters (section 4.2): the private key goes
into the caller's secret store, the public key into the callee's config.

The `local` target uses the same tokens. `stack dev` generates a key pair
per edge into the gitignored local file, so a local stack runs the code
path a deployed one does.

Not taken:

- Trusting the claims Cloud Run passes on in `X-Serverless-Authorization`.
  It saves a header, but every runtime would carry a mode that accepts a
  JWT without its signature, which is safe only while the invoker check
  stays on. Google does not document the header's handling with the check
  off, and one report says the signature is removed there too, unchecked.
- `X-Serverless-Authorization` as the service header everywhere, which an
  exposed Cloud Run server cannot verify.
- A verifier per platform in each runtime, linking a cloud's client
  library: D6 keeps the runtimes provider-neutral, and three runtimes would
  each need every cloud.
- The Kubernetes TokenReview API. It notices a token whose pod was deleted
  before the token expires, but it calls the API server per request and
  needs a Kubernetes client in each runtime. A 10-minute token bounds the
  same window offline.
- Mutual TLS, which Cloud Run does not pass to the container.
- HTTP message signatures (RFC 9421) over the method, path and body
  digest. They stop a token being replayed on another request within its
  lifetime, but every runtime and SDK would have to agree on the body's
  digest, and Google's and Kubernetes' tokens are bearer tokens anyway.
- ES256 for the generic connector's key, which WebCrypto supported first,
  but whose signatures are not deterministic, so the parity vectors could
  not be regenerated byte for byte. Every runtime the servers target verifies Ed25519 now: Go, Rust's
  `jsonwebtoken`, and WebCrypto in Node.js 22.13, Bun and Workers.
- No service auth locally, or a header that names the caller unsigned. It
  leaves a code path only production runs, and a mode that could ship.

### 9.3 Schema surface

An operation says who may call it with two decorators beside the end-user
ones, registered for operations and operation sets like `@rateLimit`. An
operation's own declaration replaces its set's.

| Declared | Who may call |
| --- | --- |
| `@auth`, `@requirePermission` or `@requireOwnership` (the user clause, as today) | an end user who meets it, directly or forwarded by a service (section 9.4) |
| `@requireService(...)` | only a listed service. No end user is looked at |
| `@requireService(...)` and a user clause | only a listed service, forwarding an end user who meets the user clause |
| `@allowService(...)` and a user clause | an end user who meets the user clause, or a listed service with no end user |

```ts
// schemas/services/shop-api/src/stock.schema.ts
import { ShopOrders } from "@acme/shop-orders";

export class StockMutations {
  // Only the orders server, placing an order for a user who may.
  @rest(HttpMethod.POST, "stock/reservations")
  @requirePermission(["orders.create"])
  @requireService({ from: [ShopOrders] })
  reserveStock(input: ReserveStockInput): Reservation {
    throw new Error("schema declaration only");
  }

  // A user who may, or the orders server on its own.
  @rest(HttpMethod.POST, "stock/reservations/{id}/release")
  @requirePermission(["stock.write"])
  @allowService({ from: [ShopOrders] })
  releaseReservation(id: Identity.UUID): Reservation {
    throw new Error("schema declaration only");
  }

  // Any server with an edge to shop-api, and no end user.
  @rest(HttpMethod.POST, "stock/reindex")
  @requireService()
  reindexStock(): ReindexResult {
    throw new Error("schema declaration only");
  }
}
```

- **`from`** is a list of API service handles. A listed service is the
  server that serves that API in the stack. Without `from`, every server
  with a `calls` edge to the API is listed. `from` narrows the edges and
  never widens them: a listed service without an edge is not admitted by
  the platform. A handle in `from` is an identity, so it adds no
  build-order edge (section 12), and two APIs may name each other.
- When a listed service admits an `@allowService` operation, it stands in
  for the end user: the user clause is not checked and no end user is
  authenticated. A service that is not listed, or that forwards a user it
  wants checked, goes through the user clause.
- The TypeScript reader refuses, and the verify pass refuses in a schema
  authored as IR: `@allowService` without a user clause (an operation only
  services call is `@requireService`); either decorator with
  `@publicRoute`, `@webhook` or `@hmacVerified` on one operation (a third
  party holds no service credential), its set's included; a
  `@requireService` operation, its own clause or its set's, whose `@mcp`
  publishes a tool (no end user's agent can call it, so the record says
  so with `hidden`); and both on one operation or one set. An
  `@publicRoute` operation opens its route even in a set with a service
  clause.

The IR records each declaration where it is written, on
`FieldDef.ServiceCallers` and `OperationSet.ServiceCallers`, a
`ServiceCallers{Mode, From}` where `Mode` is `require` or `allow` and
`From` holds the API service names, as it records the middleware trio, so
the data forms round-trip. `ir.EffectiveServiceCallers(set, op)` gives the
rule that applies, and the generators read that through `EndpointInfo`,
as they read `RequiresAuth`.

Resolution adds a check to those of section 5.2. Every `calls` edge from a
server C to an API A must reach at least one operation of A that C may
invoke:

- an operation open to anyone;
- an `@allowService` operation that lists C;
- a `@requireService` operation that lists C, when it has no user clause or
  C can forward an end user;
- an operation with a user clause and no `@requireService`, when C can
  forward an end user.

C can forward an end user when an API it serves has an operation with a
user clause. So an edge fails when every operation of the callee lists
other services or needs an end user the caller does not have: "orders
calls shop-api, but no shop-api operation admits orders"
(`unreachable-edge`). A handle in `from` that names a service the stack
does not deploy, or deploys without an edge, is not an error: an API is
written once and deployed in many stacks.

The OpenAPI document gains a `serviceAuth` security scheme, a bearer token
in the `Service-Authorization` header. OpenAPI's security list is an OR of
ANDs, so each row of the table above is one list: `[{bearerAuth}]`,
`[{serviceAuth}]`, `[{serviceAuth, bearerAuth}]` and
`[{bearerAuth}, {serviceAuth}]`. The tool manifest leaves out a
`@requireService` operation, which no end user's agent can call.

Not taken:

- One decorator with a mode argument, such as `@callers({ services,
  users: "or" | "and" })`. The pair reads as the rule it states, and each
  rule has one spelling.
- Narrowing in the stack, on `calls`, by operation name. It is a name in a
  string, and it puts the API's access rules in every stack that deploys
  it.
- Naming the calling deployable's class in `from`. A stack imports its
  APIs, so an API cannot import the stack's classes.

### 9.4 Delegation

A server that calls on behalf of a user forwards the user's
`Authorization`, unchanged, beside its own service credential. The callee
puts the service caller and the end user on the request context and checks
each against the operation's rule (section 9.3).

- **Forwarding is per call, from the request being served.** In Go, the
  generated entrypoint sets each client's end-user token hook to read the
  token of the request on the call's `context.Context`, so a handler that
  passes its context forwards. TypeScript and Rust have no context that
  every runtime carries across an `await`, so a call forwards when its
  options name the `RequestContext` it serves: `{ forward: ctx }` and
  `RequestOptions::forward(&ctx)`.
- **A client built for an edge holds no end-user token.** It has no static
  token and no refresh, since a server cannot refresh a user's session. A
  call with nothing to forward, from a background task say, carries the
  service credential alone.
- **The callee's end-user provider decides whether the forwarded token is
  good.** With the `session` provider it is when the callee's `authDb`
  holds the same Session table as the caller's, which is the case in a stack
  whose APIs share one auth database.
- **Token exchange belongs to the end-user provider, and v1 has none.** The
  core never mints, narrows or exchanges a user's token. A provider that
  wants narrower forwarded tokens would exchange them in the caller before
  the call, with the service credential as the actor token of RFC 8693; the
  core's part would be a hook on the forward option. Nothing needs it yet.

### 9.5 Runtime

Each HTTP runtime gains a `ServiceAuthenticator` and a `ServiceCaller`
beside the end-user `Authenticator` and principal:

| Runtime | Seam | The caller |
| --- | --- | --- |
| Go | `serviceauth.Authenticator`, `Authenticate(*http.Request) (*serviceauth.Caller, error)`, set on the router's `Config.ServiceAuthenticator` | `serviceauth.CallerFromContext(ctx)` |
| TypeScript | `ServiceAuthenticator`, `(ctx: RequestContext) => Promise<ServiceCaller \| null>`, a `buildRouter` option beside `authenticate` | `ctx.serviceCaller` |
| Rust | `ServiceAuthenticator`, `async fn authenticate(&self, &Parts) -> Result<Option<ServiceCaller>, ApiError>`, on `Implementations.service_authenticator` | `RequestContext.service_caller` |

A `ServiceCaller` has the calling deployable's name, the APIs it serves
(which `from` is checked against) and the credential's subject, for logs.
An authenticator returns no caller when the request carries no service
credential. It refuses a credential it cannot verify with 401, code
`service_unauthorized`, and a verified identity that is no caller of this
server with 403, code `service_forbidden`. A failure that is not the
caller's, such as keys it cannot fetch, answers 503. Each runtime ships one
implementation over the config of section 9.2, which the generated
entrypoint builds; a deployment with a credential that config cannot
express passes its own.

A route runs its steps in this order:

1. the `@hmacVerified` verifier (D26);
2. the rate limit, then the body limit (D29);
3. **the service step.** When the server has a service authenticator and
   the request carries a service credential, the authenticator verifies
   it, on every route. Then the route's service clause applies:
   `@requireService` refuses a missing caller with 401 and an unlisted one
   with 403, both with the service codes; `@allowService` with a listed
   caller skips step 4;
4. **the end-user step,** as today: authenticate the end user, then check
   the user clause with the permission matcher;
5. the rest of the route: Go's payload decryptor, the timeout and the
   handler.

The Go server authenticates the end user with the provider's
`AuthMiddleware` on its protected group, ahead of the route, and keeps
doing so for routes without a service clause, where the end user is
therefore authenticated before the service step. A route with a service
clause takes `AuthMiddleware` into its own chain at step 4 instead, so a
service that admits an `@allowService` route skips it.

When an operation has a service clause and the server has no service
authenticator, the Go server's `Config.Validate` refuses to start it, the
Rust crate does not compile, as D29 makes it for an end-user
authenticator, and the TypeScript router answers the route with 401, as it
does without `authenticate`. The TypeScript router takes a service
authenticator whether or not an operation has a clause, so its routes can
tell a delegated call from a direct one. The Go and Rust servers have the
service step only when an operation has a clause, and then on every
route, so their output for any other schema is unchanged.

End-user auth providers do not change. No auth snippet is added; a
provider never sees the service header and the service authenticator never
sees `Authorization`. The TypeScript `Principal` stops listing a service
identity among its subjects. D15 put service-to-service token verification
in each deployment's provider package; for services in a stack it is now
the runtime's.

### 9.6 SDKs

Each SDK's config gains a service credential source beside the end-user
auth config: a function from a `fresh` flag to a token (`serviceCredential`
in TypeScript, `ServiceCredential` in Go, `service_credential` in Rust and
Python), and the headers that carry it. The SDK sends the token on every
request, whether or not the API has an operation that needs a caller: in
`Service-Authorization`, and also in `X-Serverless-Authorization` when the
edge's config says the callee is a Cloud Run server whose invoker check is
on (section 9.2).

A 401 with the code `service_unauthorized` asks the source for a fresh
token once and retries. It never runs the end-user refresh, which today
runs on any 401, and an end-user 401 never asks the service source. Cloud
Run's own refusals carry no problem code, so they are end-user 401s to the
SDK, and a client built for an edge has no end-user refresh to run.

The runtimes ship a source for each row of section 9.2's table: a Google ID
token from the metadata server, a projected token read from its file, and
a token signed with an edge's key. Each caches its token and fetches or
signs a new one before expiry. The generated entrypoint builds one client
per `calls` edge, with the callee's URL and the source the edge's derived
config field names (section 3.4). Python has the config slot and no
sources, since no server is written in Python.

### 9.7 Permissions

Services hold no permissions. `@requirePermission` checks end users only,
and an operation admits a service through `@requireService` or
`@allowService`. A service's authority is its edge, declared once in
`calls` and narrowed per operation by `from`.

Not taken: permissions granted to services, in the stack or in the auth
database, and checked by `@requirePermission`. A grant restates the edge,
and lives where nothing checks it against `calls`. The provider's
permission matcher and role store know end users, not deployables. And a
service that holds an end user's permission is one principal standing in
for two, which D30 rejected.

### 9.8 Testing and parity

`runtime/http/testdata/serviceauth_parity.json` holds shared vectors, as
`runtime/schema/testdata/validation_parity.json` does for validation:

- the keys (RSA, P-256 and Ed25519, for tests only), the callee configs
  built from them, and tokens signed with them;
- per vector: the clock, the route's rule (service clause, `from`, user
  clause), the request's headers, and an end-user authenticator stub keyed
  by token;
- the expected status and code, the `ServiceCaller` and end user on the
  context, and whether the end-user authenticator ran.

The cases cover each row of section 9.3's table; expiry, a token not yet
valid, the wrong audience and the wrong issuer; an unknown `kid`, `alg:
none` and an RS256 key used as an HS256 secret; an identity no config
lists; a caller left out of `from`; both credentials invalid on a route
with a service clause; a forwarded user on each rule; and a service
credential on a route with no service clause.

A Go test writes the file with `-update`, as `TestRuntimeParityCorpus`
writes the validation corpus. Go, TypeScript and Rust each read it in the
runtime's own tests and run their gate with a fixed clock and a stub key
endpoint. Rust's envelope differs from the RFC 9457 body of Go and
TypeScript (D29), so the vectors compare status and code, not bodies.

The generator tests compile a fixture API with each rule in all three
servers and run requests through it, as D26's and D29's do: the order
against the verifier and the rate limit, the codes, and a schema without
the decorators unchanged byte for byte. The SDK tests check the retry: one
fresh service token on `service_unauthorized`, and the end-user refresh
left alone.

### 9.9 Open

- A deployable that serves no API, such as a job (section 3.1), has no
  handle to put in `from`. Until jobs land with a way to name one, it may
  call only operations whose `from` is empty.
- A credential the callee config cannot express, such as a service mesh's
  mTLS identity in `X-Forwarded-Client-Cert`. A deployment can pass its
  own service authenticator today; a platform kind of credential can come
  with the first platform that needs it.
- Workers service bindings. A call over a binding carries no token; the
  caller's binding config sets `ctx.props`, which Cloudflare documents as
  safe to trust unsigned, and the platform delivers it beside the request
  rather than in it. The Workers platform (section 6.8) decides whether the
  TypeScript service authenticator reads it there, or whether Workers
  callers sign key-pair tokens like the generic connector's.
- The cluster's certificate authority. A Kubernetes API server serves its
  keys over TLS signed by the cluster's own CA, which the runtimes' default
  HTTP clients do not trust. Until the Kubernetes platform lands, a
  deployment passes a key fetcher or HTTP client that trusts it; the
  platform may add a CA to the callee config instead.

## 10. Validation and simulation

Each level runs the same static schemas through one more step:

| Level | Needs | Catches |
| --- | --- | --- |
| 1. resolve | nothing | config fields with no binding, kind mismatches, dangling edges, unrealizable placements, policy rules |
| 2. render and compile | Go | the entrypoints and the rendered program build against their runtimes |
| 3. graph checks | nothing | properties against the pinned provider schemas; resource rules such as least-privilege IAM and nothing public unless exposed |
| 4. local stack | Docker | real servers and Postgres with migrations applied; end-to-end tests through the SDKs |
| 5. migration plan | nothing (the baseline comes from the manifest) | the SQL plan and its hazards |
| 6. cloud preview | the read-only `planner` account | the exact resource diff for an environment |
| 7. parameterized environment | the `deployer` account | the real thing, per pull request |

Levels 1 to 5 need no cloud credentials. Checked-in golden copies of each
`environment.json` make a wiring change readable in review: "this pull
request adds an edge and grants `run.invoker`".

## 11. Deploys and CI

### 11.1 Commands

The core adds a `stack` command group: `init`, `bootstrap`, `secrets set`,
`dev`, `plan`, `deploy`, `destroy` and `outputs`. Targets and provisioners
plug into it; they add no commands of their own. `stack dev` runs a local
environment (section 8.3).

### 11.2 Deploy

`stack deploy <environment>`:

1. builds the images of the affected servers;
2. applies infrastructure;
3. plans each database's migration from the model the manifest records
   (D27), checks that it is the plan the pull request showed, and runs its
   `expand` steps (`superschematic-migrate apply --phase expand`), which
   keep the servers of the previous version working;
4. rolls servers callee first, waiting for readiness;
5. runs the plan's `contract` steps (`--phase contract`), the drops and
   tightenings that the previous version's servers could not survive, once
   none of them runs;
6. applies exposure;
7. writes a deploy manifest to the state bucket: the resolved environment,
   the IR digest of each service, the image digests and each database's
   applied model.

A rollout that fails runs no `contract` step, so the previous version's
servers keep working on the expanded schema. The runner records that
schema as the database's model, and the manifest records it too. The next
deploy plans from it: its plan supersedes the pending `contract`, and any
drop still wanted is in its own `contract` (D27, amended).

The manifest is the migration baseline, the record of what is running, and
the starting point for a rollback.

### 11.3 Generated CI

A workflow per stack is installed into `.github/workflows/`, as an install
target in the way `InstallTargetDir` serves charts:

- **On a pull request:** levels 1 to 6, plus a parameterized environment
  when the stack declares one.
- **On merge:** deploy the first environment.
- **Later environments:** deploy on approval, through GitHub environments.

The workflow authenticates through Workload Identity Federation, as
`planner` for previews and `deployer` for deploys. Those are bootstrap's
accounts, so no account name is copied by hand. Only affected servers are
built and deployed, worked out from `.deps.json` and the stack's edges.

GitHub Actions is the first CI renderer. Like provisioners, others are
registrations.

## 12. Core changes

1. **IR.** The IR records the config's `authDb`, `dependencies` and
   `calls` (`ir.Schema`'s `AuthDB`, `Dependencies` and `Calls`), so
   generators and the resolver read every reference from it.
   Landed: the Stack IR types, the stack (`ir/stack.go`), the resolved
   environment (`ir/stack_environment.go`) and the resource graph
   (`ir/resource_graph.go`). A Stack schema's class carries its
   declaration on its `TypeDef` (`Stack`, `Server`, `Database` or
   `Environment`), which the data forms write, and `ir.StackOf`
   assembles a schema's declarations into its stack (section 4.1).
   Operations and operation sets gain `ServiceCallers` (section 9.3).
2. **Loader:**
   - Landed: class values in the arguments of any registered decorator.
     The argument evaluator reads a class, local or imported from another
     service's package, as the class reference `{"class": name}`, and the
     data forms write the same object (extension-model.md section 3.4), so
     `settings: [{ of: Backend }]` needs no walker code of its own.
     `@source`, `@versionGraph` and `@graphMember` stay walker-read;
     `internal/registry/core_decorators.go` says why.
   - Done: `ServiceHandle` typed by kind and config type
     (`ServiceHandle<"API", ShopApiConfig>`, in
     `packages/schema-config/src/index.ts`), written by the sentinel
     generator (section 4.3), so TypeScript can restrict a handle argument
     and type its settings. `@ts-expect-error` cases in
     `packages/schema-config/src/service-handle.typecheck.ts` pin it.
     Build-plan discovery, which `build-all` and `build --with-deps` run,
     checks each config handle's kind against the service it names
     (`validateHandleKinds` in `internal/buildplan/buildplan.go`).
   - Done: references from a schema's body (D41). A service handle in a
     decorator argument, such as `deploy` or a settings element's `of`,
     references its service, so a stack does not restate it in
     `dependencies`. `ir.Schema.References` records it, and the build
     cache keys the referencing service on the sources of the referenced
     service and of every service its config reaches, so the stack's
     output rebuilds when any of them changes. A reference is a cache
     edge, not a build-order edge: resolution reads the referenced
     services' IR and configs, which a build loads from their sources,
     and none of their outputs, so the build plan does not order them and
     two services may name each other. A decorator declares the argument
     paths whose handles only name a service (`DecoratorSpec.Identities`,
     for D37's `from`): an identity adds no edge, and the cache tracks only
     the sentinel it was imported from. Should an output compile against
     a referenced service's generated code, such as an entrypoint that
     imports a served API's package, that output will need a build-order
     edge.
3. **envgen.** The derived binding fields of section 3.4. Landed: the
   contract of the values connectors derive, with its environment
   variable encoding (`ir/derived_value.go`), and the resolver's check of
   every connector's value against it. The Go loader's `EnvConfig`, with
   a field per database and per `calls` entry named by `[derived_fields]`
   and read through the Go HTTP runtime's `stackconfig`;
   `values-schema.json` marking those fields derived; and the loader's
   refusal of an `@envVars` field that collides with one. Next: the
   TypeScript and Rust loaders read the derived fields, in the PR that
   gives them `Deps`, and the service-auth field arrives with the
   connectors that write it.
4. **Generators.** The server entrypoint, the Dockerfile, each API's `Deps`
   and constructor signature, and the one-time implementation scaffold
   (section 8.5). Landed for Go: `Deps` and `Constructor` in `deps.go`, and
   the scaffold under `build --scaffold` and `build-all --scaffold`. Next:
   the entrypoint and the Dockerfile, then `Deps`, the constructor
   signature and the scaffold in TypeScript and Rust, in a later PR.
   `examples/acme-shop/go` keeps its hand wiring until the entrypoint
   lands. Moving it to the scaffold layout now would move the code its
   docs pages quote (`go/products.go`, `go/orders.go`, `NewHandler`) for
   no running server.
5. **Config and build plan.** `calls` is in the schema config, beside
   `authDb`, in the TypeScript type and the data-form schema, valid on an
   API config and naming API services. It is a build-order edge for the
   caller's API server only: the build plan orders outputs, so two APIs
   may call each other, and each callee's key joins the caller's cache key
   (section 3.3). A config imports its handles as siblings' sentinels
   (D34): the import rule is in the static config read, and the sentinel
   sweep runs before discovery. The naming file's `[implementation_paths]`
   holds the implementation path templates, and `[derived_fields]` the
   derived field names.
6. **Runtimes.** `ServiceAuthenticator` and `ServiceCaller` in the Go, Rust
   and TypeScript HTTP runtimes (section 9.5), and a service credential
   source in the SDKs (section 9.6).
7. **Registry.** The specs of section 6.7, and the resolver that drives
   them (section 6.10). Landed: `internal/registry/stack.go`, the resolver
   in `internal/stack` with its public face in `stack`, and the acceptance
   extension `stack/stacktest`. Landed too: the core `Stack` kind with
   `@stack`, `@server`, `@database` and `@environment`
   (`internal/registry/core_stack.go`, authored from
   `@superschematic/stack`), and its generator, `stack`
   (`internal/generator/stackgen`). Its `Service` reads a service's
   `stack.Service`, an API's operations for section 9.3's check included,
   from the service's IR and its config's outputs, and
   `registry.Options.LoadDependencyConfig`, which every build sets, gives
   it each config.
8. **CLI.** The `stack` command group.

## 13. Module layout

- **The root module:** the Stack kind, the resolver, the registry specs,
  the `local` target and the `stack` commands.
- **`extensions/gcp`**, a Go module of its own (D1): the gcp target's
  platforms, connectors and Cloud DNS platform, its policy rules, and its
  pinned provider schemas with the tool that keeps them current (sections
  6.4 and 7). Bootstrap is to come.
- **`extensions/pulumi`**, a Go module of its own: the provisioner and the
  binding generator (sections 6.5 and 6.6). Built: it registers provisioner
  `pulumi`, its `bindings` package is the generator, and it joins
  `make test` and CI.
- **`extensions/cloudflare`**: the Cloudflare DNS platform in v1, and
  Workers and D1 later.
- **`cmd/superschematic`**, a Go module of its own: the installed binary.
  Built: it is a distribution of the core and the official extensions, by
  `cli.New(cli.Config{Name: "superschematic"}, gcp.Extension{},
  pulumi.Extension{ProviderVersions: map[string]string{"gcp":
  gcp.ProviderVersion}})`, so the provisioner installs the gcp provider at
  the release whose schemas the target checks against.
  `extensions/cloudflare` joins the list when it lands. An engineer
  installs one binary and gets every official target. `extensions/topcoat`
  is not linked (D44); its own binary links it. A release builds this
  binary and tags the module with the others.

The Pulumi SDK and the GCP client libraries stay out of the root module, as
the compiler keeps its TypeScript parser out of the runtimes. The root
module never depends on an extension module.

The installed binary is no longer the core-only program. Goal 2 of
`docs/extension-model.md` still holds: `cli.New(cli.Config{})` is the
core-only program, `internal/cmd/superschematic-core` in the root module.
It is never shipped, and `internal/` says so: it is not a second binary to
install. The checks that prove the core works with no extension linked
run it (`make cli-smoke`, the examples' scripts), and so does `make
behaviors`, which needs only the core. A downstream distribution links
whichever official extensions it wants beside its own, in the same way.
Every module keeps its `replace` directives in a release, and `go install`
at a version refuses a module that has any, so
`go install .../cmd/superschematic@<version>` does not work: the binary
comes from a release's download or from `make build` in a checkout.

Not taken:

- A second binary beside a core-only `superschematic`, which would make
  users choose a binary by task.
- Targets and provisioners as separate executables the core starts at
  deploy time, as Terraform loads providers. That adds a versioned protocol
  between processes, and section 2 of `docs/extension-model.md` rules out
  loading code at run time. Revisit it only if third parties need to ship
  a target without rebuilding the binary.
`extensions/deploy` and `extensions/platform` are rewritten over the stack
model, or retired, when it lands.

## 14. Milestones

1. **Wiring with no cloud.** Derived config fields, the Go entrypoint, the
   `local` target and `stack dev`. Done when acme-shop runs end to end and
   nothing in `examples/acme-shop` writes a connection string, a URL or a
   port by hand.
2. **Model, resolver and seams.** The Stack kind, environments,
   `environment.json`, the registry specs, and levels 1 to 3 in CI.
   Done when a test extension adds a platform and a provisioner with no
   core edit.
3. **GCP and Pulumi.** Bootstrap, the gcp platforms and connectors, the
   Cloud DNS and Cloudflare DNS platforms, the Pulumi provisioner, Cloud
   Build, secrets, `plan` and `deploy`. Done when
   a fresh project plus a project id and a region gives a live acme-shop.
   A nightly job proves it against a sandbox project.
4. **Service auth.** Admission and identity (section 9) on Cloud Run.
5. **Database lifecycle.** The `sqlgen` migration plan and apply step in
   deploys, the hazard gate and the deploy manifest.
6. **CI generation and parameterized environments.**
7. **Breadth.** Jobs and scheduled jobs, buckets, queues and static sites,
   and a second target (GKE or Cloudflare) added as a registration, with
   the generic connector (section 6.2) so compute can mix.

## 15. Open questions

1. **The binary.** Settled: the installed binary is a distribution of the
   core and the official extensions, in a Go module of its own (section 13).
2. **Environments that mix targets.** Settled. Placement is per deployable
   in the IR from v1, and the target is the default (section 6.3). The
   generic connector, with a key pair per edge, lands with the second
   target (section 6.2). DNS is a platform kind from v1 (section 6.9).
3. **Typed settings in TypeScript.** Settled: handles carry their kind and
   config type, targets augment a `Targets` interface, and `@environment`
   infers each settings element (section 4.3).
4. **Shared secrets.** Settled: a secret is identified by the type that
   declares the field and the field's name, so servers that include the
   same declared field share one secret (section 4.2).
5. **Where a server's implementation lives.** Settled: one implementation
   per API service per language at a naming-file path template, scaffolded
   once, with a generated `Deps`. `calls` moves to the API service's config
   (sections 3.3 and 8.5).
6. **The resource vocabulary.** Settled: Pulumi's package schemas, with
   each pinned type's Terraform name and property renames recorded beside
   it from v1, and a round-trip test with the first Terraform-family
   provisioner (section 6.4).
7. **References in `schema.config.ts`.** Settled: a config may import a
   sibling's sentinel (`import { ShopDb } from "@acme/shop-db"`) wherever
   a handle goes, and `service({ name, kind })` stays as the data form and
   the fallback spelling. The import rule moves into the static read every
   command shares, and the sentinel sweep, which reads only `name` and
   `kind`, runs before build-plan discovery (D34). A schema file may pass
   an imported sentinel to a decorator too: the handle references the
   service, a cache edge that orders nothing, unless the decorator
   declares it an identity, which adds no edge (D41).

## 16. What the source tree taught

The source tree's generator grew a deploy family for a distribution:
## 17. What the distribution's deploy family taught

A distribution built a deploy family on the source tree's generator:
`resourcesgen`, `chartgen`, `argogen`, `helmvaluesgen`, `mergedvalues`,
`stacksgen` and `suitesgen`.

Kept:

- environment kinds with a rule per kind for building an address, which
  become platforms and connectors here;
- one edge graph that drives both addresses and network rules;
- completeness checks of values against `@envVars`;
- deploy-time placeholders kept out of generated files, which become
  parameters here;
- hand-written configuration layered over generated output, which becomes
  bindings here rather than raw values files.

Not kept:

- an executable TypeScript model flattened to string handles and resolved
  again in Go, with validation in three places and derivations written in
  two languages;
- names restated and then checked for agreement instead of derived:
  service names, identity lists, the env-var names on edges;
- connection strings built inside Helm templates, where the model cannot
  see them;
- raw Helm and YAML strings inside the model;
- a closed set of environments, and one cloud built into the generators.
