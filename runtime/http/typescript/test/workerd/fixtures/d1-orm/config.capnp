# The D1 repositories fixture (D182, D186): one Worker, whose test handler
# runs the generated repositories on SQLite of fixture-references-db,
# fixture-recurrence-db and fixture-search-db over a stand-in of D1, a
# Durable Object of the Worker's own on workerd's SQLite storage, in a file
# of the run's directory, with the behaviors' core's module embedded as a
# deploy's bundle carries it. The runtime reads the caller of an
# operation through node:async_hooks, which the nodejs_als flag gives, as a
# deploy's Worker has it.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "records", worker = .records),
    # The objects' SQLite files, in the directory the harness runs workerd
    # in, its out/d1-orm/, which each run empties.
    (name = "storage", disk = (path = ".", writable = true)),
  ],
);

const records :Workerd.Worker = (
  modules = [
    (name = "worker.js", esModule = embed "worker.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
    (name = "superschematic_rules_wasm.wasm", wasm = embed "superschematic_rules_wasm.wasm"),
  ],
  durableObjectNamespaces = [(className = "Database", uniqueKey = "d1-orm-records", enableSql = true)],
  durableObjectStorage = (localDisk = "storage"),
  bindings = [(name = "DATABASE", durableObjectNamespace = "Database")],
  compatibilityDate = "2026-09-15",
  compatibilityFlags = ["nodejs_als"],
);
