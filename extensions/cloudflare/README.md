# extensions/cloudflare

The Cloudflare extension of the stack model
([docs/stack-model.md](../../docs/stack-model.md), sections 6.9 and 18,
D64). It registers the DNS platform `cloudflare`, which writes an
environment's domain records into a Cloudflare zone, whatever target runs
the environment's servers, and the target `cloudflare`, which runs an
environment on one Cloudflare account. The target's values, its state in
R2, its secrets in the Secrets Store, its bootstrap, its CI identity, its
Workers platform, which runs TypeScript servers as Workers from the
bundles it keeps in R2, its Cron platform, which runs TypeScript jobs as
Workers under Cron Triggers, and its site platform, which serves sites as
Workers with static assets kept in R2, are built; its D1 and R2 platforms
come next, in this module.

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

| File | |
| --- | --- |
| `cloudflare.go`, `target.go` | the registration, the values, the state's layout in R2 (`StateBucket`, `StateURL`) |
| `cloud.go`, `sigv4.go` | `Cloud`, what the target calls on Cloudflare outside the provisioner, over the REST API and R2's S3 API with plain `net/http` and SigV4; tests pass a fake |
| `state.go` | the state store: Pulumi's S3 backend on R2, the passphrase secrets provider, and the manifests with a history copy |
| `secrets.go` | the secret store over the Secrets Store, `<Stack>-<Environment>-<Type>-<FIELD>` |
| `bootstrap.go`, `github.go` | bootstrap: the state bucket, the passphrase and its check, the Secrets Store, the deployer and planner account tokens, and `--github` through `gh` |
| `ci.go` | the CI identity, `cloudflare-api-token`, naming the GitHub secrets each job reads |
| `workers.go`, `names.go` | the `cloudflare.workers` platform: a TypeScript server as a Worker, its version and its deployment, its config as bindings, its custom domain; names suffixed per member and refused over 63 characters |
| `bundles.go` | the bundle store: each bundle's files in the state bucket under their digest, and back |
| `cron.go` | the `cloudflare.cron` platform: a TypeScript job as a Worker and its Cron Trigger, in UTC, within Cloudflare's 15 minutes for a scheduled run |
| `sites.go`, `site.js` | the `cloudflare.sites` platform: a site as a Worker with static assets and the script it embeds; the `cloudflare.site-workers` connector; and the site store, each build's files in the state bucket under their digest, and back |
| `policy.go` | `domain-on-cloudflare` and `nothing-public-unless-exposed` |

Every command reads the token from `CLOUDFLARE_API_TOKEN` and the state's
passphrase from `PULUMI_CONFIG_PASSPHRASE`. On the cloudflare target the
DNS platform names no credential: the run's token carries the zone.

## Servers on Workers

`cloudflare.workers` places a TypeScript server, its artifact a bundle
(D64, section 18.5 of the design). It lowers a server to:

- `<server>.worker`, a `cloudflare:index/worker:Worker` in infrastructure
  named after the deployable, with Workers Logs on unless `logs: false`,
  workers.dev on only for an exposed server without a domain, previews off;
- `<server>.version`, a `workerVersion` per rollout: `main.js`,
  `main.js.map` and `superscalar_wasm_bg.wasm`, each a `contentFile` under the
  program's `artifacts/<server>/{bundle-digest}/` with its
  `{bundle-sha256:<file>}`, which the deploy pins; `mainModule: main.js`;
  `CompatibilityDate` unless `compatibilityDate` is set; `cpuMs` and
  `placement` from the settings; and the config as bindings;
- `<server>.deployment`, a `workersDeployment` sending 100% to the version;
- for an exposed server under a domain, `<server>.domain`, a
  `workersCustomDomain` for `<server>.<domain>` in the exposure step.

| Setting | Type | |
| --- | --- | --- |
| `logs` | boolean | Workers Logs, on by default |
| `compatibilityDate` | `YYYY-MM-DD` | the runtime's compatibility date, `CompatibilityDate` by default |
| `cpuMs` | integer, 1 to 300000 | `limits.cpuMs` |
| `placement` | `"smart"` | Smart Placement |

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

## Jobs on Cron Triggers

`cloudflare.cron` places a TypeScript job, its artifact a bundle built
from the generated `scheduled.ts` (section 18.9 of the design). It lowers
a job to its Worker's three nodes, as a server's, with workers.dev off,
and for a schedule the environment runs:

- `<job>.schedule`, a `cloudflare:index/workersCronTrigger:WorkersCronTrigger`
  on the Worker's name with the job's schedule, after the deployment.

| Setting | Type | |
| --- | --- | --- |
| `logs` | boolean | Workers Logs, on by default |
| `cpuMs` | integer, 1 to 300000 | `limits.cpuMs` |

It refuses, as unrealizable, a job in another language, a Worker name
over 63 characters, a schedule in a time zone other than UTC, and a
timeout times tries over `MaxScheduledRunSeconds`, 15 minutes, since
`scheduled.ts` runs every try of a firing within one run. The target has
no `Jobs` seam, so `stack run` refuses it.

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
	}},
)
```

## Pinned schemas

`schemas/pulumi-cloudflare.json` pins pulumi-cloudflare 6.22.0: the
digest of each upstream file and the types the DNS platform and the
target emit: the DNS record, the account token of bootstrap's graph, and
a Worker's `worker`, `workerVersion`, `workersDeployment`,
`workersCustomDomain` and `workersCronTrigger`.
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
the Workers fixture `workers-stack` and the web fixture `web-stack`, a
server, its job and a site, in Staging, Production and Preview, and
acme-shop's `storefront-stack`, against the golden files under
`testdata/golden/stack`, each node checked against its pinned type; deploys
the storefront with the bundle store, and the web fixture's site with the
site store, over the fake `Cloud`; and checks the bootstrap graph
(`testdata/golden/bootstrap`) and the workflows of cloudflare environments
(`testdata/golden/ci`, through actionlint when it is installed).
`TestSiteScriptUnderWorkerd` runs `site.js` under the HTTP runtime's
pinned workerd (`testdata/workerd/site`), which
`bun install --frozen-lockfile` in `runtime/http/typescript/test/workerd`
installs; it skips without it, unless `SUPERSCHEMATIC_REQUIRE_TS_CHECKS`
is set. Bootstrap, the stores and the CI identity run against a fake
`Cloud`, `gh` and provisioner, and `Cloud` itself against a local stand-in
for the REST and S3 APIs. Run it with `-update` to rewrite the golden
files, and review the diff. No test calls Cloudflare.
