# The refused fixture: a Worker whose D1 binding names no database.
# scripts/workerd-test.mjs expects workerd to fail to load it with the
# message in expected-failure.txt.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [(name = "refused", worker = .refused)],
);

const refused :Workerd.Worker = (
  modules = [
    (name = "worker.js", esModule = embed "worker.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [(name = "SHOP_DB_DATABASE_D1_BINDING", text = "SHOP_DB_DATABASE")],
  compatibilityDate = "2026-09-01",
);
