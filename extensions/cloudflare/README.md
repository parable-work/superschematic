# extensions/cloudflare

The Cloudflare extension of the stack model
([docs/stack-model.md](../../docs/stack-model.md), sections 6.9 and 18,
D64). It registers the DNS platform `cloudflare`, which writes an
environment's domain records into a Cloudflare zone, whatever target runs
the environment's servers, and the target `cloudflare`, which runs an
environment on one Cloudflare account. The target's values, its state in
R2, its secrets in the Secrets Store, its bootstrap, its CI identity, its
Workers platform, which runs TypeScript servers as Workers from the
bundles it keeps in R2 and joins them through service bindings, its R2
platform for buckets, its D1 platform and D1 migrations, its Cron
platform, which runs TypeScript jobs as Workers under Cron Triggers, and
its site platform, which serves sites as Workers with static assets kept
in R2, are built.

It is a Go module of its own, as `extensions/gcp` and `extensions/pulumi`
are, and registers through the public `registry` package alone (D10).

## Placing a domain on Cloudflare

An environment names the platform and its values under `dns`:

```ts
@environment({
  target: "gcp",
  gcp: { project: "acme-staging", region: "us-east1" },
  domain: "staging.acme.dev",
  dns: { cloudflare: { zone: "acme.dev", zoneId: "023e105f4ecef8ad9ca31a8372d0c353" } },
})
export abstract class Staging {}
```

| Value | |
| --- | --- |
| `zone` | Required. The name of the Cloudflare zone that holds the domain. The domain, and every record, must be the zone or under it. |
| `zoneId` | Required. The zone's identifier, 32 hexadecimal characters, from the zone's Overview page. It is not a secret. |
| `proxied` | Optional, `false` by default. Proxies each host through Cloudflare. |

Resolution refuses a missing value, an unknown key and a value of the
wrong shape, naming the DNS platform and the value.

The zone id is a value rather than looked up by the zone's name. A lookup
is a function call in the provisioner's program, which the resource graph
cannot express, and it would make every plan depend on Cloudflare's API.

## What it writes

