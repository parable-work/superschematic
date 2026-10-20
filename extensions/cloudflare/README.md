# extensions/cloudflare

The Cloudflare extension of the stack model
([docs/stack-model.md](../../docs/stack-model.md), sections 6.9 and 18,
D64). It registers the DNS platform `cloudflare`, which writes an
environment's domain records into a Cloudflare zone, whatever target runs
the environment's servers, and the target `cloudflare`, which runs an
environment on one Cloudflare account. The target's values, its state in
R2, its secrets in the Secrets Store, its bootstrap, its CI identity, its
D1 platform and its D1 migrations are built; its Workers, Cron, R2 and
site platforms come next, in this module.

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
| `d1.go` | the `cloudflare.d1` platform: a `d1Database` per hosted DB schema, `<stack>-<db>` with a suffix per parameter, in `sqlite`, with `locationHint` or `jurisdiction` |
| `migrations.go` | the migration runner: `superschematic-migrate job` on the deploying machine, against `d1://<accountId>/<uuid>`, each uuid looked up by name |
| `policy.go` | `domain-on-cloudflare` |

Every command reads the token from `CLOUDFLARE_API_TOKEN` and the state's
passphrase from `PULUMI_CONFIG_PASSPHRASE`. On the cloudflare target the
DNS platform names no credential: the run's token carries the zone.

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
target emit: the DNS record, the account token of bootstrap's graph,
and the D1 database.
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
against the golden files under `testdata/golden/stack`, and checks the
bootstrap graph (`testdata/golden/bootstrap`) and a cloudflare
environment's workflow (`testdata/golden/ci`, through actionlint when it is
installed). It resolves `reviews-stack`, whose databases are on D1, in
Staging, Preview and Production, and checks its workflow, whose deploy
jobs install `superschematic-migrate`. Bootstrap, the stores, the CI
identity and the migration runner run against a fake `Cloud`, `gh` and
provisioner, the runner with a stand-in for `superschematic-migrate`, and
`Cloud` itself against a local stand-in for the REST and S3 APIs. Run it with `-update` to rewrite the golden
files, and review the diff. No test calls Cloudflare.
