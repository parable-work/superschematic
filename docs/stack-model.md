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
| job | one `@job` of an API schema | a run to completion, on a schedule or on demand | its API's needs: the same connection and addresses (D52) |
| worker | one `@worker` of an API schema | a loop that handles a queue's messages until stopped | its API's needs; its queue lives in one of the API's databases (D53) |
| bucket | one Bucket schema | object storage, private, with signed URLs | nothing (D54) |
| site | one Site schema | static files over HTTPS, with a config per environment | the public address of each API it calls (D55) |

A queue is not a deployable: it lives in the database of the DB schema
that declares it (section 8.8). A second target, and the generic
connector that lets compute mix across targets, come later. Each kind
lands the same way: a deployable kind, a platform per target that
realizes it (section 6.1), and the edges it takes part in.

### 3.2 Defaults

With nothing declared, each API service in a stack is one server, each
`@job` of an API service is one job, and each DB service is one database.
A deployable is declared only to change that:

- to run several APIs in one process;
- to host several DB schemas on one database.

A declared deployable only groups. It declares no needs: a server's edges
are the union of its APIs' edges, so grouping APIs never restates one.

### 3.3 Edges

An edge is a need met by something that provides it. v1 has three kinds:

| Edge | From | To | Derived from |
| --- | --- | --- | --- |
| sql | server or job | database | the database each served API already names: its `authDb`, or its one DB-kind dependency, as `resolveUpstreamAuth` in `internal/generator/dispatch.go` reads it |
| http | server or job | server | `calls` in the config of each API the calling server serves |
| site | site | server | `calls` in the Site service's config; the server must be exposed (D55, section 8.10) |

A site edge derives the API's public address and nothing else, since the
browser carries its end user's token. Resolution refuses a site that
calls an API whose server the stack does not expose
(`site-calls-unexposed`), which no browser reaches.

A job takes its API's edges, from itself (D52): the sql edge to the API's
database and an http edge to the server of each API its API calls. A job
of an API that a declared server serves with the API it calls still
calls over HTTP, with a credential, since it runs in a process of its
own.

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
- a callers field per served API with a service clause, `<API>_CALLERS`
  (`SHOP_API_CALLERS`), named by the core's rule alone over the API's own
  name. It holds what the server's `ServiceAuthenticator` checks: the
  issuers it accepts, with their keys and audience, and the deployable
  each caller identity is, which the connectors of the http edges to the
  API write together (section 9.2);
- an identity config field per served API whose server authenticates with
  the identity runtime (D50: its `authDb` declares the user model),
  `<API>_IDENTITY` (`SHOP_API_IDENTITY`), named as the callers field is.
  It holds the identity runtime's config as one JSON string: the
  session's lifetime, the cookie, the trusted origins and the password
  hash's cost. The environment sets it with its `env` settings, a literal
  or a parameter; without one the server's platform gives its identity
  config (`PlatformSpec.IdentityConfig`), as the `local` platform's turns
  the session cookie's `Secure` off over plain HTTP; without either it is
  unbound, and the server runs with the runtime's defaults. Its binding
  names the API in `identityOf`. A job has none (section 8.7);
- a CORS field per served API that a site calls, `<API>_CORS`
  (`SHOP_API_CORS`), named by the core's rule over the API's own name as
  the callers field is. It lists the public origin of each site whose site
  edge reaches the API, which its server answers CORS for and no other
  (D55, section 8.10).

A site has no config of its own to fill: its one binding per site edge,
named after the API it reaches, is what the deploy writes into the site's
config for the environment, which the browser reads when the site loads.

An API's database field comes from its `authDb`, or its one DB-kind
dependency, as its sql edge does (section 3.3).

The Go loader has the first two. The API package's `EnvConfig` embeds the
`@envVars` type and adds a field per edge, a `stackconfig.Database` or a
`stackconfig.Service` from the Go HTTP runtime, which `LoadEnvConfig`
reads. `values-schema.json` lists each derived field in
`x-superschematic.envVars` with `derived` (the edge kind), `service` and
`variables`, and each of its variables as an optional string property
that the platform sets, not a deployment's values. The callers field is
in neither Go's `EnvConfig` nor `values-schema.json`, since its variables
follow the environment's edges: the generated entrypoint reads it with
`stackconfig.LoadCallers` (section 8.1). The TypeScript API package's
`EnvConfig` holds all three, read through the TypeScript HTTP runtime's
readers (section 8.6). The Rust loader reads no derived field yet (section
12).

What a connector derives for each edge kind has a contract, in
`ir/derived_value.go` and `ir/service_auth.go`. Resolution checks every
connector's value against it and refuses one that breaks it with a
`lowering` failure that names the member at fault:

| Edge | Value | Members |
| --- | --- | --- |
| sql | `ir.DatabaseConnection` | `url`, a connection string; or `cloudSql`, a Cloud SQL connector configuration: `instance` (the instance connection name), `database` and `user` (the IAM database user) |
| http | `ir.ServiceEndpoint` | `url`, the callee's base URL; and an optional `credential`: its `source` (`google-id-token`, `token-file` or `signed-token`, the runtimes' sources of section 9.6), the settings that source reads (`audience`, `tokenFile`, `issuer`, `key`), and the `headers` that carry it, which include `Service-Authorization` |
| http, for the callee's `<API>_CALLERS` | `ir.ServiceAuth` | `issuers`, each an `ir.ServiceAuthIssuer`: `issuer` and `issuerAliases`, `audience`, `algorithms`, `jwksUrl` or `keys` (each a `jwk`, a public JWK's JSON), `subjectClaim`, `maxLifetimeSeconds`, and `callers`, each a `subject`, the `deployable` it is and the APIs it `serves`. A connector gives one issuer per edge, listing the edge's caller (`Connected.Callee`), and resolution merges the edges to the API by issuer (section 9.2) |
| site | `ir.SiteEndpoint` | `url`, the API's public base URL, its server's `PublicAddressOf` (section 6.1) |
| site, for the callee's `<API>_CORS` | `ir.CORSPolicy` | `origins`, the public origin of each site whose site edge reaches the API, in the order of the edges, each once: `<scheme>://<host>[:<port>]` or a reference. Resolution writes it from the sites' public addresses (D55) |

A member holds a string or a reference to an output or a parameter. A
credential's `source` and `headers` are literals, as are an issuer's
aliases, algorithms, subject claim and maximum lifetime, a whole number,
and a caller's deployable and the APIs it serves. A member the contract
lacks, or that the credential's source does not read, is refused.

In environment variables, a derived field is one variable per member:
the field's name, an underscore and the member's path in upper snake case,
with a list of strings joined by commas (`SHOP_DB_DATABASE_URL`,
`SHOP_DB_DATABASE_CLOUD_SQL_INSTANCE`, `SHOP_API_SERVICE_URL`,
`SHOP_API_SERVICE_CREDENTIAL_HEADERS`). A list of strings one of which is
a reference, such as the origins of a CORS field whose site's address is
an output, is one concatenation of them and the commas
(`SHOP_API_CORS_ORIGINS`, D55). A list of objects, or an empty
list, is a variable that holds the list's length, and each object's
members follow the list's name and the object's index
(`SHOP_API_CALLERS_ISSUERS=1`, `SHOP_API_CALLERS_ISSUERS_0_AUDIENCE`). A
whole number is its decimal. `ir.DerivedVariables` encodes a value that
way for platforms, and the generated loaders read it back.
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
code never names an environment variable. It replaces a hand-written
`main` that connects with `os.Getenv("DATABASE_URL")`, as
`examples/acme-shop` did before its stack.

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
  dns: { cloudflare: { zone: "acme.dev", zoneId: "023e105f4ecef8ad9ca31a8372d0c353" } },
})
export abstract class Staging {}

