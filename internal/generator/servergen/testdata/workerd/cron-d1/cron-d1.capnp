# ts-lite-stack's job PruneNotes under workerd (D64, docs/stack-model.md,
# section 18.9): a Worker runs the bundle its generated bundle.ts writes
# from scheduled.ts, with the bindings cloudflare.cron gives a job whose
# database is on D1: TS_NOTES_DB_DATABASE_D1_BINDING and _D1_DATABASE as
# text, and TS_NOTES_DB_DATABASE, the d1 binding, here a wrapped stand-in
# (d1.js), since workerd runs no D1. The tester (tester.js) fires the
# Worker's scheduled handler through a service binding, as its Cron
# Trigger fires it. TestTypeScriptSQLiteEntrypointCompilesAndServes copies
# the bundle, tester.js and d1.js beside this file and runs `workerd test`.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "prune", worker = .prune),
    (name = "tester", worker = .tester),
  ],
  extensions = [(
    modules = [(name = "superschematic-fixture:d1", esModule = embed "d1.js", internal = true)],
  )],
);

const prune :Workerd.Worker = (
  modules = [
    (name = "main.js", esModule = embed "main.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "TS_NOTES_DB_DATABASE_D1_BINDING", text = "TS_NOTES_DB_DATABASE"),
    (name = "TS_NOTES_DB_DATABASE_D1_DATABASE", text = "0b0c4d4e-8f9a-4b1c-9d2e-3f4a5b6c7d8e"),
    (name = "TS_NOTES_DB_DATABASE", wrapped = (moduleName = "superschematic-fixture:d1", entrypoint = "d1", innerBindings = [(name = "empty", text = "2")])),
  ],
  compatibilityDate = "2026-09-15",
);

const tester :Workerd.Worker = (
  modules = [(name = "tester.js", esModule = embed "tester.js")],
  bindings = [(name = "PRUNE", service = "prune")],
  compatibilityDate = "2026-09-15",
);
