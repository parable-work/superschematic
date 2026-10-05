# Stack model

This is the design of the stack model: how a schema tree declares what
runs where, how superschematic resolves the wiring between the services in
it, and how it deploys them. Nothing in it is built yet. D30 in
`docs/DECISIONS.md` records the decisions; this document is the design they
point at.

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
- to host several DB schemas on one database;
- to add an edge the schemas cannot show (`calls`, section 3.3).

### 3.3 Edges

An edge is a need met by something that provides it. v1 has two kinds:

| Edge | From | To | Derived from |
| --- | --- | --- | --- |
| sql | server | database | the database each served API already names: its `authDb`, or its one DB-kind dependency, as `resolveUpstreamAuth` in `internal/generator/dispatch.go` reads it |
| http | server | server | `calls` on the calling server, a list of service handles |

`calls` is the one wiring fact a person writes, because no schema says that
one server's code calls another's API. It is written once, as a handle. The
config field, the URL, the invoker grant and the network rule all follow
from it.

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

Field and variable names follow a naming-file rule over the callee's
service name, with the core's rule as the default (D7, D8). A server's own
`@envVars` type holds only the application's settings. The loader refuses
an `@envVars` field whose name collides with a derived one.

The generated entrypoint (section 8.1) reads these fields, so application
code never names an environment variable. `examples/acme-shop/go/example_test.go`
connecting with `os.Getenv("DATABASE_URL")` is the code this replaces.

## 4. Authoring

### 4.1 The Stack kind

A stack is a service of a new core kind, `Stack`, whose decorators come from
`@superschematic/stack`. Its schema files declare the stack, any deployables
that differ from the defaults, and the environments. A sketch over
acme-shop:

```ts
// schemas/services/shop-stack/src/stack.schema.ts
import { ShopApi } from "@acme/shop-api";
import { ShopDb } from "@acme/shop-db";
import { ShopOrders } from "@acme/shop-orders";
import { environment, server, stack } from "@superschematic/stack";

@stack({ deploy: [ShopApi, ShopOrders], expose: [ShopApi] })
export abstract class Shop {}

@server({ serves: [ShopOrders], calls: [ShopApi] })
export abstract class Orders {}

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
    { of: ShopApi, minInstances: 1, env: { LOG_LEVEL: "warn" } },
  ],
})
export abstract class Production {}

@environment({ parameters: ["pr"] })
export abstract class Preview extends Staging {}
```

- **`deploy`** names the entry points. Everything they reach through
  `authDb`, DB dependencies and `calls` joins the stack, so `shop-db` needs
  no mention.
- **`expose`** names what is reachable from outside the environment.
  Everything else is internal, and reachable only along its edges.
- **`@server`** declares a deployable only to change a default. Here it
  replaces shop-orders' default server with one that calls shop-api.
- **`target`** picks a target (section 6.3). `gcp` holds that target's
  values, checked against the schema the target registers.
- **`domain`** is where exposed servers are reached, and **`dns`** places
  its records on a DNS platform (section 6.9). Production omits `dns` and
  gets the target's default, Cloud DNS.
- **`settings`** sets values per deployable. `of` is a service handle or a
  declared deployable's class. The loader checks each key against the
  platform's settings schema, and `env` keys against the server's
  `@envVars` fields. tsc checks the same in the editor (section 4.3).
- **`Preview extends Staging`** inherits Staging's values, and `parameters`
  makes it a family of environments, one per value (section 5.4).

Every declaration has the JSON and YAML data forms every schema has.
Handles are written as `{name, kind}` and classes by name, as other
references are.

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
  here, shop-api and the orders server.
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
arguments as data either way.

- **Handles carry their kind and config type.** The generated
  `service.generated.ts` writes the handle with two phantom type
  parameters: `service<"API", ShopApiConfig>({ name: "shop-api", kind:
  SchemaKind.API })`. The second names the service's `@envVars` type,
  wherever it lives, so the sentinel is written after the service loads. A
  DB or General handle has no config type. No person writes either
  parameter.
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

