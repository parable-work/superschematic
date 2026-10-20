# wishlist-stack's Staging under workerd (D64, docs/stack-model.md,
# sections 18.5 and 18.6): the wishlist's Worker runs the bundle its
# generated bundle.ts writes, main.js and superscalar's WebAssembly module,
# with the bindings its Worker's version holds: the two plain-text
# variables of its database field and the d1 binding they name. workerd
# runs no D1, so the binding is a wrapped stand-in (d1-binding.js) that
# answers the wishlist's statements over its table in memory. A tester
# Worker reaches the wishlist through a service binding, as the internet
# reaches it at workers.dev. scripts/check.sh copies the bundle and these
# files beside this one and runs `workerd test` on it.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "shop-wishlist", worker = .wishlist),
    (name = "tester", worker = .tester),
  ],
  extensions = [(
    modules = [(name = "acme-wishlist:d1", esModule = embed "d1-binding.js", internal = true)],
  )],
);

const wishlist :Workerd.Worker = (
  modules = [
    (name = "main.js", esModule = embed "main.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "SHOP_WISHLIST_DB_DATABASE_D1_BINDING", text = "SHOP_WISHLIST_DB_DATABASE"),
    (name = "SHOP_WISHLIST_DB_DATABASE_D1_DATABASE", text = "6f1e2d3c-4b5a-4968-8778-695a4b3c2d1e"),
    (name = "SHOP_WISHLIST_DB_DATABASE", wrapped = (moduleName = "acme-wishlist:d1", entrypoint = "d1")),
  ],
  compatibilityDate = "2026-09-15",
);

const tester :Workerd.Worker = (
  modules = [(name = "wishlist-tester.js", esModule = embed "wishlist-tester.js")],
  bindings = [(name = "WISHLIST", service = "shop-wishlist")],
  compatibilityDate = "2026-09-15",
);