Each record exposure produces is one `cloudflare:index/dnsRecord:DnsRecord`
of pulumi-cloudflare 6 (Terraform's `cloudflare_dns_record`; the
provider's 6.0 renamed `cloudflare:index/record:Record` to it), keyed
`dns.<deployable>.<type>`, with:

- `zoneId` from the values, and `name` the record's full name;
- `type` A, AAAA, CNAME or TXT. Any other type is refused;
- `content`, the record's value. A TXT value is written as double-quoted
  character strings of at most 255 bytes, as Cloudflare stores it, and a
  trailing dot is dropped from a name and from a literal CNAME target;
- `proxied`, false unless the values set it. A proxied record has the
  automatic TTL (`1`), and a DNS-only one a TTL of 300 seconds. A TXT
  record, and a record whose name begins with an underscore label such as
  `_acme-challenge`, is never proxied: it is not a host, and a proxied
  certificate validation record would not answer with its value.

The zone itself is not created: it holds the domain before the stack does.

## The API token

The provider reads a Cloudflare API token from `CLOUDFLARE_API_TOKEN`. The
token never enters the resource graph, the rendered program or a file.
The DNS platform names it as a credential, which resolution writes into
`environment.json`:

```json
"dns": {
  "platform": "cloudflare",
  "values": { "zone": "acme.dev", "zoneId": "..." },
  "credentials": [{
    "secret": "shop-cloudflare-dns-acme_dev",
    "env": "CLOUDFLARE_API_TOKEN",
    "description": "a Cloudflare API token with the DNS Edit permission on zone acme.dev, and no other"
  }]
}
```

- The secret is named `<stack>-cloudflare-dns-<zone>`, the zone's dots as
  underscores (`TokenSecret`). A zone's name holds no underscore, so two
  zones never share a secret, and the environments of a stack whose
  records share a zone share one token.
- The target's bootstrap asks for the token when an environment's
  credentials name it, and stores it in the target's secret store under
  that name: for gcp, a Secret Manager secret in the environment's project
  that only the `deployer` and `planner` accounts can read
  (docs/stack-model.md, section 7.3).
- A plan, an apply or a destroy reads the secret's latest version and sets
  `CLOUDFLARE_API_TOKEN` in the environment the provisioner runs the
  pulumi CLI in, for that run only.

## The cloudflare target

An environment names its account; bootstrap records the rest beside it:

```ts
@environment({
  target: "cloudflare",
  cloudflare: { accountId: "0123456789abcdef0123456789abcdef" },
})
export abstract class Staging {}
```

`telemetry: { endpoint, sampleRatio? }` beside `accountId` turns trace
export on for each server and job, to that OTLP/HTTP collector: each gets
`plain_text` bindings `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`,
`OTEL_RESOURCE_ATTRIBUTES` (the environment, `cloud.provider=cloudflare`,
`cloud.platform=cloudflare_workers`, the account and
`service.version=sha256:{bundle-digest}`, which the deploy pins), and
with a ratio the sampler's two. A server's or a job's `telemetry: false`
or `true` turns its own off or on; on with no endpoint is refused, and so
is a config field of one of those names.

The target places no queue worker, and says why when it refuses one
(`TargetSpec.Unplaced`): a worker's claim loop needs a long-lived process
and Postgres, which gcp's `gcp.cloudrunworker` and `stack dev` give.

| File | |
| --- | --- |
| `cloudflare.go`, `target.go` | the registration, the values, the state's layout in R2 (`StateBucket`, `StateURL`, and bootstrap's own `BootstrapBucket`, `BootstrapURL`) |
| `cloud.go`, `sigv4.go` | `Cloud`, what the target calls on Cloudflare outside the provisioner, over the REST API and R2's S3 API with plain `net/http` and SigV4; tests pass a fake |
| `state.go` | the state store: Pulumi's S3 backend on R2, with no AWS setup of the shell's, the passphrase secrets provider, the manifests with a history copy, and each run's deploy lock, created with `If-None-Match: *` |
| `secrets.go` | the secret store over the Secrets Store, `<Stack>-<Environment>-<Type>-<FIELD>` |
| `bootstrap.go`, `github.go` | bootstrap: the state and bootstrap buckets, their passphrases and each one's PBKDF2 check, written once, the Secrets Store, the deployer, planner and preview account tokens, kept in the bootstrap bucket, and `--github` through `gh` |
| `ci.go` | the CI identity, `cloudflare-api-token`, naming the GitHub secrets each job reads, `<NAME>_<STACK>_<ENV>` |
| `names.go` | every name in the account, `<stack>-<env>-<name>` (`AccountName`), suffixed per member, and the refusal of a Worker name over 63 characters, naming the part to shorten |
| `workers.go` | the `cloudflare.workers` platform: a TypeScript server as a Worker, its version and its deployment, its config as bindings, its custom domain |
| `bundles.go` | the bundle store: each bundle's files in the state bucket under their digest, and back |
| `calls.go` | `cloudflare.workers-workers`, the connector of a call between Workers: a service binding and a signed token with the edge's key |
| `r2.go` | the `cloudflare.r2` platform, `cloudflare.workers-r2`, the connector of a bucket edge, `buckets-never-public`, and `PreDestroy`, which empties a member's buckets |
| `d1.go` | the `cloudflare.d1` platform: a `d1Database` per hosted DB schema, `<stack>-<env>-<db>` with a suffix per parameter, in `sqlite`, with `locationHint` or `jurisdiction`; and `cloudflare.workers-d1`, the connector of a sql edge, a d1 binding |
| `migrations.go` | the migration runner: `superschematic-migrate job` on the deploying machine, against `d1://<accountId>/<uuid>`, each uuid read from the applied graph's outputs (`MigrationRequest.Outputs`); a deploy that only changes which servers connect needs no runner (`PrivilegelessMigrations`) |
| `cron.go` | the `cloudflare.cron` platform: a TypeScript job as a Worker, its Cron Trigger, in UTC, within Cloudflare's 15 minutes for a scheduled run, and its Workflow; and `cloudflare.cron-workers`, `cloudflare.cron-r2` and `cloudflare.cron-d1`, a job's edges through the server's connectors |
| `jobs.go` | the job runner (`Jobs`): an instance of a job's Workflow through Cloudflare's API, for `stack run` and a job on every deploy |
| `telemetry.go` | trace export: the environment's `telemetry` and a deployable's, as `OTEL_` bindings, and `WorkersCompatibilityFlags`, `nodejs_als` |
| `sites.go`, `site.js` | the `cloudflare.sites` platform: a site as a Worker with static assets and the script it embeds; the `cloudflare.site-workers` connector; and the site store, each build's files in the state bucket under their digest, and back |
| `policy.go` | `domain-on-cloudflare` and `nothing-public-unless-exposed`, which also refuses an internal Worker's preview URLs and a Worker with no `subdomain` |