- **`@environment` infers each settings element.** Its signature uses a
  `const` type parameter over the `settings` tuple and maps each element by
  its `of`. The handle's kind picks the settings type from the chosen
  target's entry, and `env` is typed from the handle's config type: the
  keys are its fields, `Secret<T>` fields are left out so a literal for
  one fails, and `Default<T, V>` is unwrapped to `T`. The wrappers in
  `packages/schema/src/wrappers.ts` gain a phantom base type so a mapped
  type can unwrap them.

`@ts-expect-error` fixtures under the authoring packages pin the behavior,
and run with tsc in `make ts`.

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

Four registrations keep platforms and tools independent of each other and
of the core:

- a deployable is placed on a **platform**;
- an edge between two placed deployables is realized by a **connector**;
- a **target** names a platform for each deployable kind;
- a **provisioner** turns the resulting resource graph into running
  resources.

### 6.1 Platform

A platform realizes one deployable kind on one runtime: Cloud Run servers,
Cloud SQL databases, local processes, a local Postgres container. It
registers:

- the deployable kind, and what it accepts: server languages, SQL dialects;
- the JSON Schema of its settings (`minInstances`, `tier`);
- how it names and addresses a deployable in an environment, including
  under a parameter;
- `Lower(deployable, environment)`, a pure function that returns resources.

### 6.2 Connector

A connector realizes one edge kind between two platforms: Cloud Run to
Cloud SQL over sql, Cloud Run to Cloud Run over http. It returns the
resources the edge needs (an IAM grant, a Cloud SQL connection on the
service) and the value of the derived binding.

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

A target is a named bundle of:

- a platform for each deployable kind;
- the schema of its environment values (`project` and `region` for
  `gcp`);
- its default DNS platform (section 6.9);
- policy rules over the resource graph.

`gcp` is Cloud Run, Cloud SQL, Secret Manager, Cloud Build with Artifact
Registry, and a load balancer. `local` is processes, a Postgres container
and a dotenv file.

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
validate offline.

### 6.5 Provisioner

A provisioner takes a resource graph to running resources and back:

- `Render(graph, dir)` writes the tool's program where a person can read
  it;
- `Plan`, `Apply` and `Destroy` run with credentials, against a state
  backend the target's bootstrap created;
- `Outputs` reads the applied graph's outputs. They feed the bindings
  (section 6.6) and the deploy manifest (section 11.2).

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

Later provisioners are registrations: OpenTofu over the same graph, or
Kubernetes manifests that a GitOps controller applies, for Kubernetes
targets.

### 6.6 Bindings

After apply, the provisioner's outputs become a generated, typed binding
for code outside the stack. It is a Go package, with TypeScript later, that
has one value per environment and one field per deployable
(`staging.ShopApi.URL`, `staging.ShopApi.ServiceAccount`). Hand-written
Pulumi programs read it over Pulumi stack references; scripts and CI read
it over the outputs file.

The binding is the escape hatch. A resource the vocabulary lacks is written
by hand, in a program of its own, and references the stack's resources
through the binding rather than through a copied name. An environment can
name such a program, and the provisioner applies it after the stack.

### 6.7 Registry surface

There are four specs, registered like the others in section 3 of
`docs/extension-model.md`:

- `RegisterPlatform(PlatformSpec)`;
- `RegisterConnector(ConnectorSpec)`;
- `RegisterTarget(TargetSpec)`;
- `RegisterProvisioner(ProvisionerSpec)`.

Each rejects a duplicate key and is checked when the registry is assembled.
The acceptance test is D10's: a test extension adds a platform, a connector
and a provisioner with no core edit.

### 6.8 Targets after Cloud Run

