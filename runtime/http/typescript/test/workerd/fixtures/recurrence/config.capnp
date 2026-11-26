# The recurrence fixture (D152): the behaviors' core's wasm32 module, as
# @superschematic/rules ships it, runs a Recurrence's remind deadline in a
# Worker. scripts/workerd-test.mjs copies the module beside the bundle.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [(name = "recurrence", worker = .recurrence)],
);

const recurrence :Workerd.Worker = (
  modules = [
    (name = "worker.js", esModule = embed "worker.js"),
    (name = "superschematic_rules_wasm.wasm", wasm = embed "superschematic_rules_wasm.wasm"),
  ],
  compatibilityDate = "2026-09-01",
);
