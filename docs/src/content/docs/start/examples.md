---
title: Examples
description: The example projects in the repository, what each one shows, and the script that builds and tests it.
sidebar:
  order: 5
---

Each example is a project in the repository that CI builds and tests end
to end. Run its script from a checkout that `make setup` has prepared
([Prerequisites](/superschematic/start/prerequisites/)).

| Example | What it shows | Run it |
| --- | --- | --- |
| [`examples/acme-shop`](https://github.com/parable-work/superschematic/tree/main/examples/acme-shop) | The tutorial's shop on the core binary: Go, Rust and TypeScript servers, clients in all four languages, jobs, a queue and its worker, a bucket, a static site, a Topcoat app, and a stack that `stack dev` runs locally | `examples/acme-shop/scripts/check.sh` |
| [`examples/acme-schematic`](https://github.com/parable-work/superschematic/tree/main/examples/acme-schematic) | The same shop with an extension that adds a kind, decorators, a generator, an auth provider, a behavior and commands, without editing the core | `examples/acme-schematic/scripts/smoke.sh` |
| [`examples/engine-notes`](https://github.com/parable-work/superschematic/tree/main/examples/engine-notes) | A notes server on the engine: workflow, comments and revisions over HTTP, an event stream and MCP | `examples/engine-notes/scripts/check.sh` |
| [`examples/engine-jobs`](https://github.com/parable-work/superschematic/tree/main/examples/engine-jobs) | A job runner on the engine and its work-queue behaviors: workers on the package's `QueueWorker` that claim jobs under leases over HTTP and beat their presence, retries, budgets, a cancel sent as a directive, a missed worker's jobs put back, and batches whose steps run in order and settle them | `examples/engine-jobs/scripts/check.sh` |
| [`examples/deploy-smoke`](https://github.com/parable-work/superschematic/tree/main/examples/deploy-smoke) | A project shaped like a finances app, which a maintainer deploys to Google Cloud or Cloudflare and checks through the site's origin: cookie sign-in, a declared query, `nearest` and a queue a TypeScript worker drains, with a site and its API on one origin. CI builds it and holds its cloud environments to goldens; the deploy runs by hand | `examples/deploy-smoke/scripts/smoke.sh Dev` |
| [`examples/migrations`](https://github.com/parable-work/superschematic/tree/main/examples/migrations) | Two versions of a DB service: `migrate plan` between them for Postgres and SQLite, its phases and hazards, and `superschematic-migrate` applying it to a database with rows | `examples/migrations/scripts/check.sh` |
| [`extensions/topcoat`](https://github.com/parable-work/superschematic/tree/main/extensions/topcoat) | A Go module of its own: for a Rust API, a crate a [Topcoat](https://github.com/tokio-rs/topcoat) app calls the operations through in-process, by each route's rules, with records of their results | `cd extensions/topcoat && go test ./...` |

## Runtime references

The generated code and the engine link runtimes, and each one has its own
README:

- [schema runtime](https://github.com/parable-work/superschematic/blob/main/runtime/schema/README.md), which generated types link
- HTTP runtime for [Go](https://github.com/parable-work/superschematic/blob/main/runtime/http/go/README.md),
  [Rust](https://github.com/parable-work/superschematic/blob/main/runtime/http/rust/README.md) and
  [TypeScript](https://github.com/parable-work/superschematic/blob/main/runtime/http/typescript/README.md), which generated servers link
- [engine](https://github.com/parable-work/superschematic/blob/main/runtime/engine/README.md) and its
  [work queue](https://github.com/parable-work/superschematic/blob/main/runtime/engine-workqueue/README.md)
- [version graph](https://github.com/parable-work/superschematic/blob/main/runtime/versiongraph/README.md), with its
  [TypeScript](https://github.com/parable-work/superschematic/blob/main/runtime/versiongraph/typescript/README.md),
  [Rust engine](https://github.com/parable-work/superschematic/blob/main/runtime/versiongraph/rust-engine/README.md) and
  [Python](https://github.com/parable-work/superschematic/blob/main/runtime/versiongraph/python/README.md) packages
