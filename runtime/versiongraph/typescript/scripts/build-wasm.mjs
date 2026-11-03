// Build the core (runtime/versiongraph/rust) for wasm32-unknown-unknown and
// copy the module into dist/, next to the compiled index.js that loads it by
// default. Needs cargo and the toolchain tools.env's RUST_VERSION names, with
// the wasm32-unknown-unknown target
// (rustup toolchain install <RUST_VERSION> --target wasm32-unknown-unknown).
// cargo builds with that toolchain whatever rustup's default is, as
// scripts/versiongraph-archive.sh does, unless RUSTUP_TOOLCHAIN already
// names one.
//
// Without cargo on PATH, the module comes prebuilt (D82):
// scripts/prebuilt_deps.py downloads the one the prebuilt-deps workflow
// built from these sources, checks it against the manifest prebuilt.pin
// pins, and stages it in prebuilt/, which this script copies.
import { execFileSync } from "node:child_process";
import { copyFileSync, existsSync, mkdirSync, readFileSync } from "node:fs";
import { delimiter, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const crate = join(root, "..", "rust");
const name = "superschematic_versiongraph.wasm";
const checkout = join(root, "..", "..", "..");

const toolsEnv = join(checkout, "tools.env");
const rustVersion = readFileSync(toolsEnv, "utf8").match(/^RUST_VERSION=(\S+)/m)?.[1];
if (!rustVersion) {
  throw new Error(`${toolsEnv} has no RUST_VERSION= line`);
}

const onPath = (tool) =>
  (process.env.PATH ?? "").split(delimiter).some((dir) => dir !== "" && existsSync(join(dir, tool)));

let built = join(crate, "target", "wasm32-unknown-unknown", "release", name);
if (onPath("cargo")) {
  execFileSync("cargo", ["build", "--release", "--target", "wasm32-unknown-unknown"], {
    cwd: crate,
    env: { ...process.env, RUSTUP_TOOLCHAIN: process.env.RUSTUP_TOOLCHAIN || rustVersion },
    stdio: "inherit",
  });
} else {
  console.error("build-wasm: cargo is not on PATH, so the version-graph core's module comes prebuilt (D82)");
  execFileSync("python3", [join(checkout, "scripts", "prebuilt_deps.py"), "fetch", "versiongraph", "--only", "versiongraph-wasm"], {
    stdio: "inherit",
  });
  built = join(root, "prebuilt", name);
}
mkdirSync(join(root, "dist"), { recursive: true });
copyFileSync(built, join(root, "dist", name));