Every command reads the token from `CLOUDFLARE_API_TOKEN` and the state's
passphrase from `PULUMI_CONFIG_PASSPHRASE`; `stack bootstrap` also reads
its own state's passphrase from `SUPERSCHEMATIC_BOOTSTRAP_PASSPHRASE`,
which no CI job holds. On the cloudflare target the
DNS platform names no credential: the run's token carries the zone.

## Servers on Workers

`cloudflare.workers` places a TypeScript server, its artifact a bundle
(D64, section 18.5 of the design). It lowers a server to:

- `<server>.worker`, a `cloudflare:index/worker:Worker` in infrastructure
  named `<stack>-<env>-<server>`, with Workers Logs on unless `logs: false`,
  workers.dev on only for an exposed server without a domain, previews off;
- `<server>.version`, a `workerVersion` per rollout: `main.js`,
  `main.js.map` and `superscalar_wasm_bg.wasm`, each a `contentFile` under the
  program's `artifacts/<server>/{bundle-digest}/` with its
  `{bundle-sha256:<file>}`, which the deploy pins; `mainModule: main.js`;
  `CompatibilityDate` unless `compatibilityDate` is set;
  `compatibilityFlags: ["nodejs_als"]`, which the runtime's
  `./workers-telemetry` needs for `AsyncLocalStorage`; `cpuMs` and
  `placement` from the settings; the config as bindings; and the `OTEL_`
  bindings when the server exports;
- `<server>.deployment`, a `workersDeployment` sending 100% to the version;
- for an exposed server under a domain, `<server>.domain`, a
  `workersCustomDomain` for `<server>.<domain>` in the exposure step.

| Setting | Type | |
| --- | --- | --- |
| `logs` | boolean | Workers Logs, on by default |
| `compatibilityDate` | `YYYY-MM-DD` | the runtime's compatibility date, `CompatibilityDate` by default |
| `cpuMs` | integer, 1 to 300000 | `limits.cpuMs` |
| `placement` | `"smart"` | Smart Placement |
| `telemetry` | boolean | trace export, the environment's by default |

Config is bindings: `plain_text` for a literal, a parameter and each
derived variable; `secret_text`, its text a reference to the output, for a
secret member such as an edge's signing key; `secrets_store_secret` for a
`Secret<T>` field, naming `secretsStoreId` and `SecretName`; and the `d1`,
`service` or `r2_bucket` binding a derived value names
(`d1: {binding, database}`, `binding: {name, worker}`, or a bucket's
`binding`). The platform refuses, as unrealizable, a server in another
language, a Worker name over 63 characters, and an API over the user model,
whose identity runtime needs `node:crypto` (`NoIdentity`); and it refuses
an exposed server without a domain while the environment records no
`workersSubdomain`, or a secret while it records no `secretsStoreId`,
saying to run bootstrap.

The bundle store keeps a bundle's files in `superschematic-state` under
`superschematic/bundles/<stack>/<deployable>/<hex>/`, each with its
SHA-256 and mode as metadata and a marker written last, and fetches them
back, checked, for a plan, a `--bundle` rollback or a `--no-build` deploy.

## Calls between Workers

`cloudflare.workers-workers` realizes an http edge from a Worker to a
Worker (section 18.10). It returns the edge's key, the generic
connector's `tls:index/privateKey:PrivateKey` node
`<caller>.calls.<callee>.key` (D63), whose type the core registers, and
derives:

```json
{
  "url": "https://shop-pricing",
  "binding": { "name": "SHOP_PRICING_SERVICE", "worker": { "$output": { "resource": "shop-pricing.worker", "name": "name" } } },
  "credential": {
    "source": "signed-token", "issuer": "shop-storefront", "audience": "shop-pricing",
    "key": { "$output": { "resource": "shop-storefront.calls.shop-pricing.key", "name": "privateKeyPemPkcs8" } }
  }
}
```

The platform writes the `service` binding the value names, and the key as
a `secret_text` binding. The callee's callers field gets the caller as an
issuer with the key's `publicKeyPem`. The caller's `worker` node depends
on the callee's, so a destroy removes the caller first, and the waves
roll the callee out first. A call between two APIs one server serves
derives `{"url": "https://<server>"}` alone and stays in process; when the
callee has a service clause, the client sends the server's loopback
credential, which `fetch.ts` generates within the isolate's build.

## Buckets on R2

`cloudflare.r2` lowers a bucket to `<bucket>.bucket`, an
`r2Bucket` named `<stack>-<env>-<bucket>` with a suffix per parameter, and with
`deleteAfterDays` to `<bucket>.lifecycle`, an `r2BucketLifecycle` whose
rule deletes each object that many days after it was written.