| Target | Platforms | Connectors | What is specific to it |
| --- | --- | --- | --- |
| GKE | Kubernetes servers; Cloud SQL databases | GKE to Cloud SQL through Workload Identity and the Cloud SQL proxy; server to server through a Service and a NetworkPolicy derived from the edge | only the Cloud SQL connector; the Kubernetes server platform is shared |
| Hosted Kubernetes (EKS, AKS, DOKS and others) | Kubernetes servers; the cloud's managed Postgres, or an in-cluster operator | a database connector per cloud; the shared Kubernetes connector between servers | the database platform and its connector |
| Cloudflare | Workers for TypeScript servers (the generated TypeScript server uses Hono, which runs on Workers); D1 for SQLite-dialect databases | Hyperdrive to a Postgres database on another target; service bindings between Workers | everything, but through the same four specs |

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

## 7. The gcp target

### 7.1 What the engineer enters

- `project` and `region`, which are required;
- `domain`, which is optional, and its DNS platform: Cloud DNS by default,
  or Cloudflare with a zone and an API token (section 6.9);
- secret values, through `stack secrets set`.

Bootstrap reads the GitHub repository from the git remote.

### 7.2 Realization

| Stack concept | gcp |
| --- | --- |
| database | a Cloud SQL Postgres instance and a database per hosted schema; a migration job |
| server | a Cloud Run service with its own service account |
| sql edge | `roles/cloudsql.client` and an IAM database user for the server's account; a Cloud SQL connection on the service |
| http edge | `roles/run.invoker` on the callee for the caller's account; the callee's URL in the caller's config |
| internal server | internal-only ingress; callers reach it over Direct VPC egress |
| exposure | a global external Application Load Balancer, with a Google-managed certificate on a host under the domain and records written by the environment's DNS platform (section 6.9); without a domain, the `run.app` URL |
| secret | a Secret Manager secret, an accessor grant to the server's account, and an environment variable that references it |
| image | built by Cloud Build, pushed to Artifact Registry and deployed by digest |
| parameter | names suffixed with the value; a database per value on the parent's instance |

### 7.3 Bootstrap

`superschematic stack bootstrap <environment>` runs once with owner
credentials (application default credentials), and is safe to run again:

1. It enables the APIs the target's platforms use.
2. It creates the state bucket and the KMS key directly, since Pulumi needs
   them before it can run.
3. It applies a bootstrap graph through the provisioner:
   - an Artifact Registry repository;
   - a `deployer` service account and a read-only `planner` one;
   - Workload Identity Federation for the repository the git remote names;
   - a VPC with a subnet for Direct VPC egress, when a server is internal
     and called.
4. When the environment's DNS platform is Cloudflare, it asks for an API
   token scoped to the zone's DNS, and stores it in Secret Manager where
   only the `deployer` and `planner` accounts can read it.

### 7.4 Database connections

Where the server's language has a Cloud SQL connector (Go, TypeScript),
connections use IAM database authentication through it, so there is no
password. Otherwise the platform generates a password into Secret Manager
and uses the Cloud SQL mount Cloud Run provides. The server's database
field is the same either way (section 3.4).

### 7.5 Defaults

The target sets defaults that `settings` can override:

- one service account per server;
- deletion protection on production databases;
- logs to Cloud Logging, and traces to Cloud Trace through the entrypoint's
  OpenTelemetry setup.

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

The engineer still writes the implementation interfaces. They also write one
constructor, whose signature the generator writes, in a package the server
names once. A mismatch fails to compile at level 2 of section 10.

### 8.2 Container image

A generated Dockerfile per server language builds the entrypoint and the
implementation together. For Go, that is a multi-stage build to a static
binary on a distroless base; TypeScript and Rust have their own
equivalents.

### 8.3 Local stack

`superschematic stack dev` resolves the `local` environment and runs it:

