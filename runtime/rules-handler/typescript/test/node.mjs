// Under Node: import the built package, let loadHandler and
// loadHandlerSync load its bundled wasm file by default (a file: URL, which
// Node's fetch does not read), and run development mode over the
// development layout on node:sqlite's DatabaseSync, which fits the store's
// SqliteConnection: a define, a publish, a create, a behavior's operation
// and a list, then the routes through dev.http. The cases run under Bun
// (test/*.test.ts); this checks that the package's entry runs in Node and
// that importing it resolves no module but its own: a resolve hook records
// every specifier, so a static import of node: or of a driver anywhere in
// its graph fails the script, as a Worker that bundles it
// would.
import assert from "node:assert/strict";
import { registerHooks } from "node:module";

const resolved = [];
registerHooks({
  resolve(specifier, context, nextResolve) {
    resolved.push(specifier);
    return nextResolve(specifier, context);
  },
});

const { Dev, SqliteStore, loadHandler, loadHandlerSync, RefusalError } = await import("../dist/index.js");
// The entry's own modules are relative; anything else is an import of the
// runtime's or a driver's.
const foreign = resolved.filter((specifier) => !specifier.startsWith("./") && !specifier.startsWith("../"));
assert.deepEqual(foreign, [], `importing the package resolved ${foreign.join(", ")}`);
const { DatabaseSync } = await import("node:sqlite");

const order = {
  name: "Order",
  fields: [{ name: "title", typeRef: { name: "string" }, required: true }],
  behaviors: [{ name: "Workflow", field: "status", config: { states: ["draft", "paid"], transitions: [{ from: "draft", to: "paid" }] } }],
};
const me = { subject: "alice", permissions: [] };

for (const handler of [await loadHandler(), loadHandlerSync()]) {
  const store = new SqliteStore(new DatabaseSync(":memory:"), handler.layout());
  const dev = new Dev(handler, store, { clock: () => 1_000_000 });
  await dev.request(me, { kind: "define", document: order });
  await dev.request(me, { kind: "publish", type: "Order" });
  const row = await dev.request(me, { kind: "create", type: "Order", data: { id: "o1", title: "Desk" } });
  assert.equal(row.status, "draft");
  assert.deepEqual(await dev.request(me, { kind: "operation", type: "Order", id: "o1", use: "", operation: "transition", params: { to: "paid" } }), { from: "draft", to: "paid" });
  const page = await dev.request(me, { kind: "list", type: "Order" });
  assert.equal(page.items[0].status, "paid");
  await assert.rejects(dev.request(me, { kind: "get", type: "Order", id: "o9" }), (error) => error instanceof RefusalError && error.refusal.code === "not_found");
  const response = await dev.http(new Request("http://node.test/namespaces/default/schemas/Order/instances/o1", { headers: { "x-superschematic-subject": "alice" } }));
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("etag"), '"2"');
  assert.equal((await response.json()).data.title, "Desk");
}
console.log("node: @superschematic/rules-handler loads, runs over node:sqlite, and its entry imports nothing");