| Setting | Type | |
| --- | --- | --- |
| `locationHint` | `wnam`, `enam`, `weur`, `eeur`, `apac`, `oc` | the bucket's `location` |
| `storageClass` | `Standard`, `InfrequentAccess` | the class of new objects |
| `deleteAfterDays` | integer, at least 1 | the lifecycle rule |
| `jurisdiction` | `eu`, `fedramp` | the bucket's and the lifecycle's `jurisdiction`; refused beside `locationHint` |

`cloudflare.workers-r2` realizes a bucket edge from a Worker: it mints
`<caller>.r2.<bucket>.token`, an `accountToken` named
`<stack>-<env>-<caller>-<bucket>` whose one policy allows
Workers R2 Storage Bucket Item Write (`R2ObjectsWriteGroup`, the id
Cloudflare publishes, which bootstrap checks against the account's list)
on `com.cloudflare.edge.r2.bucket.<account>_<jurisdiction>_<bucket>`
(`default` unless the bucket names one), and derives
`{name, binding, r2: {account, jurisdiction?, accessKeyId, token}}`, the
bucket's name, the `r2_bucket` binding's name, the bucket's jurisdiction,
whose S3 host, `<account>.<jurisdiction>.r2.cloudflarestorage.com`, the
runtime signs URLs for, and the token's `id` and `value`, the value a
`secret_text` binding. `buckets-never-public` refuses an
`r2ManagedDomain` or an `r2CustomDomain` on a Bucket service's bucket.
The target's `PreDestroy` empties each bucket of a member of a
parameterized environment through `Cloud`'s S3 calls before its destroy,
at its jurisdiction's S3 host (`JurisdictionCloud`).

## Jobs on Cron Triggers

`cloudflare.cron` places a TypeScript job, its artifact a bundle built
from the generated `scheduled.ts` (section 18.9 of the design). It lowers
a job to its Worker's three nodes, as a server's, with workers.dev off,
its version binding `SUPERSCHEMATIC_BUNDLE` (`BundleBinding`) to
`{bundle-digest}`, which the deploy pins, and:

- `<job>.workflow`, a `cloudflare:index/workflow:Workflow` named as the
  Worker, of the class `JobRun` (`WorkflowClass`) that `scheduled.ts`
  exports, on the Worker's name, after the deployment;
- for a schedule the environment runs, `<job>.schedule`, a
  `cloudflare:index/workersCronTrigger:WorkersCronTrigger` on the
  Worker's name with the job's schedule, after the deployment.

A job's edges are its API's: `cloudflare.cron-workers`,
`cloudflare.cron-r2` and `cloudflare.cron-d1` derive them with the
server's connect functions, so the job gets a service binding and a key
of its own per callee, an `r2_bucket` binding and a token of its own per
bucket, and a d1 binding; its `worker` node depends on each Worker it
binds (`bindingDependencies`).

| Setting | Type | |
| --- | --- | --- |
| `logs` | boolean | Workers Logs, on by default |
| `cpuMs` | integer, 1 to 300000 | `limits.cpuMs` |
| `telemetry` | boolean | trace export, the environment's by default |

It refuses, as unrealizable, a job in another language, a Worker name
over 63 characters, a schedule in a time zone other than UTC, and a
timeout times tries over `MaxScheduledRunSeconds`, 15 minutes, since
`scheduled.ts` runs every try of a firing within one run.

The target's `Jobs` seam (`jobs.go`) runs a job once, for `stack run` and
for a job with `onDeploy`: it starts an instance of the job's Workflow
(`Cloud.CreateWorkflowInstance`, `POST
/accounts/<id>/workflows/<name>/instances`) with the run's token, its
payload `{"bundle": "<hex>"}`, the bundle the deploy manifest records or
the rollout pinned, and reads it (`Cloud.WorkflowInstance`) every two
seconds until it ends. `JobRun` refuses a bundle other than its version's
`SUPERSCHEMATIC_BUNDLE`, then runs the job's tries in one step; the runner
returns the error an instance that errored threw, which names the last
try's, and says to deploy when the Workflow is not there.

## Sites on Workers

`cloudflare.sites` places a site (section 18.8 of the design). It lowers
a site to its Worker's three nodes, workers.dev on without a domain,
whose version holds:

- `assets`, the directory `artifacts/<site>/{site-digest}`, which the
  deploy pins and fills, with `htmlHandling` and `notFoundHandling`
  `none` and the script first under `/__superschematic/`;
- `site.js` as its one module, inline: `SiteScript`, the bytes the
  extension embeds;
- the bindings `ASSETS`, `SITE_CONFIG`, the JSON of section 8.10 with each
  API's public address, a string or a concatenation with the
  environment's parameters, and `SITE_FALLBACK` when the site has one;

and under a domain `<site>.domain`, a `workersCustomDomain` for
`<site>.<domain>`. `cloudflare.site-workers` gives a site edge to a
server on Workers the server's public address. The site store keeps a
build's files under `superschematic/sites/<stack>/<site>/<hex>/` as the
bundle store keeps a bundle's, writes no config, and fetches them back
for the deploy to place.

## Linking it

A distribution links the extension beside its target and the pulumi
provisioner, and pins the provider for the `cloudflare` package. The
installed binary, `cmd/superschematic`, links it this way, beside the gcp
target:

```go
cli.New(cli.Config{Name: "superschematic"},
	gcp.Extension{},
	cloudflare.Extension{},
	pulumi.Extension{ProviderVersions: map[string]string{
		"gcp":              gcp.ProviderVersion,
		cloudflare.Package: cloudflare.ProviderVersion,
		// The generic connector's key node (D63), each call's key.
		stack.GenericKeyPackage: stack.GenericKeyProviderVersion,
	}},
)
```

## Pinned schemas

`schemas/pulumi-cloudflare.json` pins pulumi-cloudflare 6.22.0: the
digest of each upstream file and the types the DNS platform and the
target emit: the DNS record, the account token of bootstrap's graph and
of a bucket edge, a Worker's `worker`, `workerVersion`,
`workersDeployment`, `workersCustomDomain` and `workersCronTrigger`, a
job's `workflow`, a bucket's `r2Bucket` and `r2BucketLifecycle`, and the
D1 database, `d1Database`. A call's key node is the core's `tls` type, which the
generic connector pins.
`schemas/` holds one file per type, with its Terraform name and renames
(section 6.4), and the DNS platform and the target register each as the
JSON Schema of its type's properties, so resolution checks every node
offline. The tool is
the gcp target's, shared through `stack/providerschema/pintool`:

```bash
cd extensions/cloudflare
go run ./internal/tools/providerschemas            # write schemas/
go run ./internal/tools/providerschemas -check     # fail on drift (CI)
go run ./internal/tools/providerschemas -version X.Y.Z  # move the pin
```

Moving the pin means moving `ProviderVersion` with it; `Register` refuses
a pin at another version.

## Tests

`go test ./...` resolves the `stack/stacktest` shop stack with its domains
on Cloudflare, DNS-only in Staging and Preview and proxied in Production,
the Workers fixture `workers-stack` and the calls fixture `calls-stack`, a
storefront Worker calling an internal pricing Worker and listing a bucket,
in Staging, Production and Preview, and acme-shop's `storefront-stack`,
against the golden files under `testdata/golden/stack`, each node checked
against its pinned type; deploys the storefront with the bundle store over
the fake `Cloud`, and a member of `calls-stack`'s Preview, whose destroy
empties its bucket first; holds the HTTP runtime's workerd `calls`
fixture to `calls-stack`'s Staging bindings; and checks the
bootstrap graph (`testdata/golden/bootstrap`) and a cloudflare
environment's workflow (`testdata/golden/ci`, through actionlint when it is
installed). It resolves `reviews-stack`, whose databases are on D1, in
Staging, Preview and Production, and checks its workflow, whose deploy
jobs install `superschematic-migrate`. It resolves the web fixture
`web-stack`, a server, its job and a site, in the same three
environments, deploys its site with the site store, checks its workflow,
and gives a job of the calls and D1 fixtures their edges.
The job runner runs a fake Workflow through `stack run` and a deploy's
jobs step, and reports an errored, a terminated and a missing one;
the telemetry tests read the `OTEL_` bindings, the compatibility flag
and the refusals; and `TestCloudS3InAJurisdiction` signs object calls for
`<account>.eu.r2.cloudflarestorage.com` against a TLS stand-in that signs
them again from the Host it received.
`TestSiteScriptUnderWorkerd` runs `site.js` under the HTTP runtime's
pinned workerd (`testdata/workerd/site`), which
`bun install --frozen-lockfile` in `runtime/http/typescript/test/workerd`
installs; it skips without it, unless `SUPERSCHEMATIC_REQUIRE_TS_CHECKS`
is set. Bootstrap, the stores, the CI identity and the migration runner
run against a fake `Cloud`, `gh` and provisioner, the runner with a
stand-in for `superschematic-migrate`, and `Cloud` itself against a local
stand-in for the REST and S3 APIs. Run it with `-update` to rewrite the golden
files, and review the diff. No test calls Cloudflare.