- a Postgres container with migrations applied;
- each server as a process with its resolved config;
- readiness from the generated health endpoints.

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
  party holds no service credential); and both on one operation or one
  set. An `@publicRoute` operation opens its route even in a set with a
  service clause.

The IR records the effective rule on `FieldDef.ServiceCallers` and
`OperationSet.ServiceCallers`, a `ServiceCallers{Mode, From}` where `Mode`
is `require` or `allow` and `From` holds the API service names. The
generators read it through `EndpointInfo`, as they read `RequiresAuth`.

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
calls shop-api, but no shop-api operation admits orders". A handle in `from` that names a service
the stack does not deploy, or deploys without an edge, is not an error: an
API is written once and deployed in many stacks.

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
does without `authenticate`. A server with no service clause may still
be given one, so its routes can tell a delegated call from a direct one.

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
plug into it; they add no commands of their own.

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
servers keep working on the expanded schema. The runner then holds that
plan's `contract` as pending and refuses a new plan for the database until
it runs; setting it aside for a rollback that keeps the previous servers is
open.

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

1. **IR.** `authDb` and `dependencies` move into the IR (`ir/schema.go`
   records only `Imports` today), and the Stack IR types are added.
   Operations and operation sets gain `ServiceCallers` (section 9.3).
2. **Loader:**
   - Class values in the arguments of any registered decorator. Today the
     walker special-cases the decorators that take classes
     (`internal/registry/core_decorators.go:58`).
   - `ServiceHandle` typed by kind and config type
     (`ServiceHandle<"API", ShopApiConfig>`,
     `packages/schema-config/src/index.ts:31`), written by the sentinel
     generator (section 4.3), so TypeScript can restrict a handle argument
     and type its settings. The loader checks a handle's kind against the
     service it names; `internal/loader/schemaconfig/config.go:177` checks
     only that the kind exists.
   - Build-order edges from the handles a schema references, so a stack does
     not restate them in `dependencies` (`internal/buildplan/buildplan.go:31`).
3. **envgen.** The derived binding fields of section 3.4.
4. **Generators.** The server entrypoint and the Dockerfile.
5. **Runtimes.** `ServiceAuthenticator` and `ServiceCaller` in the Go, Rust
   and TypeScript HTTP runtimes (section 9.5), and a service credential
   source in the SDKs (section 9.6).
6. **Registry.** The four specs of section 6.7.
7. **CLI.** The `stack` command group.

## 13. Module layout

- **The root module:** the Stack kind, the resolver, the registry specs,
  the `local` target and the `stack` commands.
- **`extensions/gcp`**, a Go module of its own (D1): the gcp target's
  platforms, connectors and bootstrap, and its pinned provider schemas.
- **`extensions/pulumi`**, a Go module of its own: the provisioner and the
  binding generator.
- **`extensions/cloudflare`**: the Cloudflare DNS platform in v1, and
  Workers and D1 later.
- **`cmd/superschematic`**, a Go module of its own: the installed binary.
  It is a distribution of the core and the official extensions, by
  `cli.New(cli.Config{Name: "superschematic"}, gcp.Extension{},
  pulumi.Extension{}, ...)`. An engineer installs one binary and gets every
  official target.

The Pulumi SDK and the GCP client libraries stay out of the root module, as
the compiler keeps its TypeScript parser out of the runtimes. The root
module never depends on an extension module.

The installed binary is no longer the core-only program. Goal 2 of
`docs/extension-model.md` still holds: `cli.New(cli.Config{})` is the
core-only program, and the tests that prove the core works with no
extension linked run it. A downstream distribution links whichever
official extensions it wants beside its own, in the same way.

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
   `environment.json`, the four registry specs, and levels 1 to 3 in CI.
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
5. **Where a server's implementation lives.** Is it named once on the
   server, or found by a convention for default servers?
6. **The resource vocabulary.** Pulumi's schemas are a choice of
   convenience. If OpenTofu becomes a first-class provisioner, test the
   name translation in both directions.

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