@environment({
  target: "gcp",
  gcp: { project: "acme-prod", region: "us-east1" },
  domain: "acme.dev",
  settings: [
    { of: ShopDb, tier: "db-custom-2-7680", highAvailability: true },
    { of: Backend, minInstances: 1, env: { LOG_LEVEL: "warn" } },
    { of: ShopOrders, job: "ShipOrders", schedule: "*/5 * * * *" },
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
  the entry points, API, DB and Site services. Everything they reach
  through `authDb`, DB dependencies and `calls` joins the stack, so
  `shop-db` needs no mention, and a site brings the APIs it calls.
- **`expose`** names what is reachable from outside the environment, an
  API's handle or an `@server` class. Everything else is internal, and
  reachable only along its edges. A site is always exposed, named here or
  not, and every API a site calls must be (D55, section 8.10).
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
  another platform than the target's, and every key but `of`, `job`,
  `platform`, `env`, `schedule`, `timeZone` and `enabled` is a platform
  setting, which the loader checks against that platform's settings
  schema. `env` binds the server's `@envVars` fields, each to a literal
  or to `{ parameter }`, a parameter of the environment that the deploy
  run supplies. tsc checks the same in the editor (section 4.3).
- **A job's settings** name the job beside its API's handle: `{ of:
  ShopOrders, job: "ShipOrders", schedule: "0 * * * *", timeZone:
  "America/New_York", enabled: false }` (D52). `schedule` and `timeZone`
  replace the `@job` decorator's, and `enabled` turns the schedule on or
  off; every other key is the job platform's setting, and `env` binds the
  job's fields, its API's, over what the API's server is given. Settings
  merge over the `extends` chain as a server's do. A schedule runs unless
  the settings turn it off, except in a parameterized environment, whose
  members run none unless their settings turn it on (section 8.7).
- **`Preview extends Staging`** inherits Staging's values, and `parameters`
  makes it a family of environments, one per value (section 5.4). An
  `@environment` class extends only another `@environment` class.

Every class of the schema carries one of the four decorators and holds no
fields. The kind's verification checks that, and that each class a
declaration names is an `@server` or `@database` class of the schema, so
the data forms are held to it too. Each decorator writes a declaration on
its class's `TypeDef` (`Stack`, `Server`, `Database` or `Environment`),
and `ir.StackOf` assembles them into the `ir.Stack` the resolver reads,
its deployables in name order and its environments in the order they are
declared, which the generated CI deploys them in (section 11.3). The
TypeScript reader numbers each `@environment` class
(`EnvironmentDecl.Order`): schema files in path order, and the classes of
a file in source order. A data form writes `order` itself, and an
environment without one comes after those with one, by name (D47); the
kind's verification refuses two environments with one order. The
TypeScript writer writes the environment classes last, in their order,
and leaves the property out.

Every declaration has the JSON and YAML data forms every schema has. A
class is a type, and its declaration the key the decorator writes. A
handle is written `{name, kind}`, and a settings `of` or an `expose` entry
`{service: {name, kind}}` or `{deployable: Backend}`, and a job's `of`
`{service: {name, kind}, job: ShipOrders}`. The target's values are
`values`, a job's `schedule`, `timeZone` and `enabled` keys of their own,
the DNS platform `{platform, values}`, an env value `{value}` or
`{parameter}`, and the parent the type's `extends`:

```yaml
types:
  Production:
    name: Production
    role: EmbeddedStruct
    environment:
      order: 3
      target: gcp
      values: { project: acme-prod, region: us-east1 }
      domain: acme.dev
      settings:
        - of: { service: { name: shop-db, kind: DB } }
          values: { tier: db-custom-2-7680, highAvailability: true }
        - of: { deployable: Backend }
          values: { minInstances: 1 }
          env: { LOG_LEVEL: { value: warn } }
        - of: { service: { name: shop-orders, kind: API }, job: ShipOrders }
          schedule: "*/5 * * * *"
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

A value is entered with `superschematic stack secrets set <environment>`.
It asks, with the terminal's echo off, for every secret of the
environment that has no value and every platform credential that has
none (section 7.3), or for the one secret named
(`PaymentsSecrets.STRIPE_KEY`), which it asks for even when it has a value,
to replace it. Each value goes through the target's secret store, keyed
by the secret's identity, to Secret Manager on GCP, and never into a log.
For a local environment it goes to the `secrets.env` that `stack dev`
reads (section 8.3), the one file a value is written to. A parameterized
environment's members share its secrets, so `secrets set` takes no
parameter values.

On a cloud target the secret itself, without a value, is a node of the
environment's graph, so on a fresh environment `stack deploy` creates it
in its infrastructure step. The deploy then needs a value for every secret before its next step:
at a terminal it asks for each one missing, and elsewhere, as in CI, it
stops and names them, and `secrets set` gives them. `secrets set` on a
secret whose storage does not exist yet says to deploy first. Resolution
checks that every secret field has a binding; `stack plan`, a cloud
preview (section 10), lists the secrets with no value.

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
  so tsc refuses a DB handle there. A third parameter, `J`, names an API's
  jobs, its `@job` classes (D52): `service<"API", OrdersConfig,
  "ShipOrders">`, with `unknown` for the config type of an API without
  one. It defaults to `string`, any job, and the sweep keeps it as it
  keeps the config type.
- **Targets type their own values and settings.** `@superschematic/stack`
  declares an empty `Targets` interface. Each target's authoring package
  augments it with the target's environment values and a settings type per
  deployable kind, the way D16 types each behavior's config:

  ```ts
  declare module "@superschematic/stack" {
    interface Targets {
      gcp: { values: GcpValues; server: CloudRunSettings; database: CloudSqlSettings; job: CloudRunJobSettings };
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
  - an API handle beside a `job`, which must be one of the handle's jobs,
    takes the target's `job` settings, a `schedule`, a `timeZone` and
    `enabled`, and an `env` typed from the handle's config type (D52);
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
3. servers and jobs, callees before callers, so a new caller never meets
   an old callee (a job calls what its API calls, and nothing calls a
   job);
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

Six registrations keep platforms and tools independent of each other and
of the core:

- a deployable is placed on a **platform**;
- an edge between two placed deployables is realized by a **connector**;
- a **target** names a platform for each deployable kind;
- a **DNS platform** holds an environment's domain records (section 6.9);
- a **provisioner** turns the resulting resource graph into running
  resources;
- a **CI renderer** writes a stack's workflow for one CI system from its
  resolved environments (section 11.3).

### 6.1 Platform

A platform realizes one deployable kind on one runtime: Cloud Run servers,
Cloud SQL databases, local processes, a local Postgres container. It
registers a `PlatformSpec`:

- `Kind`, the deployable kind, and what it accepts: `Languages` for a
  server or job platform, spelt as `outputs.api.language` spells them
  (`GO`, `TYPESCRIPT`, `RUST`), since a job is written in its API's
  language, or `Dialects` for a database platform (`postgres`, `sqlite`),
  in order of preference;
- `Settings`, the JSON Schema of its settings (`minInstances`, `tier`);
- `IdentityConfig`, for a server platform, the identity config (a JSON
  object) a server on it runs each API over the user model with unless its
  environment sets the API's identity config field (section 3.4, D50); the
  `local` platform's turns the session cookie's `Secure` off;
- `NameOf` and `AddressOf`, how it names and addresses a deployable in an
  environment. Under a parameter the name references the parameter
  (`{"$concat": ["shop-api-", {"$parameter": "pr"}]}`), and an address
  usually references an output of one of the deployable's nodes;
- `PublicAddressOf`, where a browser reaches an exposed deployable from
  outside the environment, beside `AddressOf`, where an edge inside it
  does (D55). Resolution asks it for each exposed deployable and records
  it in `environment.json` as `publicAddress`. A site edge derives the
  API's from its server's, and a site's own is the origin the CORS field
  of each API it calls lists, so a site platform must have one. The local
  target's is the loopback URL, as its address is. On gcp it is
  `https://<host>`, the server's host under the environment's domain, or
  its `run.app` URL without a domain, where its ingress lets a browser
  reach it; a site's is `https://<site>.<domain>`, or without a domain
  `http://` and its load balancer's address. The generic connector, which
  joins deployables on two targets, will read it too;
- `Lower`, a pure function from the environment and the resolved
  deployable, bindings included, to the deployable's resources and, for an
  exposed server, the DNS records it needs (section 6.9).

A resource a platform leaves without a phase gets the default of its
producer: rollout for a server's, a job's or a site's own resources,
infrastructure for a database's. A job platform's `AddressOf` may return
nothing, since no edge reaches a job, and its `Lower` reads the job's run
from the deployable's `Job` (D52). A site platform needs no languages or
dialects; its `Lower` reads what the site builds and serves from the
deployable's `Site`, and writes `ir.SiteDigestToken` where the digest of
the files it serves goes, which the deploy pins (D55).

### 6.2 Connector

A connector realizes one edge kind between two platforms: Cloud Run to
Cloud SQL over sql, Cloud Run to Cloud Run over http. It registers a
`ConnectorSpec` with the edge kind, its `From` and `To` platforms and a
pure `Connect`, which returns the resources the edge needs (an IAM grant, a
Cloud SQL connection on the service) and the value of the derived binding.
One connector serves an edge kind between two platforms; a second is
refused. An http edge between two APIs one server serves runs from the
server to itself, and its connector derives the server's own address.
`From` is a server or a job platform: a job takes its API's edges, so a
target that places jobs registers a connector from its job platform for
each edge its servers take, which may share the server connector's
`Connect`.

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

- a platform for each deployable kind. A kind it names none for is
  refused in its environments: a stack whose APIs declare jobs resolves
  only on a target with a job platform, or with each job placed on one by
  a settings `platform`;
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
core's own `local` provider's (a container, a database, an edge's key
pair, a process and a job), not a Pulumi package's, since its own
provisioner is the only one that applies them.

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

The outputs file, `outputs.json`, is the core's: `stack outputs
<environment> --out <file>` writes one run's (section 11.1), and stdout
without `--out` gets the same bytes. It is a versioned JSON object:

```json
{
  "version": 1,
  "stack": "shop-stack",
  "environment": "Preview",
  "parameters": { "pr": "123" },
  "resources": {
    "shop-api.service": { "url": "https://shop-api-pr-123-3kq7x2-ue.a.run.app" }
  }
}
```

`version` is the format's version, 1 today, and a reader refuses any
other; `stack` and `environment` name the environment the run applied;
`parameters`, present only for a member of a parameterized environment,
are the run's values; and `resources` holds what the provisioner's
`Outputs` read, by node ID and output name, without the secret ones. The
type is `stack.RunOutputs`, with `stack.UnmarshalOutputs` to read one and
`stack.NewOutputs` to make one; `stack.Outputs` returns it from Go.

`extensions/pulumi/bindings` is the generator. It reads each environment's
`environment.json` and the outputs file beside it; its `Outputs`,
`NewOutputs`, `UnmarshalOutputs`, `OutputsVersion` and `OutputsFile`
forward to the core's. A build writes
`<output-root>/stack/<stack>/<environment>/environment.json`, and `stack
outputs <environment> --out` with that directory and `outputs.json` puts
the run's file beside it. It writes two packages:

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

There are six specs, registered like the others in section 3 of
`docs/extension-model.md`. The core registers one target, `local`, with
its platforms, connectors and provisioner (section 8.3, and D30, amended:
the core registers the local target), and one CI renderer, `github`
(D47); every other is an extension's.

- `RegisterPlatform(PlatformSpec)` refuses a malformed or repeated name, an
  unknown deployable kind, a server platform without languages or a
  database platform without dialects (or either with the other's list), an
  unknown or repeated language or dialect, a settings schema that does not
  compile, an identity config that is not a server platform's JSON object,
  and a missing `NameOf`, `AddressOf` or `Lower`.
- `RegisterConnector(ConnectorSpec)` refuses a malformed or repeated name,
  an unknown edge kind, a missing platform or `Connect`, and a second
  connector for one edge kind between the same two platforms.
- `RegisterTarget(TargetSpec)` refuses a malformed or repeated name, an
  unknown deployable kind, a values or resource type schema that does not
  compile, a resource type another target or a DNS platform registered
  with a different schema, and a policy rule without a name or a check, or
  with a repeated name, and a deploy seam it cannot use (section 11.1):
  any but `Secrets` without a provisioner, and `Bootstrap`, `Migrations`,
  `Builder` or `CI` without `State`.
- `RegisterDNSPlatform(DNSPlatformSpec)` refuses a malformed or repeated
  name, the reserved name `manual`, a values or resource type schema that
  does not compile, a resource type a target or another DNS platform
  registered with a different schema, and a missing `Lower`. Its
  `ResourceTypes` are the schemas of the types its `Lower` emits, for a DNS
  platform of another provider than the target's, and its `Credentials`
  name the secrets its provider reads when the provisioner runs (section
  6.9).
- `RegisterProvisioner(ProvisionerSpec)` refuses a malformed or repeated
  name, a missing implementation, and a tool without a name or a version,
  or listed twice. Its `Tools` are the command-line tools it runs, which
  a generated CI job installs (section 11.3).
- `RegisterCIRenderer(CIRendererSpec)` refuses a malformed or repeated
  name, a missing `Render`, and an install directory that is not a
  relative path inside the repository.

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
of each exposed server and of each site, which is always exposed (D55),
and the records its certificate needs for validation. The DNS platform
lowers them to its provider's resources, in the same provisioner run as
the rest of the environment.

A DNS platform registers a `DNSPlatformSpec`: the JSON Schema of an
environment's values for it (a zone) and a pure `Lower` from the records
to resources. It is a spec of its own, not a `PlatformSpec`, because it
lowers records rather than a deployable. The spec also holds:

- `ResourceTypes`, the schemas of the resource types `Lower` emits, which a
  DNS platform of another provider than the target's brings itself. They
  register as a target's do (section 6.4), under the same rule: one schema
  per type, whoever registers it.
- `Credentials`, the secrets the platform's provider reads when the
  provisioner runs, such as an API token. Each names a secret in the
  target's secret store, the environment variable the provider reads it
  from, and what the engineer enters. Resolution writes them into
  `environment.json` under `dns.credentials`, even before any server is
  exposed. The target's bootstrap asks for each and stores it, and a plan,
  apply or destroy reads it and sets the variable for that run of the
  provisioner. No value reaches the resource graph, the rendered program
  or a file.

v1 has two DNS platforms:

- **Cloud DNS**, the gcp target's default. It writes into the managed zone
  in the environment's project that holds the domain.
- **Cloudflare DNS**, `cloudflare` in `extensions/cloudflare`. Its values
  are `zone`, the name of the zone that holds the domain; `zoneId`, the
  zone's identifier; and `proxied`, false by default. It refuses a domain
  or a record outside the zone, and a record type other than A, AAAA,
  CNAME and TXT. Each record is a `cloudflare:index/dnsRecord:DnsRecord`
  of pulumi-cloudflare 6.22.0, whose schema the extension pins, named by
  its full name:
  - records are DNS-only by default, with a TTL of 300 seconds;
  - `proxied: true` proxies each host's A, AAAA and CNAME records, with
    Cloudflare's automatic TTL. It never proxies a TXT record, or a name
    whose first label begins with an underscore, such as a certificate's
    `_acme-challenge` record, which must answer with its own value;
  - TXT content is written as quoted character strings of at most 255
    bytes, as Cloudflare stores it, and a trailing dot is dropped from a
    name and from a literal CNAME target.

  Its one credential is an API token with the DNS Edit permission on the
  zone, which the provider reads from `CLOUDFLARE_API_TOKEN`. Its secret
  is `<stack>-cloudflare-dns-<zone>`, the zone's dots as underscores
  (`shop-cloudflare-dns-acme_dev`). A zone's name holds no underscore, so
  two zones never share a secret, and a stack's environments in one zone
  share the token.

  The zone id is a value rather than looked up by the zone's name. The
  lookup is a function call (`cloudflare:index/getZone:getZone`), which
  the resource graph and the Pulumi YAML renderer cannot express, and it
  would make every plan depend on Cloudflare's API. The id is on the
  zone's Overview page and is not a secret.

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
   the schema a registered target or DNS platform holds for its type, and
   every inherited node is a node of the same type in the parent
   environment, which it resolves for the check.
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

`extensions/gcp` builds this section: the target, its Cloud Run, Cloud
Run job, Cloud SQL and site platforms, their connectors, the Cloud DNS
platform, the policy rules, the pinned provider schemas (section 6.4), at
pulumi-gcp 9.37.1, bootstrap with the target's Secret Manager store and
state bucket (section 7.3), image builds on Cloud Build, the migration
job (section 8.4, D46), a job's run on demand (section 8.7, D52), and a
site's publish to its bucket (section 8.10, D55). Its golden environments
resolve the acme-shop stack of section 4.1, shop-orders' job and the site
shop-web included, in a staging, a production and a parameterized
preview environment.

### 7.1 What the engineer enters

- `project` and `region`, which are required, and `production: true` for
  an environment the production defaults (section 7.5) and policy rules
  (section 7.6) apply to;
- `projectNumber`, which bootstrap records (section 7.3) and a person may
  correct later. The generated CI needs it (section 11.3): Workload
  Identity Federation names its provider by the project's number, which no
  other value gives;
- `domain`, which is optional, and its DNS platform: Cloud DNS by default,
  or Cloudflare with the zone's name (`zone`) and identifier (`zoneId`),
  and an API token that bootstrap asks for (section 6.9). Cloud DNS
  writes into the managed zone that holds the domain. The zone is named
  after the domain with its dots as hyphens unless `zone` names it, and
  lives in the environment's project unless `project` names another;
- secret values, through `stack secrets set`.

Bootstrap reads the GitHub repository from the git remote.

### 7.2 Realization

| Stack concept | gcp |
| --- | --- |
| database | a Cloud SQL Postgres instance with IAM database authentication on, which refuses a connection that does not come through a Cloud SQL connector, and a database per hosted schema; a migration job |
| server | a Cloud Run service with its own service account, which holds the Cloud Trace agent role; the config in environment variables, a derived field as one variable per member of its value; CPU allocated only while an instance handles a request, unless `cpuAlwaysAllocated` keeps it (section 7.5); a startup probe on the entrypoint's `GET /readyz` (section 8.1), every 5 seconds for up to two minutes, so an instance takes traffic once its databases answer, and a liveness probe on `GET /healthz`, every 15 seconds, which restarts an instance after three misses in a row |
| job | a Cloud Run job (`gcp.cloudrunjob`) named after the deployable, with its own service account, which holds the Cloud Trace agent role, and the config, secrets, Cloud SQL volume and VPC egress a server of its API takes; one task, which runs the image to its end, with the job's timeout for each try and the job's retries, at most the 10 Cloud Run allows (D52) |
| schedule | for a job whose environment runs a schedule, a Cloud Scheduler job named as the job is, in the environment's region, on the job's cron in its time zone, which POSTs to the Cloud Run Admin API's `jobs/<job>:run` with an OAuth token for the job's own account; that account holds `roles/run.invoker` on that job alone, which grants it `run.jobs.run`. A job whose schedule is off has neither, and runs only on demand |
| sql edge | `roles/cloudsql.client` and `roles/cloudsql.instanceUser` for the server's or the job's account, held to the edge's instance by an IAM condition; an IAM database user; the Cloud SQL connection, which the connector derives (instance connection name, database, IAM user) and the service or the job mounts |
| http edge | `roles/run.invoker` on the callee for the caller's account; the callee's `run.app` URL in the caller's config, with a Google ID token for the callee's custom audience, its full resource name `//run.googleapis.com/projects/<project>/locations/<region>/services/<service>`, as the service credential, since the service's own callers field cannot reference its URL; every service lists its resource name in `customAudiences`. The callee's callers field gets Google's issuer and keys, and the caller's service account by its email (section 9.2): a job's, as a caller that serves its API |
| internal server | internal-only ingress, with Cloud Run's invoker check on; callers also send the token in `X-Serverless-Authorization`, which the check reads |
| calling server or job | Direct VPC egress for all its traffic through the environment's network: a VPC, a subnet with Private Google Access, and Cloud NAT so the internet stays reachable. A job runs apart from every server, so it reaches each API its API calls this way, one its API's server serves too |
| exposure | a global external Application Load Balancer per exposed server, with a Google-managed certificate from Certificate Manager on a host under the domain, authorized by a DNS record, and the records written by the environment's DNS platform (section 6.9); the service takes traffic from the load balancer only, with the invoker check off. Without a domain, the `run.app` URL, open to all traffic. The server's public address is `https://<server>.<domain>`, or without a domain its `run.app` URL |
| site | a Cloud Storage bucket per site, `<project>-<site>`, in the environment's region, with uniform access and readable by `allUsers`, which a backend bucket needs; a backend bucket over it with Cloud CDN, which keeps each object as its `Cache-Control` says; and a global external Application Load Balancer whose URL map, in the site's rollout step, rewrites every path to the prefix of the files it serves and `/` to their `index.html`, and serves the fallback with 200 for a 404 (section 8.10). With a domain, a certificate for `<site>.<domain>` as an exposed server has, and `https://<site>.<domain>` is the site's origin; without one, HTTP on port 80 at the load balancer's address, which is the origin, since a certificate needs a host |
| site edge | the server's public address in the site's config, and the site's origin in the server's CORS field (section 3.4). No grant: the server is exposed, with its invoker check off |
| secret | a Secret Manager secret named `<Stack>-<Type>-<FIELD>`, an accessor grant to each reading server's or job's account, and an environment variable that references its latest version |
| image | a server's or a job's, built by Cloud Build, pushed to the Artifact Registry repository named after the stack and deployed by digest; the graph holds the image's repository path, and the deploy pins the digest it built |
| parameter | names suffixed with the parameter and its value (`shop-api-pr123`); a database per value (`shop_db_pr123`) on the parent's instance, whose secrets and network the member also inherits |

A caller reaches every callee at its `run.app` URL, exposed or not, from
inside the VPC. Cloud Run counts a request from a VPC as internal, which
an internal server's ingress requires and a server behind a load balancer
accepts, so no caller waits on a load balancer the exposure step applies
last. A call to an API the same server serves stays on loopback, with no
grant and no credential.

Each exposed server gets a load balancer of its own, and so does each
site. A platform lowers one deployable, so it cannot write the host rules
of a load balancer the environment's exposed servers would share; sharing
one waits for a lowering that sees the whole environment. A member of a
parameterized environment gets its own site bucket and load balancer, as
it gets its own exposed servers.

A schedule runs as its job's own account, which may run that job and no
other. An account per stack, with a grant on each job, could run every job
of the stack's environments in the project; an account per schedule would
add an account, whose id must fit 30 characters beside the job's, to do
what the job's account may do already. The job's account holds what the
job's runs reach, so letting it start a run adds no reach. Cloud
Scheduler's service agent mints the token, through the role Google gives
it when the project enables Cloud Scheduler, and `deployer`, which names
the account in the scheduler job, acts as it through
`roles/iam.serviceAccountUser` (section 7.3). Cloud Run starts an
execution each time the schedule fires, whether or not the last has
ended, where `stack dev` skips a run that comes due while the last goes
on (section 8.7): a job whose run may outlast its interval keeps its runs
apart itself.

Every node sets its `project`, so the provisioner needs no provider
configuration, and the network lives in the environment's graph rather
than in bootstrap, since the edges decide whether there is one.

### 7.3 Bootstrap

`superschematic stack bootstrap <environment>` runs once per project with
owner credentials (application default credentials), and is safe to run
again: each step creates what is missing and leaves the rest.

1. It enables the APIs the deploy and the environment's graph use: those
   of the state, the images and their builds (Artifact Registry, Cloud
   Build and Cloud Logging), the accounts and Workload Identity
   Federation, Secret Manager, and Cloud Run, Cloud SQL, Compute Engine,
   Certificate Manager, Cloud DNS and Cloud Scheduler as the graph's
   resource types need them, Cloud Run with any database for its migration
   job, and Cloud Scheduler for a job's schedule (D52). An API
   enabled moments ago can refuse calls as one the project has not
   enabled, so each later step retries such a refusal for up to five
   minutes.
2. It creates the state bucket and the KMS key directly, since Pulumi needs
   them before it can run: the bucket `<project>-superschematic-state`,
   with uniform access, public access prevention and object versioning,
   and the key `pulumi-state` in key ring `superschematic`, in the
   environment's region. Pulumi keeps its state in the bucket, encrypted
   with the key, and the deploy manifests sit beside it (section 11.2).
3. It applies a bootstrap graph through the provisioner, in a Pulumi
   project of its own, `<stack>-bootstrap`, with a Pulumi stack per GCP
   project:
   - an Artifact Registry repository named after the stack, in the
     environment's region;
   - a `deployer` service account and a read-only `planner` one,
     `<stack>-deployer` and `<stack>-planner`, each with its project roles
     and its use of the state bucket and the key. `planner` reads every
     resource and IAM policy a preview refreshes and sees whether a secret
     has a value, without reading one; it writes objects in the bucket,
     since a preview takes the stack's lock. `deployer` also runs Cloud
     Build builds and the migration job, as the next two accounts, and
     reads a failed execution's stderr with the Logs Viewer role. It
     applies a job's schedule with Cloud Scheduler's admin role, the role
     that creates, updates and deletes scheduler jobs, and runs a job's
     executions for `stack run` with the Cloud Run admin role it applies
     the job with (D52). It creates a site's bucket and its grant to
     `allUsers`, and publishes the site's files, with Storage's admin role
     (D55);
   - a `builder` account, `<stack>-builder`, that image builds run as
     (section 11.2): it pushes to the stack's repository, writes its
     logs, and reads the build contexts in the state bucket, under
     `superschematic/builds/`, and nothing else of it;
   - a `migrator` account, `<stack>-migrator`, that the migration job runs
     as (section 8.4): it holds the Cloud SQL client and instance user
     roles, and reads the job documents and plans in the state bucket,
     under `superschematic/migrations/`, and nothing else of it. Its IAM
     database user is a node of the environment's graph, on each Cloud
     SQL instance, since an instance is not there at bootstrap;
   - Workload Identity Federation for the GitHub repository the git remote
     names: a pool, a provider for GitHub Actions' tokens that admits only
     that repository, and the right of both accounts to be used from it.
     Without a GitHub remote it is left out, `--repository` names one,
     and `--repository ""` leaves it out.
4. It creates the secret of each platform credential the environment
   needs (a DNS platform's API token, section 6.9) in Secret Manager,
   where only `deployer` and `planner` can read it, and asks for each
   value it lacks, with the terminal's echo off. Environments may share a
   credential, so its secret is not in the bootstrap graph, whose next
   apply for another environment would delete it, and a credential asked
   for once is not asked for again.
5. It records the project's number in the schema, as `projectNumber` in
   the `gcp` values of the environment that declares `project`, so an
   environment that inherits its project inherits the number too. It
   writes the value when the schema has none, writes it again when it
   differs and says so, and leaves the file alone when it matches. In a
   TypeScript schema it changes only that property's text, through the
   compiler's syntax tree; for a JSON or YAML schema it prints the value to
   add (D47).

The bootstrap graph is checked in as a golden,
`extensions/gcp/testdata/golden/bootstrap/Staging.json`, and validates
against the pinned schemas like any graph. The network a calling server's
Direct VPC egress needs is not in it: it is in the environment's graph,
which lowers it only when an edge needs it (section 7.2).

Environments that share a project share its bootstrap, so they share a
region. The core drives bootstrap through the target's `Bootstrap` seam,
and the gcp target reaches Google Cloud through its `Cloud` interface,
over the client libraries, which a test replaces with a fake.

### 7.4 Database connections

Where the server's language has a Cloud SQL connector (Go, TypeScript),
connections use IAM database authentication through it, so there is no
password. Otherwise the platform generates a password into Secret Manager
and uses the Cloud SQL mount Cloud Run provides. The server's database
field is the same either way (section 3.4).

The connector form is built for Go. The sql connector derives the Cloud
SQL connection (section 7.2), and the generated Go entrypoint dials it with
the Cloud SQL Go connector, `cloud.google.com/go/cloudsqlconn`, under pgx:
IAM database authentication, the instance's public IP, which the instance
admits only through a connector, and a certificate refreshed when a dial
needs it rather than in the background, since Cloud Run throttles an
instance's CPU between requests (section 8.1). A TypeScript server gets no
entrypoint yet. A Rust server's sql edge fails to lower until the derived
value has a password form. An IAM database user starts with no privileges
in its database; the migration job grants them (section 8.4, D46).

### 7.5 Defaults

The target sets defaults that `settings` can override:

- one service account per server and per job;
- deletion protection on production databases (`deletionProtection`);
- a zonal instance unless `highAvailability` is set, on the
  `db-custom-1-3840` tier of the Enterprise edition (`tier`), running
  Postgres 16, the version CI tests against (`version`), with backups on
  and point-in-time recovery in production;
- one CPU, 512 MiB and no minimum instances per server (`cpu`, `memory`,
  `minInstances`, `maxInstances`, `concurrency`), and one CPU and 512 MiB
  per job's task (`cpu`, `memory`);
- a server's CPU allocated only while an instance handles a request
  (`cpuAlwaysAllocated`);
- logs to Cloud Logging, and traces to Cloud Trace through the entrypoint's
  OpenTelemetry setup.

Cloud Run allocates a service's CPU only while it handles a request, and
bills its instances for that time and their starts and stops, unless the
service sets its resources, as every server's does for `cpu` and
`memory`: then only `cpuIdle` keeps that, and without it each instance
keeps its CPU and is billed for its whole life. The Cloud Run platform
sets `cpuIdle` unless the server's settings set `cpuAlwaysAllocated:
true`, for a server that works between requests, in goroutines of its own
or on the warm instances `minInstances` keeps. Cloud Run takes a `cpu`
below 1 only with `cpuIdle`. A job's task always has its CPU (D30,
amended).

### 7.6 Policy rules

- `production-databases-highly-available`: in an environment whose values
  set `production`, every Cloud SQL instance it creates is regional.
- `nothing-public-unless-exposed`: nothing admits the public on behalf of
  anything but an exposed server or a site. It refuses an internal
  server's service that takes outside traffic or turns its invoker check
  off, a load balancer's address or forwarding rule, a grant to `allUsers`
  or `allAuthenticatedUsers`, and an instance that authorizes `0.0.0.0/0`.
  A job is never exposed, so a grant that lets anyone run it is refused
  too. A site is always exposed, so its load balancer and its bucket's
  grant to `allUsers` pass.

## 8. Generated build and runtime

### 8.1 Server entrypoint

A generator per server language writes each server's entrypoint when its
stack builds. The Stack kind's `server` generator
(`internal/generator/servergen`) reads the stack's servers, which no
environment changes (`stack.Servers`), and writes a Go module per Go
server at `<output-root>/server/<stack>/<server>/`, holding `main.go`,
`go.mod` and a Dockerfile (section 8.2), and in the same pass a package
per TypeScript server at the same place, which Bun runs (section 8.6,
D51). A server takes its name in the stack: a declared server's class
name, or the API service a default server serves. Each job of a Go API
gets a module of its own beside them, under its deployable's name
(section 8.7). The output root's `server/<stack>` directory holds only
what the last build wrote. A Rust server gets no entrypoint yet.

`main` reads its whole configuration from the environment, and:

- loads each served API's `EnvConfig` with `LoadEnvConfig`: its
  `@envVars` settings and its derived fields (section 3.4);
- opens one pgx pool per database, shared by every API on it, from the
  database field: a connection string, or a Cloud SQL connector
  configuration, which the pool dials through the Cloud SQL connector,
  logging in as the IAM database user with no password (section 7.4). A
  pool connects when first used, so the server starts while its database
  is not up;
- builds one Go SDK client per API called, shared by every API that calls
  it, from the callee's `ServiceEndpoint`. The client sends the service
  credential the endpoint names, from the Go HTTP runtime's sources
  (section 9.6), and forwards the end user of the request on each call's
  context (section 9.4);
- calls each implementation's constructor with its `Deps`, and its
  `AuthMiddleware` and `PayloadDecryptor` where the API's generated
  `Config` takes them (section 8.5);
- registers each API's routes on a router of its own, and hands each
  request to the API whose router registers its method and path. The
  build refuses two served APIs that register one method and path, path
  parameters matched whatever their names. The routes every API
  registers, its index, OpenAPI document and docs, answer for the first
  API by name;
- answers `GET /healthz` while the process runs, and `GET /readyz` while
  every database answers a ping and the server is not shutting down;
- listens on `$PORT`, 8080 when unset, logs JSON through zap, and on
  SIGTERM or SIGINT stops taking requests and gives those in flight 10
  seconds to finish.

An API whose operations have a service clause also takes a service
authenticator, a `serviceauth.Verifier` over the API's callers field
(section 9.2), which `serviceAuthenticator` in `serviceauth.go`, beside
`main.go`, builds with `stackconfig.LoadCallers`. The server refuses to
start without the field, and where no other server calls the API it starts
with no issuers and refuses every service credential.

An API whose server authenticates with the identity runtime (D50) takes
the identity service in place of an auth middleware. The entrypoint builds
one identity store per database such an API reads, over the database's
pool (`database/sql` through `stdlib.OpenDBFromPool`, the Postgres
dialect, the descriptor constant of the database's Go types), and each
API's service with its generated `NewIdentity`, from the config
`identityConfig` in `identity.go` reads from the API's identity config
field: JSON, the runtime's defaults when unset, and a refused config stops
the server. The implementation writes no auth middleware. A preflight
goes to the router that registers the method it asks about, whose CORS
middleware answers the trusted origins. OpenTelemetry export
is not set up: the runtime records spans through the global tracer, and an
exporter would add the OTLP client's dependencies to every server.

The engineer writes the implementation of each served API, and nothing else
(section 8.5). The entrypoint calls each implementation's constructor with
its `Deps`. A mismatch between the code and the generated signature fails
to compile at level 2 of section 10.

`go.mod` requires each generated module, runtime module and
implementation module the server builds from, and a replace points each
at its directory. superschematic writes no `go.sum`: the build runs with
`-mod=mod`, which fills it. `go mod tidy` would also resolve the imports
of the tests of the implementation's module, such as an SDK a test calls
its API through, which the server's `go.mod` does not replace.

Only a server that some environment places on Cloud SQL links the Cloud
SQL connector, whose Google modules (auth, the Admin API client, gRPC)
no other server should carry. The servers do not depend on an
environment, but the build knows the stack's environments: the `server`
generator resolves each, as `stack` does, and a server whose database
some environment's sql edge connects with a `cloudSql` value gets
`cloudsql.go` beside `main.go`, and its `go.mod` requires
`cloud.google.com/go/cloudsqlconn`. Its `connect` hands a Cloud SQL
configuration to `connectCloudSQL`, in `cloudsql.go`, and a connection
string to pgx as before, so one binary runs locally and on gcp. It builds
one dialer, which reads the application default credentials, when its
first Cloud SQL database connects, and its pools still connect on first
use, so `/readyz` reports a database the connector cannot reach. Any
other server refuses a Cloud SQL configuration at startup and says to
build the stack again.

### 8.2 Container image

A generated Dockerfile per server, and per job (section 8.7), builds the
entrypoint and the implementations together. Its build context is the
repository root, the parent of the schemas root, after the stack's
services are built, unless the naming file's `[paths] build_context`
names a directory above it, as `examples/acme-shop` does to reach the
runtime modules of its checkout:

```sh
docker build -f schemas/dist/server/shop-stack/Storefront/Dockerfile .
```

`Dockerfile.dockerignore` beside it cuts the context down to the
directories the build reads: the server's module, the generated modules,
the runtime modules and the implementations' modules that the server's
`go.mod` replaces with a directory, and the superscalar checkout when the
image builds from one. A server whose modules lie outside the build
context gets no Dockerfile, and the build says why.

The generated Go code links superscalar's static archive through cgo (D3),
so the binary cannot be a `CGO_ENABLED=0` build, and a server whose
database declares a version graph links the version graph's archive too.
No Go module the module proxy serves carries either, so they come from
one of two places (D47, amended):

- **A checkout.** With the naming file's `[paths] scalar_go` naming a
  superscalar checkout, a Rust stage builds superscalar's archive for the
  image's platform from it, with `tools.env`'s Rust release, the one the
  host's archives are built with, and a second stage builds the version
  graph's from the crate beside `[paths] versiongraph_go`. A Go stage puts
  each archive where its binding's cgo flags look.
- **The release.** Without `[paths] scalar_go`, as in a project that
  takes the runtime modules from the module proxy, the image takes the
  archives the release of superschematic that wrote the Dockerfile ships:
  `superschematic-archives_<version>_<platform>.tar.gz` beside the CLI on
  the release page, both archives built with one Rust release under
  `lib/`. The release builds them before the CLIs and links each CLI with
  every platform's digest (`internal/release`), so the Dockerfile pins the
  digest for `linux/amd64` and `linux/arm64`. The Go stage downloads the
  tarball for its platform from `SUPERSCHEMATIC_RELEASE`, a build argument
  whose default is the release's page, checks it, and points
  `CGO_LDFLAGS` at it. The server's `go.mod` replaces each runtime module
  no `[paths]` key names, every version of it, with the module at the
  release: superschematic's at the release's tag and superscalar's Go
  binding at the version the release links, since the runtime modules
  require one another at versions only a checkout's replace resolves.
  Every generated Go module's `go.mod` pins the ones it reaches the same
  way, so each also builds on its own. A binary built from a checkout is
  no release, and one the release workflow did not build names no
  digests: neither writes a Dockerfile without `[paths] scalar_go`, and
  the build says why.

Either way the Go stage, on the Go release `tools.env` pins, builds the
server with `-mod=mod`, and the binary runs on distroless `cc`, which
holds the glibc and libgcc the archives need and nothing else, as a
non-root user.

A TypeScript server's Dockerfile builds superscalar's Node addon and
installs the Bun workspace, frozen to the lockfile the project commits
(section 8.6). A Rust server gets none yet.

Not taken: a `CGO_ENABLED=0` binary on a static base, which no build of
the scalar library allows; the archives superscalar's own release
pipeline publishes, which it builds with its own Rust release and for
musl, while a server links them beside the version graph's archive, which
must come from the same Rust release; and building the archives from
source in every image without a checkout, which the release already
does once.

### 8.3 Local stack

`superschematic stack dev [<stack-service-dir>] [--environment <name>]`
runs an environment on the `local` target (`internal/stack/local`):

1. It builds the stack service and every service the stack reaches, each
   with its dependencies, the stack last.
2. It reads the environment the build resolved: `--environment`, or the
   stack's one environment on the local target.
3. It applies the deploy order (section 5.3) through the local provisioner,
   then stays in the foreground until Ctrl-C or until a server exits,
   running each job on its schedule meanwhile (section 8.7). The summary it
   prints names each server's and each site's URL and when each job runs.
4. It stops the servers, callers first, then the container, which keeps
   its data for the next run. `--remove-database` removes the container
   and its data instead.

| Stack concept | local |
| --- | --- |
| database | one Postgres container per environment, `superschematic-<stack>-<environment>-postgres`, from `postgres:16-alpine` unless the `postgresImage` value names another; it publishes its port on 127.0.0.1 only and trusts every connection. A database per hosted DB schema, named after it in snake case (`shop_db`) |
| migration | each run plans with `sqlmigrate` from the model the database recorded (`superschematic-migrate status --model`) to the schema's model, and applies the plan with `superschematic-migrate`, expand and contract back to back, since no server of the previous version runs. The runner is on `PATH`, or where `SUPERSCHEMATIC_MIGRATE` says |
| server | a Go process built with `go build` (with `-mod=mod`) from its entrypoint module at `<output-root>/server/<stack>/<server>` (section 8.1), or a TypeScript one, `bun main.ts` in its entrypoint package at the same place, after one `bun install` at the output root, the root of the Bun workspace (section 8.6). Its environment is its bindings, a derived field as one variable per member (section 3.4), and `PORT`, with nothing of the shell's but `PATH`, `HOME` and a few like them. It is ready once it answers `/readyz`, and each of its lines is printed with its name in front |
| job | a Go process built as a server is, from its entrypoint module at `<output-root>/server/<stack>/<job>`, with a server's environment and no `PORT`. It runs on its schedule, in its time zone, while `stack dev` waits, and once with `superschematic stack run <environment> <job>`, each line of a run's output with its name in front. `jobs/<job>.lock` in the environment's state directory keeps a schedule's runs and `stack run`'s apart (section 8.7) |
| site | built once, `bun run <build>` in its package at the naming file's `[implementation_paths] site` template after the workspace's install, then served from a file server of the provisioner's own on `http://127.0.0.1:<port>`: each file of its output, its config at `/__superschematic/config.json` with each API it calls at its loopback URL, and its fallback for a path that names no file. HTML files and the config are served `no-cache` and `no-store`, so a reload sees a rebuilt site (D55) |
| sql edge | `postgres://postgres@127.0.0.1:<port>/<database>?sslmode=disable` |
| http edge | the callee's `http://127.0.0.1:<port>`, with a `signed-token` credential (D37): `iss` and `sub` the caller's deployable, `aud` the callee's, signed with an Ed25519 key pair per calling and called server. A call between two APIs one server serves stays on loopback with no credential |
| site edge | the server's public address, its loopback URL, which is also its address. The server's CORS field lists the site's origin, `http://127.0.0.1:<port>` (D55) |
| secret | a line `<Type>.<FIELD>=<value>` in `<schemas-root>/.superschematic/local/<stack>/<environment>/secrets.env` |
| port | a server's or a site's `port` setting and the `postgresPort` value, else a hash of the stack, the environment and the deployable: 20000 to 22767 for a server or a site and 30000 to 32767 for Postgres, the same from run to run |

`<schemas-root>/.superschematic` holds what belongs to one machine: each
local environment's secrets file and the key pairs of its edges. It
ignores itself in git, and nothing in it reaches the output root.
`environment.json` and the rendered program name a secret by its ID and a
private key by a reference to its key pair node's `privateJwk` output,
which the provisioner reads when it starts the caller.

The provisioner renders `local.json` into
`<output-root>/program/<stack>/<environment>`: the containers, databases,
migrations, servers, jobs and sites it runs. Beside it are the models `stack
dev` writes for it (`models/<service>.json`), the plans it applies
(`migrations/<service>.plan.json`) and the Go servers' and jobs' binaries
it builds (`bin/`). A TypeScript server has no binary: the provisioner
runs `bun install` once at the output root, whatever the number of
TypeScript servers and waves, then starts each with `bun main.ts`. The
install is the one an engineer runs, not frozen: it writes the
workspace's lockfile, or brings the committed one up to date with the
packages the build wrote, so the lockfile follows the schemas through
`stack dev` (section 8.6). That install also links the implementations,
which lie outside the output root, to it, so a `stack dev --out`
elsewhere leaves them linked to that output root until the next install
in the usual one. Each server runs in a process group of its own, so
Ctrl-C reaches `stack dev` first, which sends each server SIGTERM,
callers first, and SIGKILL ten seconds later.

Policy rules refuse what a local environment cannot hold: a domain
(`local-no-domain`), parameters (`local-no-parameters`), and two listeners
on one port (`local-distinct-ports`). The platform runs Go and TypeScript
servers; a Rust server, which has no entrypoint yet, does not resolve
(`unrealizable`).

Not built:

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

The apply step on GCP is built (D46). Each phase a deploy runs on a
database is one execution of the stack's Cloud Run job, `<stack>-migrate`,
through the target's `Migrations` seam:

1. The runner writes each plan with steps in the phase, and a job document
   that names them, into the state bucket, under
   `superschematic/migrations/<stack>/<run>/`. The document names the
   Cloud SQL instance, the database of each DB service on it, the plan and
   the IAM database users of the servers that connect.
2. It creates or updates the job to run the runner's image as the
   `migrator` account (section 7.3), with one task, no retry and an hour to
   finish, and runs it once with the document's `gs://` URL as its
   argument, `superschematic-migrate job --job <url>`. It waits for the
   execution, and a failed one fails the step with the execution's name,
   its logs' URL and the runner's error. Cloud Run says only that the
   container exited with an error, so the deploy reads what the task wrote
   to stderr from Cloud Logging, for up to 30 seconds while none has
   arrived, and reports the runner's error, the last line that begins
   `superschematic-migrate: `, else the first line, such as a panic's.
   With no line, or none it can read, it reports Cloud Run's message and
   says why. An error while waiting is not a failed execution: the step
   fails saying the execution may still be running, and the next deploy
   runs the phase again, which the runner resumes.
3. The job reads the document and the plans through Cloud Storage's API,
   and reaches each database through the Cloud SQL Go connector, with IAM
   database authentication as the migrator's IAM database user, so there
   is no password. The Cloud SQL platform gives each instance that user,
   with the `cloudsqlsuperuser` role, which owns the databases the
   platform creates, so the migrator creates the tables and owns them.

The job owns the database's privileges. After each phase's steps, it gives
every server that connects to the DB service, by its IAM database user,
USAGE on the schemas that hold the migrator's objects, SELECT, INSERT,
UPDATE and DELETE on its tables, SELECT on its views and USAGE and SELECT
on its sequences, leaving out the runner's state tables, and takes every
such privilege back from a user it gave them to that no longer connects,
all in one transaction. It takes nothing from a role the migrator is
granted: Cloud SQL gives `cloudsqlsuperuser` CREATE on the public schema
itself, and the first live deploy's job reported taking that back. The grant is table-level DML, not what each API
reads. The deploy manifest records the servers each DB service's job saw
connect, and a deploy runs the expand phase of a DB service whose servers
changed even when its plan has no steps, before the new server rolls out
and before a removed server's user goes. Since Postgres drops no role that
holds privileges, an IAM database user's node abandons the user when it
goes.

The runner's image is built once per release, with Cloud Build, from a
generated Dockerfile that installs the runner with `go install` from its
module at the release's tag, which the Go checksum database verifies,
onto distroless static. `runtime/migrate/go` carries no `replace`
directive, so `go install` takes it. The release is the one the binary is
part of; a binary built from a checkout names none, and
`SUPERSCHEMATIC_MIGRATE_IMAGE` names an image of the runner by digest in
its place, which `scripts/migrate-dev-image.sh` builds from the checkout. The local target runs the runner on the host (section 8.3).

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
  not-implemented error, which answers 501. For an API whose generated
  `Config` takes them, it also writes `AuthMiddleware(deps)`, which
  refuses every request with 401 until it verifies the end user, and
  `PayloadDecryptor(deps)`, which refuses every encrypted payload. For an
  API with jobs it writes `NewJobs`, whose value has a method per job that
  returns the not-implemented error (D52). It never writes into a
  directory that holds a Go file, so the package is the engineer's from
  then on: a stack's build fails on an API whose package declares no
  `NewJobs` while the API declares jobs, an implementation that predates
  them, and says what to add. A stack's build scaffolds each API its servers
  serve; `build --scaffold` and `build-all --scaffold` scaffold each Go
  API built, outside a stack. A service the cache would restore builds
  again when its implementation is missing, and so does a stack that
  serves it, since the cache stores outputs, not the scaffold.
- **Module.** The entrypoint imports the package from the module of the
  nearest `go.mod` at the package or above it, up to the repository root,
  and reads that module's path on each build. When none holds a package
  the stack's build scaffolds, it writes a `go.mod` beside the scaffold,
  module `<go_module_root>/implementation/<service>`, which the engineer
  may rename. An existing package that no module holds fails the build.
- **Signature.** The API generator writes `Deps` and the constructor's
  signature in `deps.go`: `type Constructor func(deps Deps)
  (Implementations, error)`, which the scaffold asserts with `var _
  api.Constructor = New`. TypeScript gets the equivalent with its
  entrypoint (section 8.6), and Rust later (section 12). `Deps` is typed and filled by the entrypoint:

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

  For an API with jobs, `deps.go` also declares `Jobs`, a method per job
  sorted by name, and `JobsConstructor`, which the scaffold asserts with
  `var _ api.JobsConstructor = NewJobs` (D52). A job is built from the
  same `Deps` as the API:

  ```go
  type Jobs interface {
      // ShipOrders runs the job ShipOrders, on */15 * * * * unless an
      // environment changes its schedule.
      ShipOrders(ctx context.Context) error
  }

  type JobsConstructor func(deps Deps) (Jobs, error)
  ```

Outside a stack the scaffold stays opt-in. Nothing imports the package
but a server's generated `main` (section 8.1), and a build of a tree whose
Go code lives elsewhere would gain a stub package beside it.

A server that serves several APIs calls each one's constructor with that
API's `Deps`, built from the server's shared connections and clients.

Not taken:

- A package path named on each server, such as `@server({ go:
  "example.com/acme/orders" })`. That is a string per server, and default
  servers would still need a convention.
- A `main` the engineer writes, calling a generated `Run(impl)`. That brings
  hand wiring back, and the server still has to name its main package.

### 8.6 TypeScript servers

A TypeScript server runs on Bun, which runs the generated packages' `.ts`
as they are, with no build step (D51). The pieces mirror Go's:

- **Derived config.** The API package's `config.ts` declares `EnvConfig`
  and `loadEnvConfig()`. `EnvConfig` extends the types package's
  `Loaded<Type>` of the `@envVars` settings with the fields of section 3.4
  under their names: a `Database` per sql edge, a `Service` per `calls`
  entry, and, unlike Go's, the API's callers field, a `ServiceAuthConfig`,
  when an operation has a service clause. `loadEnvConfig()` reads the
  settings with the types package's loader and the rest with the HTTP
  runtime's `loadDatabase`, `loadService` and `loadCallers`, which read the
  variables Go's `stackconfig` reads, and throws a `StackConfigError`
  naming every variable at fault. Vectors shared by both runtimes
  (`runtime/http/testdata/stackconfig_parity.json`) hold them to one
  encoding.
- **Implementation.** `[implementation_paths] typescript` defaults to
  `typescript/{service}`. The API generator writes `Deps` and
  `Constructor`, `(deps: Deps) => Implementations |
  Promise<Implementations>`, in `deps.ts`, and, for an API with a route
  that needs an end user, `AuthenticatorFactory`, the type of the
  implementation's `authenticate`, which builds the router's
  `Authenticator` from `Deps` as Go's `AuthMiddleware(deps)` does. `Deps`
  holds `config`, `db`, a client per callee in a member named after it
  (`shopApi`), and `logger`, each left out by Go's rule. The scaffold
  writes the package once, as Go's does (section 8.5), in a directory
  that holds no `.ts` file: `package.json`
  (`<npm_scope>/<service>-implementation`), `tsconfig.json`, and an
  `index.ts` whose `create` is a `Constructor` and whose methods throw the
  runtime's `notImplemented()`, with an `authenticate` that establishes no
  end user and a verifier per `@hmacVerified` provider that refuses every
  request. `deps.db` is a `pg` Pool that the runtime's `connectPostgres`
  (`@superschematic/http-runtime/postgres`) opens from the derived
  connection; a TypeScript ORM, when one exists, adds a typed client on
  the same pool. The Node Cloud SQL connector reads the instance's
  settings from the Admin API when the pool opens, where Go's dialer waits
  for the first dial; both pools connect to the database on first use.
- **Packages.** One Bun workspace spans the generated TypeScript packages,
  the servers and the implementations, so every import resolves with
  `workspace:*` or by name. Its root is the output root's `package.json`,
  which the generator owns, not the repository root's, which is the
  project's own: its members are `types/typescript/*`, `sdk/typescript/*`,
  `api/*` and `server/*/*`, and the implementations through a `../`
  pattern from the implementation template, which Bun 1.4 accepts. Where
  `[paths]` names a checkout of superscalar, the HTTP runtime or the
  version-graph runtime, the root's `overrides` point every dependency on
  it at the checkout with a `file:` path from the root; a member's own
  `file:` path is read wrongly by Bun once members sit at different
  depths. The root depends on superscalar itself, which the runtime and
  the API packages import without naming. `bun install` runs in the output
  root, or in a generated package under it; an install inside an
  implementation does not find the root, and the lockfile lives in the
  output root. A `file:` path or pattern climbs from the output root's
  physical path, the one Bun runs in, so an output root under a symbolic
  link, such as a temporary directory on macOS, resolves too.
- **Lockfile.** The install writes `bun.lock` beside the root, and the
  project commits it (D51, amended), so every install of one commit takes
  the same versions: the image's, the generated CI's and an engineer's.
  No build removes it, and the root it pairs with is the same bytes on
  every build of the same schemas. The members' manifests are the build's
  and the implementations' are the project's, so nothing else is
  committed for it. A frozen install needs every member the lockfile
  names that another member depends on, so the lockfile to commit is the
  one an install writes after `build-all`: an install after building only
  some services, as `stack dev`'s first in a fresh clone of a project
  whose stack does not reach them all, drops the others' packages from it.
  Its `file:` paths, like the root's overrides, are relative to the output
  root, so it is the same on every machine with the same layout, and a
  change to the manifest of a checkout `[paths]` names changes it too. An
  output root is usually ignored whole (`dist/`), and git cannot take back
  a file under an ignored directory, so the rule ignores the output
  root's contents and takes the lockfile back:

  ```gitignore
  schemas/dist/*
  !schemas/dist/bun.lock
  ```

  Where a broader rule ignores the directory, such as a repository's
  `dist/`, the schemas root's `.gitignore` takes it back first (`!/dist/`,
  `/dist/*`, `!/dist/bun.lock`), as acme-shop's does. The ignore rules are
  the project's: the build of a stack with a TypeScript server, in a
  repository whose rules ignore the lockfile, prints one line naming the
  rule and the fix, and changes no ignore file. `stack init` (section
  11.1), when it is built, writes the rule.
- **Entrypoint.** The `server` generator writes
  `<output-root>/server/<stack>/<server>/` for a TypeScript server in the
  pass that writes the Go ones (section 8.1): `package.json`
  (`<npm_scope>/<stack>-<server>-server`, a workspace member that depends
  with `workspace:*` on each served API package, each implementation by
  the name its `package.json` gives it, each callee's SDK and the types
  package of each database an identity store reads, and on the runtime,
  Hono and, with a database, `pg`), `tsconfig.json` and
  `main.ts`. `main.ts` does what Go's `main` does:
  - reads `$PORT`, 8080 when unset, and logs JSON lines through the HTTP
    runtime's `createLogger`, bound to the stack and the server;
  - loads each API's config with `loadEnvConfig()`, and opens one pool per
    database with `connectPostgres`, shared by every API on it. Only a
    server some environment places on Cloud SQL depends on the Cloud SQL
    Node connector; any other refuses a Cloud SQL configuration at
    startup and says to build the stack again, as Go's does;
  - builds one SDK client per API called, with the endpoint's URL and
    `serviceCredentialFor` its credential, and calls each implementation's
    `create(deps)`, and its `authenticate(deps)` where a route needs an end
    user. An API whose server authenticates with the identity runtime
    (D50) has no `authenticate`: `main.ts` builds one identity store per
    database such an API reads, `postgresIdentityStore` over its pool and
    the `identityDescriptor` the database's TypeScript types export, and
    the API's service with its generated `identityService()`, from the
    config `identityConfig` reads from the API's identity config field
    with `parseIdentityConfigJSON`, the runtime's defaults when unset; a
    refused config stops the server. The router takes it as `identity`.
    The end user travels per call, `{ forward: ctx }` (D37), so no handler
    captures it as Go's `CaptureAuthorization` does;
  - mounts each API's `buildRouter` on one Hono app, with
    `authenticateService: serviceAuthenticator(config.<API>_CALLERS)` for
    an API with a service clause, then the runtime's `notFoundHandler` and
    `errorHandler`. Hono merges the routers' routes, so an identity API's
    router registers its CORS on each of its own routes: a request gets
    the CORS of the API that serves it alone, and a preflight that of the
    API that registers the method it asks for, as Go's dispatch does.
    The build refuses two served APIs that register one method and path,
    a manually routed operation included, which a TypeScript router
    mounts;
  - serves `/healthz`, and `/readyz`, which answers 503 `draining` during
    shutdown and 503 `unavailable` with each database whose ping fails
    within two seconds, through `Bun.serve`, which it hands Hono as the
    routes' bindings, so the runtime reads the peer's address from it;
  - on SIGTERM or SIGINT stops taking requests, gives those in flight ten
    seconds, ends the pools and exits; a second signal exits at once.

  Bun runs `main.ts` as it is. A mismatch between the implementation and
  `Deps` shows when `tsc` checks the package, as the generated CI's
  `check` does (section 11.3), not when Bun runs it.
- **Image.** With the naming file's `[paths]` naming the checkouts of
  superscalar's TypeScript binding and the HTTP runtime's package, which
  no registry serves yet, the Dockerfile's addon stage builds
  superscalar's Node addon with `cargo build -p superscalar-napi` for the
  image's platform, on `rust` at the release `tools.env` pins, against the
  glibc of the Debian release the Bun image runs on. The build stage, on
  `oven/bun` at `tools.env`'s Bun release, puts the addon in the binding's
  `native/`, builds the binding's and the runtime's `dist/`, which the
  root's overrides copy, and installs the workspace without development
  packages, frozen to the committed lockfile, which fails when the
  lockfile no longer matches the packages the build wrote. A context
  without the lockfile, as in a project that has not committed it,
  resolves afresh and prints that it does, so two images of one commit
  may differ. The image keeps that fallback rather than refusing: the
  generated CI's `check` refuses a missing or stale lockfile, and every
  deploy job waits for it (section 11.3). The image copies the output root and the
  implementations it runs from that stage and runs `bun main.ts` as the
  non-root `bun` user, with `PORT=8080`. `Dockerfile.dockerignore` takes
  in the workspace's root and lockfile, every member's manifest by
  pattern, so the context does not depend on which services built before
  the stack, every types package, each package the server depends on
  whole, the two checkouts without their build output, and the
  superscalar crates. Without either `[paths]` key no Dockerfile is
  written, and the build says why. The deploy builds it as it builds Go's,
  from the Dockerfile at the server's path.

`stack dev` runs a TypeScript server on Bun beside the Go servers (section
8.3). acme-shop's storefront is the proof: its implementation is the
workspace package `typescript/shop-storefront`, whose `create` keeps carts
in memory and whose `authenticate` knows its callers by static tokens,
built from `Deps`; `shop-stack` deploys and exposes it; and
`TestStackDevRunsTheShop` waits for its `/readyz` and reads a cart through
its API. The example's other TypeScript, its clients and type tests, is a
second member, `typescript/clients`, so nothing in the example links a
package by hand.

### 8.7 Jobs

A job is a run to completion that an API service declares, with `@job` on
a class of its schema (D52):

```ts
// The warehouse's pick run: ships each placed order.
@job({ schedule: "*/15 * * * *", timeZone: "UTC", timeout: "5m", retries: 1 })
export abstract class ShipOrders {}
```

- **Declaration.** `@job` comes from `@superschematic/api` and goes on a
  class of an API schema that holds no fields and extends nothing. Every
  argument is optional: `schedule`, a five-field cron (minute, hour, day of
  the month, month, day of the week; no descriptor such as `@hourly`, and
  no time zone prefix); `timeZone`, an IANA name, UTC unless set;
  `timeout`, a duration of whole seconds as Go writes one (`90s`, `10m`,
  `1h30m`), ten minutes unless set, Cloud Run's default for a task; and
  `retries`, zero or more, none unless set. The class is no type: the IR
  records it in `Schema.Jobs` (`ir.Job`), with its name and comment, and
  the data forms write it under `jobs:`. No type, operation set or other
  job of the schema takes its name, nor another job its Go method.
- **Code.** The API's implementation package implements it, with the same
  `Deps` as the API: the API generator writes a `Jobs` interface, a method
  per job taking a context and returning an error, and the scaffold writes
  `NewJobs` with a method that returns the not-implemented error (section
  8.5).
- **Deployable.** Each job of an API in the stack is a deployable of kind
  `job`, named after its API service and its class in kebab case
  (`shop-orders-ship-orders`, `ir.JobDeployableName`). Not after the
  server that serves the API: grouping APIs into an `@server` leaves a
  job's name, and every cloud resource named after it, as they were, and
  two APIs of one server may each declare a job of one name. No class
  declares a job's deployable, so a name another deployable takes is
  refused. The name is lower case, digits and hyphens, as a Cloud Run
  job's and a service account's are; a target refuses one longer than its
  resources take, as it does a server's.
- **Edges and config.** A job's edges are its API's (section 3.3), so it
  connects to the same database and calls the same services, through the
  connectors from its platform, which a target registers beside its
  server's. Its config fields are its API's `@envVars` fields and the
  fields its edges derive, and it takes the `env` its API's server is
  given, under its own; a key the server takes for another API it serves
  does not reach it. It reads the API's secrets, and has no callers field
  and no identity config field, since it serves no request: its entrypoint
  builds no identity service (D50), and its `Deps` reach the user model's
  tables through the ORM. It rolls out after its callees, as a server
  does (section 5.3).
- **Identity.** A job serves its API in a callee's callers field. A
  callee's `from: [ShopOrders]` therefore admits ShopOrders' server and
  its jobs alike, and `Caller.Deployable` tells them apart. Resolution's
  check that each edge reaches an operation its caller may invoke (section
  9.3) covers the API's calls through its server; a job takes the edges
  whether or not it makes the calls, so its own are not checked. It
  forwards no end user, so it reaches what admits its API's identity or
  anyone.
- **Entrypoint.** The `server` generator writes a Go module per job at
  `<output-root>/server/<stack>/<job>/`, beside the servers' and from the
  same templates, so the build's pass that removes what it no longer
  writes covers it: `main.go`, `go.mod`, `cloudsql.go` where some
  environment places the API's database on Cloud SQL, and a Dockerfile
  with its ignore file. `main` builds `Deps` as a server's does (section
  8.1): the API's `EnvConfig`, a pool per database and a client per API
  called, which sends the job's service credential and forwards no end
  user. It calls the implementation's `NewJobs`, then the job's method
  once, with a context SIGTERM and SIGINT cancel. It logs `job started`,
  then `job done` or `job failed` with how long the run took, through
  zap, and exits 1 when the method returns an error, so the platform
  records the run as failed and runs it again if its retries allow. It
  serves no port and answers no health check. A package that predates its
  jobs, with no `NewJobs`, fails the build with the signature to add
  (section 8.5).
- **Image.** A job's Dockerfile is a server's (section 8.2), with the
  binary at `/job`. `stack build` builds it, `--image` and the deploy
  manifest pin it, and the deploy rolls it out, as they do a server's
  (section 11.2); the target's `ImageBuilder` takes the job's name in
  `BuildRequest.Deployable`.
- **Schedule.** The decorator's schedule, a five-field cron in its time
  zone (UTC unless set), is the default. An environment's settings change
  it or turn it off: `{ of: ShopOrders, job: "ShipOrders", schedule,
  timeZone, enabled }` (section 4.1). A member of a parameterized
  environment runs no schedule unless its settings turn one on, with
  `enabled: true`. A job with no schedule runs only on demand, and turning
  on a schedule a job does not have is refused. Resolution records what
  runs on the job's deployable (`ir.ResolvedJob`): its API and class, the
  schedule it runs on in the environment, empty for none, the time zone,
  the timeout in seconds and the retries.
- **Running locally.** The local target lowers a job to a
  `local:process/job:Job` node, and `stack dev` builds its binary in its
  rollout wave, as it builds a server's, then runs each schedule beside
  the servers until Ctrl-C (section 8.3): never two runs of one job at
  once, a run that comes due while the last goes on skipped, each line of
  its output with the job's name in front, a run stopped at the job's
  timeout, SIGTERM first, and run again up to its retries when it fails. A
  run's end is logged, success or failure, and never stops the
  environment.
- **On demand.** `superschematic stack run <environment> <job>` runs a job
  once, waits for it, and exits non-zero when its last try fails. On the
  local target it runs against the environment `stack dev` runs, from
  another terminal: it builds no schema, reads the environment the last
  build resolved and the program `stack dev` rendered, so the run has the
  environment's values, secrets and keys, and builds the job's binary
  again, so a change to the job's implementation is in the run. It refuses
  an environment whose container or servers do not answer. A lock file per
  job in the environment's state directory keeps its runs apart: the
  schedule skips a run while `stack run`'s goes on, and `stack run` refuses
  to start while the schedule's does. On a cloud target it runs the
  deployed job, with the image the deploy manifest records, through the
  target's `Jobs` seam (section 11.1), and refuses a target without one
  and a run whose last deploy did not roll the job out.
- **gcp.** The job platform is `gcp.cloudrunjob` (section 7.2), with
  connectors from it to Cloud SQL and to Cloud Run that share the server's
  `Connect`.
  - A job is a Cloud Run job named after the deployable, as a server's
    service is, with a service account of its own by the same name, the
    API's secrets, Cloud SQL volume and config, and Direct VPC egress when
    its API calls another. It has one task, the decorator's timeout for
    each try and its retries, which Cloud Run caps at 10, so a job with
    more fails to lower; its settings are the task's `cpu` and `memory`.
    The deploy pins its image as a server's. Its account's id is its name,
    which GCP holds to 30 characters, the value of each parameter
    included: `shop-orders-ship-orders-pr` leaves four for a pull
    request's number.
  - An enabled schedule is a Cloud Scheduler job, named as the job is,
    that POSTs to the Cloud Run Admin API's `jobs/<job>:run` with an OAuth
    token for the job's own account, which holds `roles/run.invoker` on
    that job alone and so may run only it. A schedule that is off leaves
    the Cloud Run job, which runs on demand, and neither the scheduler job
    nor the grant.
  - `stack run` runs an execution of the job as the last deploy left it,
    with no overrides, after checking that it runs the image the deploy
    manifest records, and refuses one that runs another, as during a
    deploy. It waits for the execution's end, retries included, and on a
    failure reports the last try's error: the error of its `job failed`
    line, or its panic, which it reads from Cloud Logging as the migration
    job's error is read (D46), with Cloud Run's account of the try and the
    URL of the logs.
  - Bootstrap enables Cloud Scheduler for an environment that runs a
    schedule, and gives `deployer` Cloud Scheduler's admin role (section
    7.3). The migration job gives a job's IAM database user its privileges
    as it gives a server's (section 8.4).
  - The Cloud Run job resource can also start an execution when it is
    created or updated (`runExecutionToken`, `startExecutionToken` in the
    pinned schema), which a job that runs on every deploy could use.

Workers, which run until stopped, come with queues. A job that runs on
every deploy is not built.

### 8.8 Queues and workers

A queue is data, so it lives in a database (D53). A DB service's schema
declares one with `@queue` on a message class. The database the stack
places that service on is its backing, in the service's dialect: Postgres
on the local container or Cloud SQL, SQLite, and D1 when a target offers
it.

```ts
// shop-db (DB service)
@queue({ retries: 5, backoff: "30s" })
export class OrderPlaced { orderId!: string }

// shop-orders (API service)
@worker({ queue: OrderPlaced, concurrency: 4 })
export abstract class FulfilOrders {}
```

- **Storage.** sqlgen writes the queue's table, and the migration plan
  (D27) carries it like any table. Each message has:
  - its fields, as the message class declares them;
  - a state: ready, claimed, done or dead;
  - an attempt count, a time it is next due, and a claim's expiry.
- **Enqueue.** The DB's ORM gains a typed `Enqueue` per queue, which takes
  the transaction the caller writes in. A message commits with the writes
  that caused it, or not at all.
- **Claim.** Each dialect claims its own way: `FOR UPDATE SKIP LOCKED` on
  Postgres, and a write transaction on SQLite and D1, which have one
  writer. A claim that expires returns its message to ready, so a worker
  that dies loses nothing. Delivery is at least once, and handlers are
  idempotent.
- **Retries.** A failed handler retries after the queue's backoff, up to
  its retries, then marks the message dead, where an operator finds it.
- **Workers.** An API declares `@worker({ queue })`. Its implementation
  implements a typed handler per worker with the API's `Deps`, through a
  generated `Workers` interface and scaffold. Each worker is a deployable
  of kind `worker` by default, named after its API and its class. Its
  edges and its identity are its API's, as a job's are (section 8.7). The
  queue's DB must be the API's `authDb` or one of its DB dependencies, so
  the worker already has the connection.
- **Entrypoint.** A worker gets a module of its own beside the servers'.
  Its `main`:
  - builds `Deps` as a server's does;
  - claims and handles up to `concurrency` messages at a time;
  - on SIGTERM stops claiming, lets the running handlers finish within
    the platform's grace, and returns what is left to ready.
- **Local.** `stack dev` runs each worker as a process with no port. A
  worker that exits stops the environment, as a server's exit does.
- **gcp.** A worker is a Cloud Run worker pool
  (`gcp:cloudrunv2/workerPool:WorkerPool`), which has no port and no URL.
  Its instance count is a setting, one unless set. A member of a
  parameterized environment runs one instance per worker unless its
  settings say otherwise.

The engine's work-queue behaviors (D16) stay the engine's. A stack does
not deploy the engine, whose single SQLite writer Cloud Run cannot keep.

### 8.9 Buckets

A bucket is object storage, a service of the core kind `Bucket` (D54).
An API lists the buckets it uses in its config, by handle, as it lists
its `calls`:

```ts
// schemas/services/shop-media/schema.config.ts
export default defineConfig({ name: "shop-media", kind: SchemaKind.Bucket });

// schemas/services/shop-api/schema.config.ts
export default defineConfig({ name: "shop-api", kind: SchemaKind.API, buckets: [ShopMedia], ... });
```

- **Deployable.** Each Bucket service in the stack is a deployable of
  kind `bucket`. An API's server, jobs and workers each get a `bucket`
  edge to every bucket the API lists.
- **Derived value.** A bucket edge derives a field holding the bucket's
  name and how to reach it. On gcp that is the bucket alone, since the
  workload's account reaches it. Locally it adds the emulator's endpoint.
  The runtimes read it as they read a database's.
- **Code.** `Deps` gains a `Bucket` per bucket the API lists: a
  provider-neutral interface in the Go and TypeScript runtimes to put,
  get, delete and list objects, and to sign a URL for one. The GCS
  implementation reads `STORAGE_EMULATOR_HOST`, so it reaches the local
  emulator too. Only a server, job or worker some environment places on a
  provider links that provider's client, as Cloud SQL's connector is
  linked (D30, amended).
- **Private.** Buckets are private. A browser uploads or downloads an
  object directly through a signed URL, which also avoids Cloud Run's
  32 MiB request limit. On gcp, signing goes through IAM's `signBlob` as
  the workload's own account.
- **Local.** `stack dev` runs fake-gcs-server in a container beside
  Postgres, one per environment, with a bucket per Bucket service.
- **gcp.** A bucket is a `gcp:storage/bucket:Bucket` with uniform access
  and public access prevention. Its name starts with the project, since
  bucket names are global. The connector grants the workload's account
  `roles/storage.objectUser` on it, and the right to sign as itself. A
  member of a parameterized environment gets a bucket of its own, which
  its destroy empties.

### 8.10 Static sites

A static site is a directory a front-end build writes, served as files.
It is a service of the core kind `Site` (D55):

```ts
// schemas/services/shop-web/schema.config.ts
export default defineConfig({
  name: "shop-web",
  kind: SchemaKind.Site,
  calls: [ShopApi, ShopOrders],
  site: { build: "build", output: "dist", fallback: "index.html" },
});
```

Every member of `site` is optional: `build` is the package.json script
that builds the site, `build` unless set; `output` the directory it
writes, relative to the site's package, `dist` unless set; and
`fallback` the file, relative to the output, served for a path that names
no file, which a single-page application's router reads. A site with no
fallback answers such a path 404. A Site schema declares nothing; its
config may leave `outputs` out.

- **Source.** The site's code sits at its implementation path, the naming
  file's `[implementation_paths] site`, `web/{service}` unless set, a
  member of the Bun workspace (D51), so it imports the SDKs of the APIs it
  calls. The build writes a typed browser config there,
  `config.generated.ts`, on every build: `loadApis()` returns each API the
  site calls, by the camel case of its name, with its public `baseUrl` and
  `client(create)`, which builds a client of the API from a factory the
  site hands it, its SDK's constructor or any other. It imports no SDK:
  a browser bundle of a generated SDK takes superscalar's Node backend
  until superscalar's browser build bundles (D55, amended). When the
  package is missing, the build scaffolds a minimal site there once: an
  `index.html`, a script that loads the config, and a `build` script that
  bundles them with `bun build`.
- **Build.** The deploy runs the site's `build` script after a frozen
  install of the workspace. It digests the output as it digests a server's
  build context (D46): every file in path order, at the epoch, owned by
  root, with no link. It uploads the files under their digest when the
  digest is new, and keeps the files of every digest it uploaded. `stack
  build` builds and uploads the same way, and prints each site's digest
  as a `--site` flag, which `stack deploy` takes, with no build, to serve
  those files again: a rollback.
- **Edges.** A site gets a `site` edge to each API it calls. The API must
  be exposed, since a browser reaches it at its public address
  (`site-calls-unexposed`). The edge derives that address and nothing
  else (`ir.SiteEndpoint`): the browser carries its end user's token. A
  site rolls out a wave after the servers it calls.
- **Config.** One build serves every environment. The site reads
  `/__superschematic/config.json` when it loads, which the deploy writes
  per environment with each API's public address,
  `{"apis": {"shop-api": {"url": "https://shop-api.acme.dev"}}}`, read
  from the run's outputs once the servers it calls rolled out. It is never
  cached.
- **CORS.** Each API answers CORS for the origins of the sites that call
  it. Those origins are derived into a field of the API, `<API>_CORS`
  (`ir.CORSPolicy`, one variable, `<API>_CORS_ORIGINS`), as its callers
  are (section 9.2). The Go and TypeScript runtimes read it
  (`stackconfig.LoadCORS`, `loadCors`) and check it (`runtime/http`'s
  `cors`): a preflight from a listed origin is answered 204 with the
  origin, credentials, `Accept`, `Authorization` and `Content-Type`, the
  methods of the API's operations and a max age of ten minutes, and any
  other request from it carries the origin and credentials. Any other
  origin gets `Vary` alone. On a server that serves several APIs, a
  request is the API's whose routes take it, a preflight by the method it
  asks for. The vectors in `runtime/http/testdata/cors_parity.json` hold
  both runtimes to one decision, and the generated entrypoints wire it for
  each API a site of the stack calls.
- **Publish.** A target publishes a site through its `Sites` seam
  (`registry.SitePublisher`): the files of a build under their digest,
  and the site's config for a run. The site's platform writes where it
  serves its files from into the graph with `ir.SiteDigestToken`, and the
  deploy pins it to the digest it publishes (`stackdeploy.PinSites`), as
  it pins an image, so the graph stays a function of the schemas. The
  deploy publishes a site before the wave that rolls it out applies, and
  the apply switches the site to the new files at once. The manifest
  records each site's digest (`sites`).
- **Local.** `stack dev` builds each site once and serves the built
  directory and its config from a small file server, with the single-page
  fallback, and prints its URL in the summary (section 8.3).
- **gcp.** The site platform, `gcp.site`, gives each site a bucket of its
  own, `<project>-<site>`, and the publisher puts each build's files in it
  under the hex of their digest, `<hex>/index.html`, with a marker object
  after the last, so a prefix with the marker holds every file and is not
  put again; the config goes under the same prefix,
  `<hex>/__superschematic/config.json`. A backend bucket with Cloud CDN
  serves the bucket behind the load balancer that exposure builds (section
  7.2). The URL map rewrites `/` to `/<hex>/index.html` and every other
  path to `/<hex>/` and the path, `pathPrefixRewrite` with
  `ir.SiteDigestToken` until the deploy pins it, and applies in the site's
  rollout step: the deploy uploads, then the apply points the URL map at
  the new prefix, so the switch is one change, and a deploy given an
  earlier digest points back. A site with a fallback serves it for a 404,
  with 200, through the URL map's `defaultCustomErrorResponsePolicy`;
  since a load balancer with backend buckets alone serves no custom error
  response, the URL map sends one reserved path,
  `/__superschematic/none`, to a backend service with no backends. Files
  are served `no-cache`, so the CDN asks the bucket whether a file
  changed before it serves it, and the config `no-store`. The bucket is
  readable by `allUsers`: a backend bucket reads only public objects for a
  browser's unsigned requests, so a project whose organization policy
  forbids public buckets cannot serve a site. With no domain, the site is
  served over HTTP at its load balancer's address (D55, amended).


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
the callee's callers field (below).

| Platform | The caller sends | Lifetime | The callee checks |
| --- | --- | --- | --- |
| Cloud Run | a Google ID token, from the metadata server, whose audience is the callee's custom audience: its full resource name, `//run.googleapis.com/projects/<project>/locations/<region>/services/<service>`, which the callee's service lists | 1 hour; fetched again 5 minutes before it expires | RS256 against Google's keys; `iss` `https://accounts.google.com` or `accounts.google.com`; `aud`; `exp`; the caller's service account by its email in `email` |
| Kubernetes | a projected service account token whose audience is the callee, read from the file the kubelet keeps current | 10 minutes, the shortest Kubernetes allows; the kubelet replaces it at 80% of that, and the caller reads the file again each minute | the signature against the issuer's keys; `iss`; `aud`; `exp`; the caller's service account in `sub` |
| local, and the generic connector (section 6.2) | a token the caller signs with the edge's Ed25519 key | 5 minutes | the signature against the edge's public keys; `iss`; `aud`; `exp` |

**The callers field.** The callee's config holds, for each API it serves
with a service clause, a callers field: `<API>_CALLERS` (`SHOP_API_CALLERS`),
the API's name in upper snake case and `_CALLERS`, which begins with no
other derived field's name, so a server that serves an API and calls it
holds both. Its value, `ir.ServiceAuth` in `ir/service_auth.go`, lists the
issuers the API's server accepts, each with:

- `issuer` and `issuerAliases`, the `iss` values it writes;
- `audience`, which the token's `aud` must hold, and `algorithms`;
- `jwksUrl`, where its keys are, or `keys`, each a public JWK's JSON or a
  reference to an output that holds one;
- `subjectClaim`, the claim that names the caller, `sub` when unset, and
  `maxLifetimeSeconds`, the longest a token may live;
- `callers`: each a `subject`, the claim's value, the `deployable` it is,
  and the APIs that deployable `serves`, which a route's `from` is checked
  against.

An identity the field does not list is no caller, whatever signed its
token. The value is the runtimes' `serviceauth` config, with two changes:
the callers are a list, since a subject may be a reference, rather than a
map keyed by subject; and a key is its JWK's JSON, since it may be an
output.

The connector of each http edge between two servers gives the callee an
issuer that lists the edge's caller alone (`Connected.Callee`). Resolution
checks it against the contract (`ir.CheckServiceAuthIssuer`), with the
edge's caller as its deployable and that caller's APIs as what it serves,
and refuses an edge to an API with a service clause whose connector gives
none (`lowering`). It merges the entries of the edges to an API by issuer:
two entries that name one issuer must agree on all but their callers. The
binding names the API (`callersOf`) and the edges it comes from. An API no
other server calls gets the field with no issuers, so its server starts
and refuses every service credential.

| Connector | Issuer | Keys | Audience | Caller |
| --- | --- | --- | --- | --- |
| local | the caller's deployable | the edge's key pair's `publicJwk` output | the callee's deployable | the caller's deployable in `sub`; a token lives at most 300 seconds |
| gcp | `https://accounts.google.com`, alias `accounts.google.com` | `https://www.googleapis.com/oauth2/v3/certs` | the callee's custom audience | the caller's service account, `<service>@<project>.iam.gserviceaccount.com`, in `email` |

In environment variables the field follows section 3.4's encoding, with
two rules it adds: a list of objects is a variable that holds the list's
length, and each object's members follow the list's name and the
object's index; and a whole number is its decimal
(`SHOP_API_CALLERS_ISSUERS=1`, `SHOP_API_CALLERS_ISSUERS_0_AUDIENCE`,
`SHOP_API_CALLERS_ISSUERS_0_KEYS_0_JWK`). No issuers is
`SHOP_API_CALLERS_ISSUERS=0`. The Go runtime reads it with
`stackconfig.LoadCallers`, which refuses a variable under the field's name
that is no member, and the generated entrypoint builds the API's
`serviceauth.Verifier` from it (section 8.1).

Every Cloud Run service lists its full resource name as a custom
audience, and a caller asks the metadata server for a token for it. The
service's `run.app` URL is an output of the service, which its own
callers field cannot reference without the service depending on itself;
the resource name is composed from names, so the caller's credential and
the callee's field both hold it.

The Kubernetes platform reads the issuer's keys from the API server's
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
per edge into the gitignored local file, and the callee's callers field
references its public key, so a local stack runs the code path a deployed
one does.

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
- One callers field per server, the union over its inbound edges. Without
  `from`, a route lists every server with a `calls` edge to its API, and a
  server whose edge reaches one API of the callee would pass a route of
  another.
- The callee's `run.app` URL as the audience on Cloud Run, an output of
  the callee's own service.
- The caller's unique id in `sub` on Cloud Run, which D37 first chose. It
  is an output of the caller's account node, which every callee's config
  would reference; the email is a name resolution composes, and the
  account it names is the environment's own.
- The field as one JSON document in one variable, which section 3.4
  refuses for every derived field.

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
implementation over the config of section 9.2, which the generated Go
entrypoint builds from the API's callers field (section 8.1), and the
TypeScript and Rust entrypoints will when they exist; a deployment with a
credential that config cannot express passes its own.

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

The callers field has its own tests:

- `ir` checks the contract and its variables, and the resolver's tests
  check the merge by issuer, an API no server calls, a call within one
  server, the collisions, and each way a connector's entry can break the
  contract;
- the local and gcp targets' goldens resolve the shop with an
  `@requireService` and an `@allowService` operation on shop-api, which
  Orders calls (`stacktest.RequireServiceShop` and `AllowServiceShop`);
- `stackconfig.LoadCallers` reads the variables into a verifier config
  that admits a token signed with the edge's key, and servergen's golden
  and compiled tests cover `serviceauth.go`;
- `cli`'s `TestStackDevVerifiesServiceCallers` runs three Go servers with
  `stack dev`: the caller's call is admitted with its end user forwarded,
  a request with no credential, from a server with no edge, or for
  another audience is 401 `service_unauthorized`, and a caller the
  route's `from` leaves out is 403 `service_forbidden`.

### 9.9 Open

- A deployable that serves no API has no handle to put in `from`, and may
  call only operations whose `from` is empty. A job is not one: it serves
  its API in a callee's callers field, so `from` names it by its API
  (section 8.7, D52). A worker, when queues land, may follow the same
  rule.
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
- A call between two APIs one server serves stays on loopback and carries
  no service credential, so a `@requireService` operation the other API
  calls refuses it with 401, though resolution's check (section 9.3)
  counts the call as admitted. The call could carry a token the server
  signs for itself, or the check could leave such calls out.
- The callers field of a server outside a stack. `values-schema.json`
  does not list it, since its variables depend on the environment's
  edges, so a deployment that sets its own config writes the variables by
  hand.

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
`dev`, `plan`, `build`, `deploy`, `destroy`, `outputs` and `run`. Targets
and provisioners plug into it; they add no commands of their own. `stack
dev` runs a local environment (section 8.3), and `stack run` runs a job
once on demand, locally or in the cloud (section 8.7).

Each cloud command opens the Stack service as `stack dev` does: the one
`--stack` names, else the working directory when it is one, else the one
under `./schemas/services`. Unlike `stack dev`, which builds the stack
and reads the `environment.json` the build writes, it resolves the
environment as the `stack` generator does, without a build, so `plan`
never reads a stale one. The provisioner's program goes to
`<schemas-root>/dist/program/<stack>/<environment>`, or `--program-dir`.
`plan`, `build`, `deploy`, `bootstrap`, `destroy` and `outputs` refuse a
local environment, which `stack dev` runs, `secrets set` writes its
`secrets.env`, and `run` runs a job against it. A command
that works on one run of a parameterized environment takes each
parameter's value as `--param pr=123`. `bootstrap`, `secrets set`,
`plan`, `build`, `deploy`, `destroy`, `outputs` and `run` are built, in
`cli/stack_deploy.go` and `cli/stack_run.go` over `internal/stackdeploy`,
whose public face is in `stack`; the reference page "CLI" lists their
flags. `build` builds the images a deploy would build (section 11.2) and
deploys nothing: it prints each as an `--image` flag and writes no
manifest. A target plugs into them, and into the generated CI, through
seven seams on its `TargetSpec` (D45, D46, D47, D52):

- `State`, a state store: the provisioner's state backend for an
  environment, and each run's deploy manifest;
- `Secrets`, a secret store: set, exists and list, keyed by a secret's
  identity (section 4.2) or a credential's secret name;
- `Bootstrap`, the bootstrap of section 7.3;
- `Migrations`, a migration runner, which runs one phase of a database's
  plans where `superschematic-migrate` reaches the database, and gives the
  servers that connect their privileges (section 8.4);
- `Builder`, an image builder: a build request is a server or a job, its
  Dockerfile and its build context, which the deploy writes as an
  archive, and the result is the image by digest. Cloud Build on gcp;
- `CI`, how a generated CI job signs in to a resolved environment as
  `planner` or `deployer` (section 11.3): an identity, a kind and its
  fields, which a CI renderer turns into its own steps, or none yet.
  Workload Identity Federation on gcp;
- `Jobs`, a job runner (`JobRunner`): a request is a run, a job and the
  image the deploy manifest records for it, and the runner runs the
  deployed job once, as its platform runs it on its schedule, and returns
  when the run ends, with the last try's error when it fails (section
  8.7). Without it, `stack run` refuses the target's environments. On
  gcp, an execution of the job's Cloud Run job, whose error the runner
  reads from Cloud Logging as the migration job's.

A target with none of them resolves and does not deploy. Platform
credentials, such as a DNS platform's API token, come from one function,
`stackdeploy.CredentialsOf`, which bootstrap, `plan`, `deploy`, `destroy`
and `outputs` all read: each is a secret name, the environment variable its
provider reads and what to enter, as the DNS platform resolved them into
`environment.json` under `dns.credentials` (section 6.9). Every run reads each value with the
run's account and hands it to the provisioner, which passes it to the
tool's process for that run only, never to its config, its program or a
file.

### 11.2 Deploy

`stack deploy <environment>`:

1. decides the image of each server and job: the one `--image` names, by
   digest (`--image shop-api=<repository>@sha256:<digest>`); else a build,
   when the target builds images and the server or job has the Dockerfile
   the stack's build writes (section 8.2), unless its build context is the
   one the image the manifest records was built from; else the image the
   manifest records. One with none of them is refused, and `--no-build`
   builds nothing;
2. plans each database's migration from the model the manifest records
   (D27), with the readers of the schemas root as the readers after the
   rollout, refuses a hazard of a `--fail-on` class (every class by
   default) that no `--allow` acknowledges, and with `--expect` refuses
   plans other than the ones `stack plan --out` wrote;
3. builds the images it decided to build, through the target's
   `Builder`, before anything changes, and pins every image. A platform
   writes a server's image into the graph as its repository path, and the
   deploy pins it: every string property of the server's own nodes equal
   to the repository becomes `<repository>@<digest>`, so the program the
   provisioner renders names each image by digest;
4. applies infrastructure;
5. needs a value for every secret (section 4.2);
6. runs each database's `expand` steps (`superschematic-migrate apply
   --phase expand`), which keep the servers of the previous version
   working, and the runner then gives the servers that connect their
   privileges; a DB service whose connecting servers changed runs its
   expand phase even with no steps (section 8.4);
7. rolls servers and jobs callee first, a wave at a time, each wave
   returning once the platform reports its servers ready: Cloud Run's provider waits for
   the revision's `Ready` condition, which the startup probe on `/readyz`
   holds back until the server's databases answer (section 7.2);
8. runs the plan's `contract` steps (`--phase contract`), the drops and
   tightenings that the previous version's servers could not survive, once
   none of them runs;
9. applies exposure;
10. writes a deploy manifest to the state bucket after every step and at
    the end: the resolved environment, the IR digest of each service, the
    image of each server and job with the digest of the build context the
    deploy built it from, and each database's applied model with the servers
    its runner last saw connect.

A build context is the repository root as the server's
`Dockerfile.dockerignore` cuts it down, read by BuildKit's rules, written
as a gzipped tarball whose entries carry no time, owner or mode but the
executable bit, so the same files give the same archive on any machine.
The digest of its tar stream decides whether the server changed: it
covers the server's entrypoint module, the generated and runtime modules,
the implementations and the superscalar checkout the image builds from,
or the Dockerfile that pins the release's archives, and nothing the
ignore file leaves out. A context that lacks a path the ignore file takes
in by name, such as a superscalar checkout a CI runner never made, or
holds one under a symbolic link, which a context carries as a link and
not its files, is refused before the upload. On gcp the builder uploads the
archive to the state bucket, under `superschematic/builds/`, and runs a
Cloud Build build of the Dockerfile with BuildKit, as the `builder`
account (section 7.3), which pushes to the stack's repository with the tag
`context-<digest>`. A tag that exists is not built again, so a `stack
build` before a deploy, or a deploy that failed after its builds, saves
the next deploy the build.

Each step of the deploy order is one targeted update of the provisioner's
program (section 6.5), and each migration phase runs between two updates
through the target's migration runner, outside the graph (D45). The plans
depend on what the manifest records, and the graph is a pure function of
the schemas, so a migration is not a node.

A rollout that fails runs no `contract` step, so the previous version's
servers keep working on the expanded schema. The runner records that
schema as the database's model, and the manifest records it too. The next
deploy plans from it: its plan supersedes the pending `contract`, and any
drop still wanted is in its own `contract` (D27, amended). A migration
phase that fails is recorded in the manifest as pending, on the model the
database held before it, and the next deploy finishes that phase first, as
the runner requires, then plans from where it ends. A server whose wave
did not finish keeps the image the previous deploy recorded, and the
context it was built from, so the next deploy builds it again, or finds
the image the failed deploy built. A build that fails stops the deploy
before it changes anything.

The manifest is the migration baseline, the record of what is running, and
the starting point for a rollback. The manifest records where the last
deploy got to: `deploying` while it runs, then `deployed`, or `failed` with
the step and the error. The bucket keeps every version of it.

`stack plan <environment>` runs the provisioner's `Plan` over the program
with the images pinned, plans each database's migration the same way, and
prints both, with the secrets that have no value, the servers and jobs
with no image yet, and the records to create by hand for a `manual` domain. It
changes nothing, so the read-only `planner` account runs it. `stack
destroy` removes a run's resources and its manifest, and `stack outputs`
prints the run's outputs file, or writes it with `--out`, which the
bindings generator reads (section 6.6).

`stack plan` builds nothing: it plans each server and job at the image
`--image` names or the manifest records, and lists one with neither among
those with no image yet.

### 11.3 Generated CI

A CI renderer writes a workflow per stack from the stack and its resolved
environments (D47). A stack opts in through its config, naming the
renderer:

```ts
// schemas/services/shop-stack/schema.config.ts
export default defineConfig({
  name: "shop-stack",
  kind: SchemaKind.Stack,
  outputs: { ci: { github: { branch: "main" } } },
});
```

Each renderer takes `branch`, which pull requests target and pushes
deploy from, `main` unless set, and `install`, the renderer's directory
unless set. Only a Stack service takes `outputs.ci`, and a renderer no
extension registered fails the build. The Stack kind's `ci` generator
resolves every environment as the `stack` generator does, asks each
environment's target for its identities (`TargetSpec.CI`) and its
provisioner for its tools (`ProvisionerSpec.Tools`), and renders. It
writes the workflow under `<output-root>/ci/<stack>/<renderer>/` and
installs it into the install directory under the repository root,
`.github/workflows/<stack>.yml`, when that directory exists, through
`InstallTargetDir`. Without the directory, or outside a git repository,
it logs why and installs nothing. GitHub Actions (`github`) is the first
renderer. Others are registrations, as provisioners are. A stack without
`outputs.ci` gets no workflow, so an example in a repository with CI of
its own installs nothing. The paths in the workflow are relative to the
repository root, where a CI job starts, and its build writes to the
default output root, `<schemas-root>/dist`, which the `stack` commands
read.

The GitHub workflow:

- **On a pull request:**
  - A `check` job needs no credentials. It installs superschematic, the
    static archives the release ships for the runner's platform (section
    8.2), checked against the digest the binary names, with
    `CGO_LDFLAGS` pointing at them, and the schemas root's packages, by
    the root's lockfile (`bun install --frozen-lockfile` or `npm ci`),
    runs `build-all` over the services root, which resolves every
    environment and checks its graph (levels 1 and 3), and compiles each
    Go server's entrypoint module with `go build -mod=mod` (level 2). It
    runs on a push too. A binary the release workflow did not build names
    no digests, and its archives step fails.
  - For a stack with a TypeScript server, `check` then installs the
    output root's Bun workspace from the lockfile the project commits
    (section 8.6), `bun install --frozen-lockfile` in
    `<schemas-root>/dist`, in a step that names the lockfile. It fails
    when the lockfile is missing, where a frozen install alone would
    install without one, or no longer matches the packages the build
    wrote, and says to run `bun install` there after `build-all` and
    commit it. Then each
    TypeScript server's entrypoint package and the implementation of each
    API one serves type-checks with `tsc --noEmit` (level 2), through `bun
    x --no-install`, so the compiler is the one the package depends on and
    the install linked, never one bunx downloads. The type-check reads the
    runtime packages as the install linked them: from the registry they
    carry their declarations, while a checkout that `[paths]` names needs
    its build output in the CI checkout, which the image builds for itself
    and the workflow does not.
  - A `plan` job per cloud environment without parameters runs `stack
    plan` as `planner`, after `check`: the infrastructure diff, the
    migration plans and their hazards (levels 5 and 6).
  - A `preview` job per environment with one parameter runs `stack deploy
    <environment> --param <parameter>=<pull request number>` as
    `deployer` after `check`, a member per pull request, and `stack
    destroy` of that member when the pull request closes, when `check`
    does not run (level 7). An environment with more parameters has no CI
    job, and the workflow says so.
  - A pull request from a fork runs `check` alone: GitHub gives its jobs
    no identity token.
- **On a push to the branch:** a `deploy` job per cloud environment
  without parameters, in the order the environments are declared. The
  first deploys once `check` passes; each later one waits for the one
  before. Each runs in a GitHub environment of its own name, so that
  environment's required reviewers approve it. Reviewers are a setting of
  the repository, not of the stack. `workflow_dispatch` runs `check` and,
  from the branch, the deploys.
- One deploy at a time per environment, and one preview job per member,
  neither cancelled by the next. A plan job has a group per pull request
  and environment: GitHub keeps one pending job per group and cancels the
  one it replaces, so a plan sharing the environment's group could cancel
  a pending deploy. A plan that meets a running deploy's lock fails, and
  runs again.
- Local environments have no job. Level 4 runs on an engineer's machine.
  The workflow's header names each environment that has no job and why.

Each cloud job signs in through the target's CI identity (`TargetSpec.CI`):
on gcp, Workload Identity Federation through the pool bootstrap creates,
as `<stack>-planner` or `<stack>-deployer`. The provider's name holds the
project's number, `projectNumber` (section 7.1), which bootstrap records.
An environment without it has no cloud jobs, and the workflow names the
bootstrap to run. `google-github-actions/auth` signs in, and its
credentials file gives superschematic and Pulumi application default
credentials. The cloud jobs install the provisioner's tools: the Pulumi
provisioner declares the `pulumi` CLI at the release of the Pulumi SDK it
is built with. A job that installs Bun installs `tools.env`'s release, the
one a TypeScript server's image runs on.

The workflow installs the release of superschematic that generated it,
the version of the root module in the binary's build information, from
its repository's release page, checked against the release's
`SHA256SUMS`. A binary built from a checkout is no release, so its
workflow's install step fails and says to generate again with a released
binary. Every action is pinned by commit, and the file holds no
timestamp. The build cache keys the workflow on the stack's inputs, which
hold its config, and on the binary, which names the release, so it needs
no key of its own. Only servers whose build context changed
are built and rolled (section 11.2), so the workflow builds and deploys
whatever the deploy decides is affected, and needs no list of its own.

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
   `values-schema.json` marking those fields derived; the loader's
   refusal of an `@envVars` field that collides with one; and the callers
   field of an API with a service clause, `ir.ServiceAuth` in
   `ir/service_auth.go`, which the local and gcp connectors write and
   `stackconfig.LoadCallers` reads (section 9.2). The TypeScript API
   package's `loadEnvConfig()` reads them through the TypeScript HTTP
   runtime's readers (section 8.6). Next: the Rust loader reads the
   derived fields, in the PR that gives it `Deps`.
4. **Generators.** The server entrypoint, the Dockerfile, each API's `Deps`
   and constructor signature, and the one-time implementation scaffold
   (section 8.5). Landed for Go: `Deps` and `Constructor` in `deps.go`;
   the scaffold, which a stack's build writes for each API its servers
   serve and `build --scaffold` and `build-all --scaffold` write outside a
   stack; and the Stack kind's `server` generator
   (`internal/generator/servergen`), which writes each Go server's
   entrypoint module and Dockerfile at `server/<stack>/<server>` (sections
   8.1 and 8.2). Its clients send the D37 service credential each edge's
   endpoint names, `serviceauth.go` builds the service authenticator of
   each API with a service clause from its callers field, and a server
   some environment places on Cloud SQL links the Cloud SQL connector.
   For TypeScript, `Deps`, `Constructor` and the scaffold have landed, the
   output root's Bun workspace holds the implementations, and the
   `server` generator writes each TypeScript server's package, `main.ts`
   and Dockerfile at `server/<stack>/<server>` in the pass that writes the
   Go servers' (section 8.6). Next: OpenTelemetry export; then `Deps`, the
   constructor signature, the scaffold and the entrypoint in Rust.
   `examples/acme-shop` keeps its implementations at the scaffold layout,
   `go/shop-api`, `go/shop-orders` and `typescript/shop-storefront`,
   which the entrypoints of its `shop-stack` import (section 14,
   milestone 1, and section 8.6).
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
   it each config. Landed too: the deploy seams on `TargetSpec` (`State`,
   `Secrets`, `Bootstrap`, `Migrations` and `Builder`),
   `ProvisionRequest.Env` for a run's credentials, and the deploy that
   drives them in deploy order (`internal/stackdeploy`, section 11.2),
   with the build contexts it writes and the builds it skips, which
   `stack/stacktest`'s fake target carries too; the gcp target's
   bootstrap, Secret Manager store and state bucket (section 7.3), its
   image builds on Cloud Build, its migration runner, a Cloud Run job
   that runs `superschematic-migrate job` on Cloud SQL and owns the
   servers' privileges (section 8.4, D46), and `stackdeploy.CredentialsOf`
   reading the credentials a DNS platform resolves into
   `dns.credentials`. Next: a deploy lock beyond the provisioner's and
   the runner's, and the generated CI of section 11.3.
8. **CLI.** The `stack` command group. Landed: `stack dev` (section 8.3),
   which runs Go and TypeScript servers, and `bootstrap`, `secrets set`,
   `plan`, `deploy`, `destroy` and `outputs` (section 11).

## 13. Module layout

- **The root module:** the Stack kind, the resolver, the registry specs,
  the `local` target, the `stack` commands, and the `ci` generator with
  the `github` CI renderer (`internal/generator/cigen`): a workflow is
  text, with no dependency to keep out of the core (D47).
- **`extensions/gcp`**, a Go module of its own (D1): the gcp target's
  platforms, connectors and Cloud DNS platform, its policy rules, and its
  pinned provider schemas with the tool that keeps them current (sections
  6.4 and 7), and its bootstrap, secret store and state store over Google
  Cloud's client libraries (section 7.3), which stay out of the root
  module, its image builder and migration runner (D46), and its job
  runner (D52).
- **`extensions/pulumi`**, a Go module of its own: the provisioner and the
  binding generator (sections 6.5 and 6.6). Built: it registers provisioner
  `pulumi`, its `bindings` package is the generator, and it joins
  `make test` and CI.
- **`extensions/cloudflare`**, a Go module of its own: the Cloudflare DNS
  platform and its pinned pulumi-cloudflare schemas (sections 6.4 and
  6.9), kept current by the gcp target's tool, which both share through
  `stack/providerschema`. Built. Workers and D1 come later.
- **`cmd/superschematic`**, a Go module of its own: the installed binary.
  Built: it is a distribution of the core and the official extensions, by
  `cli.New(cli.Config{Name: "superschematic"}, gcp.Extension{},
  cloudflare.Extension{}, pulumi.Extension{ProviderVersions:
  map[string]string{"gcp": gcp.ProviderVersion, cloudflare.Package:
  cloudflare.ProviderVersion}})`, so the provisioner installs each
  provider at the release whose schemas the target or the DNS platform
  checks against. An engineer installs one binary and gets every official
  target. `extensions/topcoat` is not linked (D44); its own binary links
  it. A release builds this binary and tags the module with the others.

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
   port by hand. Done: `examples/acme-shop`'s `shop-stack` deploys
   `shop-api` and `shop-orders`, each on a default Go server, and its `Dev`
   environment is on the `local` target. Their implementations sit at
   `go/shop-api` and `go/shop-orders` behind `Deps`, with no `main` and no
   connection code. `TestStackDevRunsTheShop` (`examples/acme-shop/go`)
   runs `superschematic stack dev`, which starts Postgres with `shop-db`
   migrated and both servers on their generated entrypoints; it waits for
   each `/readyz`, signs a user in through the ORM, calls `CreateProduct`
   and `ListProducts` and then `PlaceOrder` through the generated Go SDKs,
   and stops the stack with `--remove-database`. Every URL, port and
   connection string it uses comes from the resolved `environment.json`.
   `scripts/check.sh` runs it in the full tier. What stays hand-written
   runs outside the stack: the Topcoat app's listen address, the Rust
   server's (port 0, which it prints), and the in-process base URL of the
   TypeScript storefront's tests.
2. **Model, resolver and seams.** The Stack kind, environments,
   `environment.json`, the registry specs, and levels 1 to 3 in CI.
   Done when a test extension adds a platform and a provisioner with no
   core edit.
3. **GCP and Pulumi.** Bootstrap, the gcp platforms and connectors, the
   Cloud DNS and Cloudflare DNS platforms, the Pulumi provisioner, Cloud
   Build, secrets, `plan` and `deploy`. Done when
   a fresh project plus a project id and a region gives a live acme-shop.
   A run from a maintainer's machine proves it, with an owner's
   application default credentials: no CI job, no secret in CI and no
   sandbox kept between runs. Done on 2026-10-07, from a binary built from
   a checkout, on a fresh project in `us-central1`. acme-shop's stack took
   a gcp environment beside `Dev` for the run only, since the example
   builds with the core binary, which links no target but `local`. The
   migration runner's image came from `scripts/migrate-dev-image.sh`, since
   no release exists to build it from. `stack bootstrap` ran with
   `--repository ""`, leaving Workload Identity Federation out, then
   `stack plan`, `stack build` and `stack deploy`. The deploy took seven
   minutes: Cloud SQL and the accounts, the migration job, which applied
   shop-db's 20 expand steps as the migrator's IAM database user with
   `cloudsqlsuperuser` and gave both servers their privileges, then both
   Cloud Run services. A client then signed a user in through the ORM,
   over the Cloud SQL Go connector as shop-api's IAM database user, and
   called `CreateProduct`, `ListProducts` and `PlaceOrder` through the
   generated Go SDKs at the `run.app` URLs, as `TestStackDevRunsTheShop`
   does locally. A second deploy built both images inside the deploy and
   rolled them out with no migration, and `plan` then showed no change.
   `stack destroy` removed the run in three minutes. It left what the
   stack's runs in the project share: bootstrap's state bucket, KMS key
   ring and key (which Google Cloud never deletes), Artifact Registry
   repository with the images, and its four accounts; the migration job;
   the build contexts and job documents in the bucket; and the enabled
   APIs. Deleting the project removes them all.

   The run found five bugs, each fixed in a pull request of its own:
   acme-shop's servers had no Dockerfile, since their runtime modules lie
   above the example (`[paths] build_context`, D30 amended); bootstrap
   failed on Cloud KMS until enabling its API reached every server, so it
   retries such a refusal; the operation of a build in a region answered
   NotFound, so the deploy polls the build by name; both Cloud Run
   services planned an update on every preview, from a
   `minInstanceCount` of 0 that Cloud Run does not return; and the
   migration job reported taking back Cloud SQL's own grant to
   `cloudsqlsuperuser`. A failed job execution failed its step with only
   Cloud Run's "The container exited with an error", the execution's name
   and the URL of its logs, where the runner's own error was; the deploy
   now reads that error from Cloud Logging (D46, amended). Not run: a
   domain, its load balancer and either DNS platform, secrets (acme-shop
   has none), Workload Identity Federation, a parameterized environment,
   and a calling server's network, which waits for a stack with `calls`
   edges (milestone 4).
4. **Service auth.** Admission and identity (section 9) on Cloud Run.
5. **Database lifecycle.** The `sqlgen` migration plan and apply step in
   deploys, the hazard gate and the deploy manifest.
6. **CI generation and parameterized environments.** Built (D47). A
   stack's `outputs.ci` writes its GitHub Actions workflow (section
   11.3), which checks with no credentials, plans each cloud environment
   as `planner`, deploys a preview member per pull request, and deploys
   the cloud environments in declaration order behind their GitHub
   environments' reviewers. Bootstrap records the project's number the
   workflow signs in with. No generated workflow has run on GitHub yet.
7. **Breadth.** Jobs and scheduled jobs, buckets, queues and static sites,
   and a second target (GKE or Cloudflare) added as a registration, with
   the generic connector (section 6.2) so compute can mix. Built so far:
   - TypeScript servers on Bun (section 8.6, D51);
   - jobs and scheduled jobs, on the local target and on gcp as Cloud
     Run jobs with Cloud Scheduler (section 8.7, D52);
   - static sites, on the local target and on gcp from a bucket behind a
     load balancer with Cloud CDN, never run against Google Cloud
     (section 8.10, D55).
   
   Not yet: buckets, queues with their workers, a site's SDK clients in
   the browser, and a second target.

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
