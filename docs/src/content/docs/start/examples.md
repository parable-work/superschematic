---
title: Examples
description: The example projects in the repository, what each one shows, and the script or test that builds and runs it.
sidebar:
  order: 5
---

Each example is a project in the repository. CI's full tier, which runs
before every release and twice a day on `main`, runs the scripts of
acme-shop, acme-schematic, engine-notes, engine-jobs and migrations, and
Go tests that build or load every other one, as each row says. Run an
example's script, or the Go test that runs it, from a checkout that
`make setup` has prepared
([Prerequisites](/superschematic/start/prerequisites/)).

| Example | What it shows | Run it |
| --- | --- | --- |
| [`examples/acme-shop`](https://github.com/parable-work/superschematic/tree/main/examples/acme-shop) | The tutorial's shop on the installed binary: Go, Rust and TypeScript servers, clients in all four languages, jobs, queues and their workers, a bucket, a memory store, a static site, commerce on its tables' behaviors, a Topcoat app, and stacks that `stack dev` runs locally | `examples/acme-shop/scripts/check.sh` |
| [`examples/acme-schematic`](https://github.com/parable-work/superschematic/tree/main/examples/acme-schematic) | The same shop with an extension that adds a kind, decorators, a generator, an auth provider, a behavior and commands, without editing the core | `examples/acme-schematic/scripts/smoke.sh` |
| [`examples/engine-notes`](https://github.com/parable-work/superschematic/tree/main/examples/engine-notes) | A notes server on development mode, with no build: workflow, comments and revisions over HTTP, an event stream and MCP | `examples/engine-notes/scripts/check.sh` |
| [`examples/engine-jobs`](https://github.com/parable-work/superschematic/tree/main/examples/engine-jobs) | A job runner on development mode and the work-queue behaviors: workers that claim jobs under leases over HTTP and beat their presence, retries, budgets, a missed worker's jobs put back, and batches whose steps run in order and settle them | `examples/engine-jobs/scripts/check.sh` |
| [`examples/runtime-providers`](https://github.com/parable-work/superschematic/tree/main/examples/runtime-providers) | [Runtime providers](/superschematic/guides/runtime-providers/), each a package of its own: two SMS channel adapters an environment swaps by its config, a mail relay, a credential broker scoped to named vaults whose values reach only the runner, a local-process sandbox, a model stub and a file processor, with a stack whose environments name them. `make ts` runs its script; CI builds its stack in `cmd/superschematic`'s `TestRuntimeProvidersExampleStack` | `examples/runtime-providers/scripts/check.sh` |
| [`examples/embedded-notes`](https://github.com/parable-work/superschematic/tree/main/examples/embedded-notes) | A notes app as [one binary](/superschematic/guides/stacks/#build-one-binary) on the `embedded` target: two Rust servers, a worker and the site in one process, its database a SQLite file it migrates as it starts from the plans its releases recorded. CI builds and runs it in `cmd/superschematic`'s `TestEmbeddedNotesIsOneBinary` | `cd examples/embedded-notes && ../../bin/superschematic stack build Desktop` |
| [`examples/deploy-smoke`](https://github.com/parable-work/superschematic/tree/main/examples/deploy-smoke) | A project shaped like a finances app, which a maintainer deploys to Google Cloud or Cloudflare and checks through the site's origin: cookie sign-in, a declared query, `nearest` and a queue a TypeScript worker drains, with a site and its API on one origin; the behaviors, a recurrence's reminder, the notifier, search across tables and the inbound path, each through a stub channel adapter where it needs one. CI builds it and holds its cloud environments to goldens; the deploy runs by hand | `examples/deploy-smoke/scripts/smoke.sh Dev` |
| [`examples/migrations`](https://github.com/parable-work/superschematic/tree/main/examples/migrations) | Two versions of a DB service: `migrate plan` between them for Postgres and SQLite, its phases and hazards, and `superschematic-migrate` applying it to a database with rows | `examples/migrations/scripts/check.sh` |
| [`extensions/topcoat`](https://github.com/parable-work/superschematic/tree/main/extensions/topcoat) | A Go module of its own: for a Rust API, a crate a [Topcoat](https://github.com/tokio-rs/topcoat) app calls the operations through in-process, by each route's rules, with records of their results | `cd extensions/topcoat && go test ./...` |

## Platform features

These seven are a schemas root and the TypeScript runtime's wiring, each
for one feature of the TypeScript runtime on Postgres: six wire it by hand
in `server/server.ts`, and `agent-orchestration` has a stack whose
generated server does, with its implementation in `typescript/work-api`.
A Go test loads each example's schemas, and an
end-to-end test runs the same scenario, under fixture names, against
Postgres: set `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL` for the
`tsrestgen` tests and `SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL` for the
`tsgen` ones, and run them without `-short`.

| Example | What it shows | Loaded by | End to end |
| --- | --- | --- | --- |
| [`examples/access-records`](https://github.com/parable-work/superschematic/tree/main/examples/access-records) | [Per-record access](/superschematic/guides/api-routes/#per-record-access): rows each user sees or does not, a row rule along a path of relations, a party who reaches only what a grant allows until a date, a field revealed by permission, and the access log | `TestAccessRecordsExampleLoads` (`internal/loader/access`) | `TestGeneratedAccessEndToEnd` (`internal/generator/tsrestgen`) |
| [`examples/recurring-duties`](https://github.com/parable-work/superschematic/tree/main/examples/recurring-duties) | [Recurrence and the calendar](/superschematic/guides/database-tables/#a-rows-recurrence-and-the-calendar): duties due on a calendar rule, after they were last done or before an agreement ends, moved off weekends and holidays, and a calendar read and feed that show each person only what they may read | `TestRecurringDutiesExampleLoads` (`internal/loader`) | `TestGeneratedRecurrenceEndToEnd` (`internal/generator/tsrestgen`) |
| [`examples/notifications`](https://github.com/parable-work/superschematic/tree/main/examples/notifications) | [Events and notifications](/superschematic/guides/notifications/): routes as rows, each person's channels and quiet hours, escalation, replies by text or email that run as their sender, and password reset by email | `TestNotificationsExampleLoads` (`internal/loader/notifications`) | `TestNotificationsEndToEnd` (`internal/generator/tsgen`) |
| [`examples/references-files`](https://github.com/parable-work/superschematic/tree/main/examples/references-files) | [References and files on any row](/superschematic/guides/database-tables/#references-and-files-on-any-row): uploads straight to a bucket, a file filed onto a second row with no second upload, and backlinks held to what the reader may read | `TestReferencesFilesExampleLoads` (`internal/loader/rowrefs`) | `TestReferencesTablesEndToEnd` (`internal/generator/tsgen`) |
| [`examples/search-across-tables`](https://github.com/parable-work/superschematic/tree/main/examples/search-across-tables) | [Search across tables](/superschematic/guides/database-tables/#search-across-tables): one ranked search over projects, tasks and pages, each caller's access checked inside the query, and an agent run or a party that finds only what its grant reaches | `TestSearchAcrossTablesExampleLoads` (`internal/loader/search`) | `TestSearchTablesEndToEnd` (`internal/generator/tsgen`) |
| [`examples/parties`](https://github.com/parable-work/superschematic/tree/main/examples/parties) | [Outside parties](/superschematic/guides/parties/): contractors who send reports by text or email, one inbound path that decides who sent each message, email held to its SPF, DKIM and DMARC results, triage for unknown senders, and sending to a contact as a permission | `TestPartiesExampleLoads` (`internal/loader/conversations`) | `TestPartiesEndToEnd` (`internal/generator/tsgen`) |
| [`examples/agent-orchestration`](https://github.com/parable-work/superschematic/tree/main/examples/agent-orchestration) | [Agent runs](/superschematic/guides/agent-runs/): agents as rows, each run in a fresh sandbox with a token its task bounds, approvals before money moves, budgets that pause, one switch that stops every run, and a customer's text that starts one, on a generated server that builds the dispatcher | `TestAgentOrchestrationExampleLoads` (`internal/loader/agents`), `TestAgentOrchestrationExampleUsesTheGeneratedWiring` (`cmd/superschematic`) | `TestAgentRunsEndToEnd` (`internal/generator/tsgen`) |

## Runtime references

The generated code and the engine link runtimes, and each one has its own
README:

- [schema runtime](https://github.com/parable-work/superschematic/blob/main/runtime/schema/README.md), which generated types link
- HTTP runtime for [Go](https://github.com/parable-work/superschematic/blob/main/runtime/http/go/README.md),
  [Rust](https://github.com/parable-work/superschematic/blob/main/runtime/http/rust/README.md) and
  [TypeScript](https://github.com/parable-work/superschematic/blob/main/runtime/http/typescript/README.md), which generated servers link
- [engine](https://github.com/parable-work/superschematic/blob/main/runtime/engine/README.md), its
  [work queue](https://github.com/parable-work/superschematic/blob/main/runtime/engine-workqueue/README.md) and its
  [commerce package](https://github.com/parable-work/superschematic/blob/main/runtime/engine-commerce/README.md)
- the behaviors' core in [TypeScript](https://github.com/parable-work/superschematic/blob/main/runtime/rules/typescript/README.md)
  and [Go](https://github.com/parable-work/superschematic/blob/main/runtime/rules/go/README.md), and
  [development mode's handler](https://github.com/parable-work/superschematic/blob/main/runtime/rules-handler/typescript/README.md)
- the [migration runner](https://github.com/parable-work/superschematic/blob/main/runtime/migrate/README.md), which applies a plan
- [version graph](https://github.com/parable-work/superschematic/blob/main/runtime/versiongraph/README.md), with its
  [TypeScript](https://github.com/parable-work/superschematic/blob/main/runtime/versiongraph/typescript/README.md),
  [Rust engine](https://github.com/parable-work/superschematic/blob/main/runtime/versiongraph/rust-engine/README.md) and
  [Python](https://github.com/parable-work/superschematic/blob/main/runtime/versiongraph/python/README.md) packages
