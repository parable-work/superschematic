# ts-till-stack's server Till under workerd (D64, D37 amended;
# docs/stack-model.md, sections 9.9 and 18.2): a Worker runs the bundle its
# generated bundle.ts writes from fetch.ts, with the bindings Edge gives
# Till: ts-pricing's callers field, which admits no issuer, and
# ts-checkout's endpoint of ts-pricing, Till's own address, with no binding
# and no credential. The tester (tester.js) calls Till through a service
# binding. TestTypeScriptWorkersCallAnAPIItServesWithItsLoopbackKey copies
# the bundle and tester.js beside this file and runs `workerd test`.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "till", worker = .till),
    (name = "tester", worker = .tester),
  ],
);

const till :Workerd.Worker = (
  modules = [
    (name = "main.js", esModule = embed "main.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "TS_PRICING_CALLERS_ISSUERS", text = "0"),
    (name = "TS_PRICING_SERVICE_URL", text = "https://Till"),
  ],
  compatibilityDate = "2026-09-15",
);

const tester :Workerd.Worker = (
  modules = [(name = "tester.js", esModule = embed "tester.js")],
  bindings = [(name = "TILL", service = "till")],
  compatibilityDate = "2026-09-15",
);
