// Build the core (runtime/versiongraph/rust) for wasm32-unknown-unknown and
// copy the module into dist/, next to the compiled index.js that loads it by
// default. Needs cargo and the toolchain tools.env's RUST_VERSION names, with
// the wasm32-unknown-unknown target
// (rustup toolchain install <RUST_VERSION> --target wasm32-unknown-unknown).
// cargo builds with that toolchain whatever rustup's default is, as
// scripts/versiongraph-archive.sh does, unless RUSTUP_TOOLCHAIN already
// names one.
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const crate = join(root, "..", "rust");
const name = "superschematic_versiongraph.wasm";

const toolsEnv = join(root, "..", "..", "..", "tools.env");
const rustVersion = readFileSync(toolsEnv, "utf8").match(/^RUST_VERSION=(\S+)/m)?.[1];
if (!rustVersion) {
  throw new Error(`${toolsEnv} has no RUST_VERSION= line`);
}

execFileSync("cargo", ["build", "--release", "--target", "wasm32-unknown-unknown"], {
  cwd: crate,
  env: { ...process.env, RUSTUP_TOOLCHAIN: process.env.RUSTUP_TOOLCHAIN || rustVersion },
  stdio: "inherit",
});
mkdirSync(join(root, "dist"), { recursive: true });
copyFileSync(join(crate, "target", "wasm32-unknown-unknown", "release", name), join(root, "dist", name));
