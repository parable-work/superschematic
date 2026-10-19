---
title: Stack targets
description: Write a platform, connector, target, DNS platform, provisioner or CI renderer for the stack model, pin the provider schemas it emits, and test it offline with stack.Resolve.
sidebar:
  order: 4
---

A [stack](/superschematic/guides/stacks/) deploys through registrations.
The core registers one target, `local`, which `stack dev` runs, and one
CI renderer, `github`; the gcp target, the Cloudflare DNS platform and the
Pulumi provisioner are extensions like any other, so a new cloud, DNS
provider, infrastructure tool or CI system needs no core edit.
[A deploy target](/superschematic/extending/write-an-extension/#a-deploy-target)
lists the six registrations. This page covers what each function you
write receives and returns, the seams the `stack` commands and the
generated CI deploy through, how to pin a provider's schemas, and how to
test the result. The design is sections 6, 7 and 11 of
[docs/stack-model.md](https://github.com/parable-work/superschematic/blob/main/docs/stack-model.md),
and D45, D46 and D47 in `docs/DECISIONS.md`.

`extensions/gcp`, `extensions/cloudflare` and `extensions/pulumi` are the
worked examples, and `stack/stacktest` is the smallest one: a fake target
with every registration and every deploy seam, written against the public
`registry`, `stack` and `ir` packages only. Put an extension that links a
cloud's SDK in a Go module of its own, as those three are, so the core
never depends on it.

## Names and purity

A platform, connector, target, DNS platform, provisioner or CI renderer
name is lowercase words joined by dots or hyphens (`gcp.cloudrun`,
`gcp.cloudrun-cloudsql`). `manual` is reserved: it is the DNS platform of
a domain no registered DNS platform holds.

Every function a platform, connector or DNS platform registers is pure:
the same context gives the same result, and it reads nothing but its
argument. Resolution reads every value it returns through its JSON form,
so what it accepts is exactly what `environment.json` reads back.

## Values and references

A property, a name or an address is a plain value or a reference, from
`ir`:

| Go value | JSON form | Means |
| --- | --- | --- |
| `ir.Output{Resource: "shop-api.service", Name: "uri"}` | `{"$output": {...}}` | an output of another node. The name may be a path: `dnsResourceRecords[0].data` |
| `ir.Parameter("pr")` | `{"$parameter": "pr"}` | a parameter of the environment, which the deploy run supplies |
| `ir.Concat{"shop-api-pr", ir.Parameter("pr")}` | `{"$concat": [...]}` | strings and references joined into one string |

A node's `dependsOn` gains every node its properties reference, so you
name only the dependencies a reference does not imply.

## A platform

`NameOf`, `AddressOf` and `Lower` each receive a `registry.PlatformContext`:
the `StackEnvironment` being resolved (stack, name, target, values merged
from the parents, domain and parameters, and the schemas root of the
checkout it resolves in) and a copy of the deployable resolved so far.
Every build of the CLI sets `SchemasRoot`, absolute with its links
resolved; the local target names its containers and shifts its ports by
it, so two checkouts run one environment side by side. A cloud target's
platform ignores it: nothing it names may change from checkout to
checkout.

- `Provider` names the provider the platform runs on: `local`, `gcp`,
  `cloudflare`, `fake`. It is required. Two platforms of one provider meet
  only through a connector that names both; between two of different
  providers that no connector names, the core's generic connector serves
  an http or a site edge (see below).
- `NameOf` returns the deployable's name in the environment. It sees no
  resource name, address or bindings yet. Under a parameter, reference
  the parameter, so each member gets a name of its own.
- `AddressOf` returns how an edge reaches the deployable, usually an
  output of one of its nodes. It sees the resource name.
- `PublicAddressOf`, for an exposed deployable, returns where a browser
  reaches it from outside the environment: a site edge derives the API's
  from it, and so does the generic connector. A site platform must have
  one; a server platform without one cannot be called from a site or from
  another provider.
- `Lower` returns a `registry.Lowered`: the deployable's nodes, and for an
  exposed server under a domain, the DNS records it needs, in the neutral
  shape (`ir.DNSRecord`: name, type and value). It sees the bindings too.

A node is an `ir.Resource`: an ID, a type token, properties, `DependsOn`,
a `Phase` and `Inherited`. The ID must be stable across runs, since a
provisioner keys state on it. Two producers that return the same node
share it, and two that return different nodes under one ID fail. A node
left without a phase gets its producer's default: rollout for a server's
own resources, exposure for DNS records, infrastructure otherwise. Mark a
node `Inherited` when a member of a parameterized environment reads it
from the parent instead of creating it, as the gcp target does with a
database instance.

A server platform sets each binding on the server. A derived binding is
one environment variable per member of its value, and
`ir.DerivedVariables(field, value)` encodes it the way the generated
loaders read it back:

```go
for _, b := range d.Bindings {
	if b.Source != ir.BindingDerived {
		continue
	}
	vars, err := ir.DerivedVariables(b.Field, b.Value)
	if err != nil {
		return registry.Lowered{}, fmt.Errorf("binding %s: %w", b.Field, err)
	}
	for _, v := range vars {
		if v.Secret {
			// From the platform's secret store; see below.
			continue
		}
		env = append(env, map[string]any{"name": v.Name, "value": v.Value})
	}
}
```

A variable marked `Secret` is a secret member, such as a signed-token
credential's private key (`ir.SecretDerivedMember`). Its value is always a
reference to an output, never a literal: resolution refuses a connector
that derives one as a literal. Set it from the platform's secret store,
never as a plain value. gcp writes a Secret Manager secret of the
deployable's own and a version whose data is the reference, grants the
deployable's account access, and mounts it with `valueSource.secretKeyRef`
at that version; `stack/stacktest`'s fake platforms write a fake secret
version; the local target's process reads it from its state directory,
where its provisioner resolves the reference.

`Settings` is the JSON Schema of a deployable's settings on the platform.
Resolution validates each `settings` element against it, and refuses a
server whose language is not in `Languages`, or a database whose hosted
schemas do not all support one of its `Dialects`, with `unrealizable`.

A job platform (`Kind: ir.DeployableJob`) places an API's jobs. It
declares `Languages`, as a server platform does, since a job is written in
its API's language. Its `AddressOf` may return nil: nothing reaches a job.
Its `Lower` reads what runs from the deployable's `Job`, an
`ir.ResolvedJob`: the API and the `@job` class, the schedule the
environment runs, empty when it runs only on demand, its time zone, the
timeout in seconds and the retries. A job has a server's bindings, from
its API's config and its own edges, and no callers field.
`stack/stacktest`'s `fake.job` lowers one to a job with its own account
and, for a schedule, a scheduler entry. gcp's `gcp.cloudrunjob` lowers
one to a Cloud Run job, sharing the Cloud Run service's lowering of the
account, secrets, config, Cloud SQL volume and egress, and for a schedule
to a Cloud Scheduler job and the grant that lets the job's account run
it.

A bucket platform (`Kind: ir.DeployableBucket`) keeps a Bucket service's
objects (D54). It declares no `Languages` and no `Dialects`: a bucket
runs no code and hosts no schema. Its `NameOf` names the bucket with its
provider, and should give each member of a parameterized environment one
of its own; its `AddressOf` is what its connectors derive the bucket's
name from. A bucket takes settings, such as versioning, and no `env`.
`extensions/gcp`'s `gcp.storage` lowers one to a private Cloud Storage
bucket, `stack/stacktest`'s `fake.storage` to a private bucket in the
environment's region; the core's `local.gcs` to a bucket on the
environment's fake-gcs-server container.

A worker platform (`Kind: ir.DeployableWorker`) places an API's workers
(D53). It declares `Languages` as a job platform does, and its
`AddressOf` may return nil: nothing reaches a worker. Its `Lower` reads
what runs from the deployable's `Worker`, an `ir.ResolvedWorker`: the API
and the `@worker` class, the queue and the database that holds it, how
many instances run, zero when the environment turns the worker off, the
concurrency of each and the grace it gives running handlers when it
stops. It sets `WORKER_CONCURRENCY` on the process to the concurrency,
which the worker's entrypoint reads. A worker has a server's bindings,
from its API's config and its own edges, and no callers field.
`stack/stacktest`'s `fake.worker` lowers one to a pool with its own
account, the local target's `local.worker` to a process with no port,
ready once it starts, and gcp's `gcp.cloudrunworker` to a Cloud Run worker
pool scaled by hand to the instances, sharing the Cloud Run service's
lowering of the account, secrets, config, Cloud SQL volume and egress.

A runner platform (`Kind: ir.DeployableRunner`) places an engine
database's runner (D58): one per DB service whose types compose
behaviors that an API of the stack connects to, named
`<db>-engine-runner`, in the language of the first API on it. It declares
`Languages` as a worker platform does, and its `AddressOf` may return nil.
Its `Lower` reads the deployable's `Runner`, an `ir.ResolvedRunner`: the
database, the API and the instances, one, or zero when the environment
turns it off. Its one edge is a sql edge to its database, so the target
registers a sql connector from the runner platform, and its one binding
is the connection. On Postgres one instance runs the engine's runner at a
time, under the runner's lock, so the platform runs one with CPU between
requests: `stack/stacktest`'s `fake.runner` lowers it to a pool of one,
the local target's `local.runner` to a process with no port, and gcp's
`gcp.cloudrunrunner` to a Cloud Run worker pool of one instance. A target
with no runner platform cannot run a stack whose APIs open the engine.

A server, job or worker platform says what a deploy builds for the
deployables it places in `Artifact` (D64): `ir.ArtifactImage`, the
default, an image the target's `Builder` builds from the Dockerfile a
stack's build writes, which the platform writes into the graph as its
repository path; or `ir.ArtifactBundle`, the files `bun bundle.ts <dir>`
writes in the deployable's package, which the target's `Bundles` keeps.
Resolution records it as the deployable's `artifact`. A bundle's platform
names each file it reads relative to the provisioner's program, with
`ir.BundlePath(deployable, file)`, `artifacts/<deployable>/{bundle-digest}/<file>`,
and its SHA-256 with `ir.BundleFileToken(file)`; the deploy pins both and
places the files there before the provisioner runs:

```go
modules = append(modules, map[string]any{
	"name":          "main.js",
	"contentFile":   ir.BundlePath(d.Name, "main.js"),
	"contentSha256": ir.BundleFileToken("main.js"),
})
```

`stack/stacktest`'s `fake.bundle` places a TypeScript server that way, as
a script reading `main.js` and its source map.

## A connector

`Connect` receives a `registry.ConnectorContext`: the environment, the
edge with its field set, and copies of both deployables with their names
and addresses. It returns a `registry.Connected`: the edge's nodes, such
as an IAM grant, and `Value`, the derived binding's value on the calling
server.

That value has a contract per edge kind, in `ir/derived_value.go`, and
resolution refuses one that breaks it with `lowering`, naming the member
at fault:

| Edge | Value | Members |
| --- | --- | --- |
| sql | `ir.DatabaseConnection` | `URL`, a connection string; or `CloudSQL`: `Instance`, `Database` and `User` |
| http | `ir.ServiceEndpoint` | `URL`, the callee's base URL, and an optional `Credential`: its `Source` (`google-id-token`, `token-file` or `signed-token`), the members that source reads, and the `Headers` that carry it |
| bucket | `ir.BucketConnection` | `Name`, the bucket's name with its provider, and an optional `Endpoint`, the base URL of an emulator that serves the provider's API in its place. No credential: the workload's own identity reaches the bucket, which the connector's grant allows |

Each member may hold a reference. An http edge between two APIs one
server serves runs from the server to itself, and its connector derives
the server's own address. Return an error for an edge the platforms
cannot serve, as the gcp sql connector does for a Rust server.

A connector's `From` is a server, a job or a worker platform, and its
`To` a database platform for a sql edge, a server platform for an http
edge and a bucket platform for a bucket edge. A job or a worker takes its
API's edges, so a target with a job or a worker platform registers a
connector from it for each edge its server platform has; it may share the
server connector's `Connect`, which sees the job or the worker as `From`,
as gcp's do. For an http edge, the callee's issuer lists the job or the
worker as a caller that serves its API.

### Across providers

The core registers a generic connector for http edges, `generic.http`,
and one for site edges, `generic.site` (`ConnectorSpec.Generic`, which
names no platform). `Registry.Connector` returns the connector that names
both platforms when there is one, and otherwise the generic one of the
edge's kind when the two platforms' providers differ. So a target needs
no connector to or from another provider's platforms, and a connector
that knows both, such as a database driver for another cloud's Postgres,
still wins where it is registered. Two platforms of one provider that no
connector names get none, and resolution reports `no-connector`. A sql or
a bucket edge across providers always needs a connector of its own.

The generic connector reaches the callee at its public address, so the
callee's server must be exposed: resolution refuses an http edge across
providers to one the stack does not expose with
`cross-provider-unexposed`, and one whose platform gives no public
address with `lowering`. For an http edge it adds a key node, the Pulumi
`tls` provider's `PrivateKey` with the `ED25519` algorithm, which the core
pins and registers (`internal/stack/generic/schemas`), and derives:

- for the caller, the callee's public address and a `signed-token`
  credential, `iss` and `sub` the caller's deployable and `aud` the
  callee's, whose `Key` references the node's `privateKeyPemPkcs8`, a
  secret member, which the caller's platform sets from its secret store;
- for the callee, an issuer that is the caller, with the node's
  `publicKeyPem` as its one key (`ir.ServiceAuthKey.PEM`), EdDSA and a
  lifetime of 300 seconds.

The HTTP runtimes read both PEMs: a signed-token source signs with the
PKCS#8 key, its `kid` the key's RFC 7638 thumbprint, and the callers
reader turns the SubjectPublicKeyInfo key into a JWK with the same `kid`.
A platform that can be called from another provider gives its exposed
servers a `PublicAddressOf`; one that can call out sets secret members
from its store. Each edge has one key slot today; rotation is not built.
A distribution that links the pulumi provisioner pins the `tls` provider
for it, `stack.GenericKeyPackage` at `stack.GenericKeyProviderVersion`.

## A target

A `TargetSpec` names a platform for each deployable kind (`server`,
`database`, `job`, `worker` and `bucket`), the JSON Schema of an
environment's values under the target's name, its default DNS platform,
its provisioner, the schema of every resource type its platforms,
connectors and default DNS platform emit, and its policy rules. A kind it
names no platform for is refused in its environments, so a stack whose
APIs declare jobs or workers resolves on it only with each placed on
another target's platform.

A policy rule is a name and a `Check` over the whole resolved
environment that returns one message per violation:

```go
registry.PolicyRule{Name: PolicyHighAvailability, Check: checkHighAvailability}

func checkHighAvailability(env *ir.ResolvedEnvironment) []string
```

`Finalize` refuses a target whose platforms, DNS platform or provisioner
nobody registered, so a distribution that links a target links the
provisioner it names. The gcp target's tests register a stub provisioner
under the name `pulumi` for that reason.

### Deploy seams

A target that resolves can be built and checked. To bootstrap, plan and
deploy it with the `stack` commands and the generated CI, it also fills
seams on its `TargetSpec`, each an interface in `registry`:

| Field | Interface | Does |
| --- | --- | --- |
| `State` | `StateStore` | gives the provisioner's state backend for an environment, and reads, writes and deletes each run's deploy manifest |
| `Secrets` | `SecretStore` | sets, gets, lists and checks secret values, keyed by a secret's identity (`PaymentsSecrets.STRIPE_KEY`) or a platform credential's secret name |
| `Bootstrap` | `Bootstrapper` | prepares a cloud project once, with the provisioner, the program directory and the credentials the environment needs, and returns the values only the cloud knows, each a `BootstrapValue` the core records in the schema beside the value it belongs with (gcp's `projectNumber`, beside `project`) |
| `Migrations` | `MigrationRunner` | runs one phase of each database's migration plans between two steps of the deploy |
| `Builder` | `ImageBuilder` | builds a server's or a job's image from the Dockerfile a stack's build writes, and returns it by digest; `BuildRequest.Deployable` names which |
| `CI` | `CIIdentities` | says how a generated CI job signs in to a resolved environment as `planner` or `deployer`: a `CIIdentity`, or nil when it cannot yet |
| `Jobs` | `JobRunner` | runs a deployed job once on demand, for `stack run`: a `JobRunRequest` names the run, the job and the image the deploy manifest records, and `RunJob` returns when the run ends, with the last try's error when it fails (D52) |
| `Sites` | `SitePublisher` | puts a site's files under their digest, and its config for a run, where its platform serves them (D55) |
| `Bundles` | `BundlePublisher` | keeps the files of a server's, job's or worker's bundle under their digest, `Publish(ctx, run, deployable, dir, digest)` (D64) |
| `PreDestroy` | `DestroyPreparer` | readies a run's resources before `stack destroy` hands them to the provisioner, such as emptying a member's buckets; an error stops the destroy (D64) |

A target with none of them resolves and does not deploy. `RegisterTarget`
refuses any of them but `Secrets` without a provisioner, and any of them
but `State` and `Secrets` without `State`. With no `Migrations`, a deploy
that has a migration to run is refused; with no `Builder`, every image
comes from `--image` or the deploy manifest; with no `Jobs`, `stack run`
refuses the target's environments; with no `Sites` or `Bundles`, an
environment with a site, or with a deployable that runs a bundle, does not
deploy.

A `BundlePublisher` should also be a `registry.ArtifactFetcher`,
`Fetch(ctx, run, deployable, digest, dest)`, which writes the files it
keeps under a digest into `dest`, each executable when it was published
so. A deploy fetches a bundle it did not build, for a `--bundle`
rollback, a `--no-build` deploy, `stack plan` and `stack destroy`, checks
it against the digest and places it beside the program. A
`SitePublisher` that is an `ArtifactFetcher` has each site's files placed
there too, at `artifacts/<site>/<hex>/`, for a provider that uploads them
from the program; gcp's is not one. The gcp target fills all seven: its migrations run
each phase as an execution of the stack's Cloud Run job, which runs
`superschematic-migrate` on Cloud SQL (`gcp.Extension{Migrations: ...}`
takes a runner of your own instead), its builder builds each changed
server's and job's image on Cloud Build (D46), its CI identity signs a
job in through Workload Identity Federation (D47), and its job runner
runs an execution of a job's Cloud Run job and reads a failed one's error
from Cloud Logging (D52). Each operation works on a
`registry.Run`: the resolved environment and the values of its
parameters.

A `CIIdentity` is a kind and its fields, which a CI renderer turns into
its own sign-in steps. gcp's kind is `gcp-workload-identity`, with
`provider`, the workload identity provider bootstrap creates, and
`account`, the role's service account. Its `Identity` is pure, and nil
until the environment's values hold what the identity names, as gcp's is
until bootstrap records `projectNumber`. A renderer refuses a kind it
does not know, so a target with a new kind of sign-in teaches the
renderers it is used with.

## A DNS platform

`Lower` receives a `registry.DNSContext`, the environment, the DNS
platform's values and the records, and returns their nodes. A DNS
platform of another provider than the target's brings the schemas of
its own resource types in `ResourceTypes`, as Cloudflare's does. One
schema per type holds across the registry: a type two registrations
declare with different schemas is refused.

`Credentials` names the secrets the provider reads when the provisioner
runs, each an `ir.DNSCredential` with the secret's name, the environment
variable and what the engineer enters. Resolution writes them into
`environment.json` under `dns.credentials`. No value ever enters the
graph. The `stack` commands read them through `stackdeploy.CredentialsOf`:
bootstrap asks for each and stores it in the target's secret store, and
`plan`, `deploy`, `destroy` and `outputs` hand each to the provisioner for
that run.

## A provisioner

A provisioner implements `registry.Provisioner`:

```go
type Provisioner interface {
	Render(env *ir.ResolvedEnvironment, dir string) error
	Plan(ctx context.Context, req ProvisionRequest) ([]PlannedChange, error)
	Apply(ctx context.Context, req ProvisionRequest, step ir.DeployStep) error
	Destroy(ctx context.Context, req ProvisionRequest) error
	Outputs(ctx context.Context, req ProvisionRequest) (map[string]map[string]any, error)
}
```

A `ProvisionRequest` carries the resolved environment, the run's
parameter values, the directory `Render` wrote, the build's output root,
the `StateBackend` (its URL, its secrets provider, and `Env`, what the
state store gives a run to reach the state and decrypt it, such as S3
credentials and a passphrase), and `Env`, the platform credentials the run
reads from the target's secret store. The provisioner hands both to its
tool's process for that run only, the backend's last. `Render` takes the whole
environment, not only its graph, because the program exports every output
the environment references, a deployable's address included. `Apply`
applies one step of the deploy order, so the deploy runs migrations
between steps through the target's runner; a migrate step holds no nodes.
The Pulumi
provisioner's
[`provision.go`](https://github.com/parable-work/superschematic/blob/main/extensions/pulumi/provision.go)
is the reference, and its tests run every operation against a `file://`
backend with the `random` provider, which needs no credentials.

A `ProvisionerSpec` also lists in `Tools` the command-line tools the
provisioner runs, each with a name and the version it needs, which a
generated CI job installs before it plans or deploys. The Pulumi
provisioner declares the `pulumi` CLI at `pulumi.CLIVersion`, the release
of the Pulumi SDK it is built with.

## A CI renderer

A CI renderer writes a stack's workflow for one CI system. It registers
a `registry.CIRendererSpec`: its name, which is the key of `outputs.ci` in
a stack's config, `Dir`, the directory it installs into relative to the
repository root (`.github/workflows`), and a pure `Render`:

```go
Render func(registry.CIRequest) ([]registry.CIFile, error)
```

A `CIRequest` carries the stack, with its service's directory, the
schemas root, the output root the workflow's build writes and the schemas
root's package manager, all relative to the repository root; its
environments in declaration order, each resolved, with the identity its
target's `CI` seam gives each role and its provisioner's tools; the
renderer's options from the config (`branch` and `install`, with their
defaults applied); `Version`, the release of superschematic that
renders, empty for a binary built from a checkout; and `Archives`, by
platform (`linux-x64`), the URL and SHA-256 of the static archives the
release ships, which a job that compiles a Go server installs and points
`CGO_LDFLAGS` at, empty for a binary the release workflow did not build.
Each `CIFile` is a
path relative to the install directory and its bytes. The Stack kind's
`ci` generator writes them under `<output-root>/ci/<stack>/<renderer>/`
and installs them when the directory exists. The core's `github`
renderer, in `internal/generator/cigen`, is the reference.

## Pin the provider schemas

Resolution validates every node's properties against the schema
registered for its type, offline. Take those schemas from the provider
release your provisioner installs, and pin them. `stack/providerschema`
is the format, one file per Pulumi type token with its input properties,
the object types they reach, and its Terraform name and property renames,
and `stack/providerschema/pintool` writes and checks the files.

The Cloudflare extension's layout is the pattern:

```
extensions/cloudflare/
  schemas/
    pulumi-cloudflare.json               the pin: package, version, upstream digests, types
    index.dnsRecord.DnsRecord.json       one file per pinned type
    schemas.go                           embeds them; ResourceTypes(version)
  internal/tools/providerschemas/main.go pintool.Main(schemas.PinFile)
```

```sh
cd extensions/cloudflare
go run ./internal/tools/providerschemas                  # write schemas/
go run ./internal/tools/providerschemas -check           # fail on drift, in CI
go run ./internal/tools/providerschemas -version 6.22.0  # move the pin
```

Keep a `ProviderVersion` constant beside it. `schemas.ResourceTypes`
refuses a pin at another version, and the distribution passes the
constant to the provisioner (`pulumi.Extension{ProviderVersions: ...}`),
so the provider that applies a node is the one its schema came from.

The root module pins one provider the same way, for the generic
connector's key node: pulumi-tls, in `internal/stack/generic/schemas`,
written by `go run ./internal/tools/providerschemas` from the repository's
root. pulumi-tls is bridged through the Terraform plugin framework, whose
bridge metadata has no auto-aliasing table, so the tool takes the
Terraform type from the metadata's renames table and refuses a list
property there, whose Terraform name it cannot know. A connector that
emits a type no target registers brings its schema in
`ConnectorSpec.ResourceTypes`, as a DNS platform does.

## Test it offline

Assemble a registry with your extension, resolve a stack, and compare
the written `environment.json` with a golden file. `stacktest.AcmeShop()`
returns the facts of services shaped like the acme shop's, so you need no
schema tree:

```go
reg, err := registry.Assemble(registry.DefaultNaming(), gcp.Extension{}, pulumiStub{})
if err != nil {
	t.Fatal(err)
}
resolved, err := stack.Resolve(reg, stack.Input{
	Stack:       shop(), // an *ir.Stack whose environments name your target
	Services:    stacktest.AcmeShop(),
	Environment: "Staging",
})
if err != nil {
	t.Fatal(err)
}
path, err := stack.Write(t.TempDir(), resolved)
```

A failed resolution is a `*stack.Errors`, and `Has(code)` tells the
failures apart without matching message text. `extensions/gcp/gcp_test.go`
and `extensions/cloudflare/cloudflare_test.go` are complete examples, with
`-update` to rewrite their goldens. To build a stack written as a schema,
pass your extension to `cli.New` and run `build`, as
`TestEveryBuildWritesAStacksEnvironments` in `cli/build_stack_test.go`
does with the fake target.

The deploy itself is public too: `stack.Deploy`, `stack.Plan`,
`stack.Destroy`, `stack.Outputs`, `stack.Bootstrap`, `stack.SetSecrets`
and `stack.RunJob` are what the commands call. `stack/stacktest` has
in-memory seams (`FakeState`, `FakeSecrets`, `FakeMigrations`,
`FakeBootstrap`, `FakeBuilder`, `FakeJobs`, `FakeSites`, `FakeBundles` and
`FakePreDestroy`) that record each call, so a test reads the order a deploy
ran in, and `FakeCI`, a CI seam shaped like gcp's. `WithBundle` places
shop-storefront on `fake.bundle`, and `WriteBundlePackage` writes the
package whose `bundle.ts` a deploy runs.
`extensions/pulumi/deploy_test.go` deploys through the real provisioner
against a `file://` backend.

To check what the generated CI does with a target, read each resolved
environment with `registry.CIEnvironment`, which asks the target's `CI`
seam and the provisioner's `Tools` as the `ci` generator does, and render
it with the core's `github` renderer (`reg.CIRenderer("github")`), as
`extensions/gcp/ci_test.go` does against its golden workflow.

## Link it

A binary links the target, any DNS platform of another provider, and the
provisioner, with each provider's pin:

```go
cli.New(cli.Config{Name: "my-schematic"},
	gcp.Extension{},
	cloudflare.Extension{},
	pulumi.Extension{ProviderVersions: map[string]string{
		"gcp":              gcp.ProviderVersion,
		cloudflare.Package: cloudflare.ProviderVersion,
	}},
	myTarget.Extension{},
)
```

The installed `superschematic` links `gcp.Extension{}` and the pulumi
provisioner pinned to the gcp provider, and not the Cloudflare extension:
a binary that uses Cloudflare DNS links it, as above.
