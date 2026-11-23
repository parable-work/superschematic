// Build the handler's WebAssembly binding
// (runtime/engine/rust/rules-handler-wasm, the crate
// superschematic-rules-handler-wasm of the engine's workspace, D117) for
// wasm32-unknown-unknown and copy the module into dist/, next to the
// compiled index.js that loads it by default, as
// runtime/rules/typescript's script builds the core's.
// Needs cargo and the toolchain tools.env's RUST_VERSION names, with the
// wasm32-unknown-unknown target
// (rustup toolchain install <RUST_VERSION> --target wasm32-unknown-unknown).
// cargo builds with that toolchain whatever rustup's default is, unless
// RUSTUP_TOOLCHAIN already names one. It builds with the workspace's dist
// profile, what a release ships: one codegen unit and LTO across the
// crates, and no symbol table.
//
// A module the same as the one dist/ holds is left as it is, so a build
// that changes nothing moves no file a reader watches; a new one is moved
// in whole, by a rename, so a reader never sees half of it.
//
// No prebuilt module is published yet, so without cargo on PATH the build
// fails and says so.
import { execFileSync } from "node:child_process";
import { copyFileSync, existsSync, mkdirSync, readFileSync, renameSync } from "node:fs";
import { delimiter, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const checkout = join(root, "..", "..", "..");
const workspace = join(checkout, "runtime", "engine", "rust");
const name = "superschematic_rules_handler_wasm.wasm";

const toolsEnv = join(checkout, "tools.env");
const rustVersion = readFileSync(toolsEnv, "utf8").match(/^RUST_VERSION=(\S+)/m)?.[1];
if (!rustVersion) {
  throw new Error(`${toolsEnv} has no RUST_VERSION= line`);
}

const onPath = (tool) =>
  (process.env.PATH ?? "").split(delimiter).some((dir) => dir !== "" && existsSync(join(dir, tool)));

if (!onPath("cargo")) {
  throw new Error("build-wasm: cargo is not on PATH, and the handler's module has no prebuilt copy yet: install the toolchain tools.env names, with the wasm32-unknown-unknown target");
}
execFileSync("cargo", ["build", "--profile", "dist", "--target", "wasm32-unknown-unknown", "-p", "superschematic-rules-handler-wasm"], {
  cwd: workspace,
  env: { ...process.env, RUSTUP_TOOLCHAIN: process.env.RUSTUP_TOOLCHAIN || rustVersion },
  stdio: "inherit",
});
const target = process.env.CARGO_TARGET_DIR || join(workspace, "target");
const built = join(target, "wasm32-unknown-unknown", "dist", name);
const out = join(root, "dist", name);
mkdirSync(join(root, "dist"), { recursive: true });
if (!existsSync(out) || !readFileSync(out).equals(readFileSync(built))) {
  const staged = `${out}.${process.pid}.tmp`;
  copyFileSync(built, staged);
  renameSync(staged, out);
}
