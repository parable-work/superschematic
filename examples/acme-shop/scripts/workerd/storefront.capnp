# storefront-stack's Staging under workerd (D64, docs/stack-model.md,
# section 18.5): the storefront's Worker runs the bundle its generated
# bundle.ts writes, main.js and superscalar's WebAssembly module, with the
# bindings its Worker's version holds, none, since its config is empty; and
# a tester Worker reaches it through a service binding, as the internet
# reaches it at workers.dev. scripts/check.sh copies the bundle and
# tester.js beside this file and runs `workerd test` on it.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "shop-storefront", worker = .storefront),
    (name = "tester", worker = .tester),
  ],
);

const storefront :Workerd.Worker = (
  modules = [
    (name = "main.js", esModule = embed "main.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  compatibilityDate = "2026-09-15",
);

const tester :Workerd.Worker = (
  modules = [(name = "tester.js", esModule = embed "tester.js")],
  bindings = [(name = "STOREFRONT", service = "shop-storefront")],
  compatibilityDate = "2026-09-15",
);
