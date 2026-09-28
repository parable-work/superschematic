// Build the core (runtime/versiongraph/rust) for wasm32-unknown-unknown and
// copy the module into dist/, next to the compiled index.js that loads it by
// default. Needs cargo and the wasm32-unknown-unknown target
// (rustup target add wasm32-unknown-unknown).
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const crate = join(root, "..", "rust");
const name = "superschematic_versiongraph.wasm";

execFileSync("cargo", ["build", "--release", "--target", "wasm32-unknown-unknown"], {
  cwd: crate,
  stdio: "inherit",
});
mkdirSync(join(root, "dist"), { recursive: true });
copyFileSync(join(crate, "target", "wasm32-unknown-unknown", "release", name), join(root, "dist", name));
