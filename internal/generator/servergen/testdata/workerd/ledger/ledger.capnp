# ts-ledger-stack under workerd (D92, docs/stack-model.md, section 18): the
# server ts-ledger and its two workers, each a Worker that runs the bundle
# its generated bundle.ts writes, main.js and superscalar's WebAssembly
# module, with the bindings the cloudflare target gives its version: the
# database's hyperdrive binding, named after its field, and its two
# plain_text variables, and a worker's WORKER_CONCURRENCY and
# WORKER_DRAIN_SECONDS. workerd's hyperdrive binding is Hyperdrive's local
# form: a connection string whose host workerd routes to the service the
# binding names, here the Postgres the test reaches, $PG_ADDRESS, in a
# database of its own. The site ts-ledger-web, whose origin serves
# ts-ledger (D91), is a Worker that runs the script the cloudflare
# extension embeds (extensions/cloudflare/site.js), with the bindings the
# cloudflare.sites platform gives its version: its config and SITE_ORIGIN,
# which name ts-ledger under /api/ts-ledger on the site's own address, and
# SITE_API_TS_LEDGER, a service binding of the server's Worker, beside a
# stand-in for its static assets (assets.js). The tester (tester.js) calls
# the server through a service binding, and through the site's Worker as a
# browser on the site's origin does, and fires each worker's scheduled
# handler, as its Cron Trigger fires it. TestTypeScriptHyperdriveUnderWorkerd
# copies the bundles, superscalar's module, the site's script, assets.js and
# tester.js beside this file, fills in the database, and runs `workerd
# test`. The cloudflare extension's TestWorkerdLedgerFixtureHoldsTheGoldenBindings
# holds each Worker's bindings to its version's.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "ledger", worker = .ledgerWorker),
    (name = "embed", worker = .embedWorker),
    (name = "sync", worker = .syncWorker),
    (name = "site", worker = .siteWorker),
    (name = "assets", worker = .assetsWorker),
    (name = "postgres", external = (address = "$PG_ADDRESS", tcp = ())),
    (name = "tester", worker = .testerWorker),
  ],
);

const ledgerWorker :Workerd.Worker = (
  modules = [
    (name = "main.js", esModule = embed "ts-ledger/main.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "TS_LEDGER_DB_DATABASE", hyperdrive = (designator = "postgres", database = "$PG_DATABASE", user = "$PG_USER", password = "$PG_PASSWORD", scheme = "postgresql")),
    (name = "TS_LEDGER_DB_DATABASE_HYPERDRIVE_BINDING", text = "TS_LEDGER_DB_DATABASE"),
    (name = "TS_LEDGER_DB_DATABASE_HYPERDRIVE_CONFIG", text = "a76a99bc342644deb02c38d66082262a"),
  ],
  compatibilityDate = "2026-09-15",
  compatibilityFlags = ["nodejs_als"],
);

const embedWorker :Workerd.Worker = (
  modules = [
    (name = "main.js", esModule = embed "ts-ledger-embed-line-items/main.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "TS_LEDGER_DB_DATABASE", hyperdrive = (designator = "postgres", database = "$PG_DATABASE", user = "$PG_USER", password = "$PG_PASSWORD", scheme = "postgresql")),
    (name = "TS_LEDGER_DB_DATABASE_HYPERDRIVE_BINDING", text = "TS_LEDGER_DB_DATABASE"),
    (name = "TS_LEDGER_DB_DATABASE_HYPERDRIVE_CONFIG", text = "a76a99bc342644deb02c38d66082262a"),
    (name = "WORKER_CONCURRENCY", text = "2"),
    (name = "WORKER_DRAIN_SECONDS", text = "3"),
  ],
  compatibilityDate = "2026-09-15",
  compatibilityFlags = ["nodejs_als"],
);

const syncWorker :Workerd.Worker = (
  modules = [
    (name = "main.js", esModule = embed "ts-ledger-sync-items/main.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "TS_LEDGER_DB_DATABASE", hyperdrive = (designator = "postgres", database = "$PG_DATABASE", user = "$PG_USER", password = "$PG_PASSWORD", scheme = "postgresql")),
    (name = "TS_LEDGER_DB_DATABASE_HYPERDRIVE_BINDING", text = "TS_LEDGER_DB_DATABASE"),
    (name = "TS_LEDGER_DB_DATABASE_HYPERDRIVE_CONFIG", text = "a76a99bc342644deb02c38d66082262a"),
    (name = "WORKER_CONCURRENCY", text = "2"),
    (name = "WORKER_DRAIN_SECONDS", text = "6"),
  ],
  compatibilityDate = "2026-09-15",
  compatibilityFlags = ["nodejs_als"],
);

const siteWorker :Workerd.Worker = (
  modules = [(name = "site.js", esModule = embed "site.js")],
  bindings = [
    (name = "ASSETS", service = "assets"),
    (name = "SITE_CONFIG", text = "{\"apis\":{\"ts-ledger\":{\"url\":\"https://ts-ledger-stack-edge-ts-ledger-web.acme.workers.dev/api/ts-ledger\"}}}"),
    (name = "SITE_FALLBACK", text = "index.html"),
    (name = "SITE_ORIGIN", text = "[{\"path\":\"/api/ts-ledger\",\"binding\":\"SITE_API_TS_LEDGER\"}]"),
    (name = "SITE_API_TS_LEDGER", service = "ledger"),
  ],
  compatibilityDate = "2026-09-15",
);

const assetsWorker :Workerd.Worker = (
  modules = [(name = "assets.js", esModule = embed "assets.js")],
  compatibilityDate = "2026-09-15",
);

const testerWorker :Workerd.Worker = (
  modules = [(name = "tester.js", esModule = embed "tester.js")],
  bindings = [
    (name = "LEDGER", service = "ledger"),
    (name = "EMBED", service = "embed"),
    (name = "SYNC", service = "sync"),
    (name = "SITE", service = "site"),
    (name = "SITE_URL", text = "https://ts-ledger-stack-edge-ts-ledger-web.acme.workers.dev"),
  ],
  compatibilityDate = "2026-09-15",
);
