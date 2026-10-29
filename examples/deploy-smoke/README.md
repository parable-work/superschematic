# deploy-smoke

A small project shaped like a finances app, which a maintainer deploys to
Google Cloud or to Cloudflare to prove there what the tests prove only
under fakes and the pinned workerd (D91, D92, D97): one origin for a site
and the API it calls, Hyperdrive, a queue drained by a Cron Trigger, and
cookie sign-in on Workers. It builds with the installed binary,
`bin/superschematic` (`make cli`), which links the gcp, cloudflare and
pulumi extensions.

| Path | What it is |
|---|---|
| `schemas/services/smoke-db` | DB, Postgres alone: the user model and its seeded `member` role (D50, D88), `LineItem` with a three-dimension vector column and an hnsw index (D84), the keyed queue `DrainLineItem`, one message waiting and one claim per line item (`serialize`, D80), and the declared query `lineItemTotals` (D86), verified into `queries.lock` |
| `schemas/services/smoke-api` | API over `smoke-db`, served in TypeScript: approval-gated registration with cookie sessions, the first registrant the administrator, and user administration; line items written with their name's embedding, the declared query, `nearest`, and an enqueue in a transaction; the worker `DrainLineItems`; and the job `SyncAllLinkedAccounts`, whose deployable, `smoke-api-sync-all-linked-accounts`, is longer than a GCP service account's id, so gcp names it short (D97) |
| `schemas/services/smoke-web` | Site: a page that says where `smoke-api` is on its origin and who is signed in, with no SDK in its bundle |
| `schemas/services/smoke-stack` | Stack: `smoke-api` and the site on one origin (`origins`, D91); `Dev` on the local target, `Gcp` on gcp and `Cloudflare` on cloudflare, whose project, region, account and Hyperdrive config are placeholders that resolve offline |
| `typescript/smoke-api` | `smoke-api`'s implementation; `embed.ts` stands in for an embedding model |
| `web/smoke-web` | the site's page |
| `scripts/smoke.sh` | deploys an environment, checks it and destroys it; `Dev` runs `stack dev` instead |
| `scripts/smoke-check.ts` | the checks, through the site's origin, on Bun |
| `testdata/` | `Gcp`'s and `Cloudflare`'s `environment.json`, which `cmd/superschematic`'s `TestDeploySmokeEnvironments` builds and holds to |

## Run it

```sh
make cli                                      # from the checkout's root
examples/deploy-smoke/scripts/smoke.sh Dev    # stack dev: Docker, Bun
```

`smoke.sh Dev` runs `stack dev`, waits for it, runs the checks against the
site's address and stops it, removing its database. The checks, each
through the site's origin as the site's page would make them:

1. the site's page, its fallback for a path of no file, and its config,
   which puts `smoke-api` at `/api/smoke-api` on the same origin, where
   `smoke-api` answers;
2. a sign-in that sends `Origin` alone, as every unsafe request here
   does, taken as same-origin: the identity runtime does so only when the
   `Host` it sees is the site's, so the front passed it on;
3. the administrator registered as the first user, or signed in on a
   database an earlier run left; a member registered, refused until the
   administrator approves them, then signed in three times with a cookie;
   a sign-in from another origin refused;
4. the API's own 404, not the site's page, for a line item that is not;
5. three line items, the declared query adding them up, and `nearest`
   finding one nearest its own name;
6. a drain requested, and the line item polled until the worker drained
   it;
7. sign-out.

It prints each sign-in's time and the time from the enqueue to the
drain: on Cloudflare a sign-in hashes with the identity runtime's own
argon2id, about 120 ms of CPU, and the worker drains at the next Cron
firing, within a minute.

`smoke.sh Gcp` and `smoke.sh Cloudflare` write the account's values in
place of the placeholders, build every service, deploy, read the site's
address from the environment and `stack outputs`, check it, and destroy
it, the destroy also when a step fails; `--keep` leaves it up, and
`--destroy` removes it later. `CONTRIBUTING.md`, "A deploy to the cloud",
has what each target needs once, and `.github/workflows/deploy-smoke.yml`
runs the script by hand on GitHub Actions.
