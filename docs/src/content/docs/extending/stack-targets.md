---
title: Stack targets
description: Write a platform, connector, target, DNS platform or provisioner for the stack model, pin the provider schemas it emits, and test it offline with stack.Resolve.
sidebar:
  order: 4
---

A [stack](/superschematic/guides/stacks/) deploys through registrations.
The core registers one target, `local`, which `stack dev` runs; the gcp
target, the Cloudflare DNS platform and the Pulumi provisioner are
extensions like any other, so a new cloud, DNS provider or infrastructure
tool needs no core edit.
[A deploy target](/superschematic/extending/write-an-extension/#a-deploy-target)
lists the five registrations. This page covers what each function you
write receives and returns, the seams the `stack` commands deploy
through, how to pin a provider's schemas, and how to test the result. The
design is sections 6, 7 and 11 of
[docs/stack-model.md](https://github.com/parable-work/superschematic/blob/main/docs/stack-model.md),
and D45 in `docs/DECISIONS.md`.

`extensions/gcp`, `extensions/cloudflare` and `extensions/pulumi` are the
worked examples, and `stack/stacktest` is the smallest one: a fake target
with every registration and every deploy seam, written against the public
`registry`, `stack` and `ir` packages only. Put an extension that links a
cloud's SDK in a Go module of its own, as those three are, so the core
never depends on it.

## Names and purity

A platform, connector, target, DNS platform or provisioner name is
lowercase words joined by dots or hyphens (`gcp.cloudrun`,
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
from the parents, domain and parameters) and a copy of the deployable
resolved so far.

- `NameOf` returns the deployable's name in the environment. It sees no
  resource name, address or bindings yet. Under a parameter, reference
  the parameter, so each member gets a name of its own.
- `AddressOf` returns how an edge reaches the deployable, usually an
  output of one of its nodes. It sees the resource name.
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
		env = append(env, map[string]any{"name": v.Name, "value": v.Value})
	}
}
```

`Settings` is the JSON Schema of a deployable's settings on the platform.
Resolution validates each `settings` element against it, and refuses a
server whose language is not in `Languages`, or a database whose hosted
schemas do not all support one of its `Dialects`, with `unrealizable`.

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

Each member may hold a reference. An http edge between two APIs one
server serves runs from the server to itself, and its connector derives
the server's own address. Return an error for an edge the platforms
cannot serve, as the gcp sql connector does for a Rust server.

## A target

A `TargetSpec` names a platform for each deployable kind, the JSON Schema
of an environment's values under the target's name, its default DNS
platform, its provisioner, the schema of every resource type its
platforms, connectors and default DNS platform emit, and its policy rules.

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
deploy it with the `stack` commands, it also fills four seams on its
`TargetSpec`, each an interface in `registry`:

| Field | Interface | Does |
| --- | --- | --- |
| `State` | `StateStore` | gives the provisioner's state backend for an environment, and reads, writes and deletes each run's deploy manifest |
| `Secrets` | `SecretStore` | sets, gets, lists and checks secret values, keyed by a secret's identity (`PaymentsSecrets.STRIPE_KEY`) or a platform credential's secret name |
| `Bootstrap` | `Bootstrapper` | prepares a cloud project once, with the provisioner, the program directory and the credentials the environment needs |
| `Migrations` | `MigrationRunner` | runs one phase of each database's migration plans between two steps of the deploy |

A target with none of them resolves and does not deploy. `RegisterTarget`
refuses `State`, `Bootstrap` or `Migrations` without a provisioner, and
`Bootstrap` or `Migrations` without `State`. With no `Migrations`, a
deploy that has a migration to run is refused, which is where the gcp
target stands today; `gcp.Extension{Migrations: ...}` takes a runner of
your own. Each operation works on a `registry.Run`: the resolved
environment and the values of its parameters.

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
the `StateBackend` (its URL and its secrets provider), and `Env`, the
platform credentials the run reads from the target's secret store, which
the provisioner hands to its tool's process for that run only. `Render` takes the whole
environment, not only its graph, because the program exports every output
the environment references, a deployable's address included. `Apply`
applies one step of the deploy order, so the deploy runs migrations
between steps through the target's runner; a migrate step holds no nodes.
The Pulumi
provisioner's
[`provision.go`](https://github.com/parable-work/superschematic/blob/main/extensions/pulumi/provision.go)
is the reference, and its tests run every operation against a `file://`
backend with the `random` provider, which needs no credentials.

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
`stack.Destroy`, `stack.Outputs`, `stack.Bootstrap` and `stack.SetSecrets`
are what the commands call. `stack/stacktest` has in-memory seams
(`FakeState`, `FakeSecrets`, `FakeMigrations` and `FakeBootstrap`) that
record each call, so a test reads the order a deploy ran in, and
`extensions/pulumi/deploy_test.go` deploys through the real provisioner
against a `file://` backend.

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
