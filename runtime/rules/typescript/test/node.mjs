// Under Node: import the built package and let init, and initSync, load its
// bundled wasm file by default (a file: URL, which Node's fetch does not
// read). The contract's cases run under Bun (test/*.test.ts); this checks
// that the package loads and runs in Node, and that importing it resolves
// no module but its own entry: a resolve hook records every specifier, so
// a static import of node: or of a driver anywhere in its graph fails the
// script, as a Worker that bundles it would.
import assert from "node:assert/strict";
import { registerHooks } from "node:module";

const resolved = [];
registerHooks({
  resolve(specifier, context, nextResolve) {
    resolved.push(specifier);
    return nextResolve(specifier, context);
  },
});

const { init, initSync, RulesError } = await import("../dist/index.js");
assert.deepEqual(resolved, ["../dist/index.js"], `importing the package resolved ${resolved.join(", ")}`);

const order = {
  name: "Order",
  uses: [{ behavior: "Workflow", config: { states: ["draft", "paid"], transitions: [{ from: "draft", to: "paid" }] }, anchor: { field: "status" } }],
};
const create = { caller: { subject: "alice" }, now: 1000, request: "r1", action: { kind: "create", id: "o1", own: { title: "Desk" } } };

for (const core of [await init(), initSync()]) {
  const step = core.run(order, create);
  assert.deepEqual(step.done.changed.rows[0].owned, { status: { status: "draft", enteredAt: 1000 } });
  assert.deepEqual(core.check([order]), [null]);
  assert.equal(core.declarations()[0].value, "status");
  assert.throws(
    () => core.run({}, create),
    (error) => error instanceof RulesError && error.code === "invalid_input",
  );
}
console.log("node: @superschematic/rules loads and runs, and its entry imports nothing");
