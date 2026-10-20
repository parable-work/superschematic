// Put @superschematic/engine into node_modules, from this checkout.
//
// The engine is a peer dependency: a deployment opens the engine with
// these behaviors' names, and this package's types and its worker's
// client are the engine's, so both must be the one copy in its
// node_modules. The peer dependency is optional only so bun does not look
// for the unpublished engine in the registry, as the engine's own optional
// peer dependency on @superschematic/http-runtime is; a consumer declares
// it.
//
// The link is a symlink to runtime/engine/typescript, which must be built
// first (bun install && bun run build there, after the http runtime;
// runtime/engine/README.md, "Development"), with its addon
// (scripts/engine-napi.sh, --js-behaviors for these tests' own behaviors).
// The engine's imports resolve from its own node_modules, which its build
// fills (runtime/engine/typescript/scripts/link-local-deps.mjs), so this
// package needs no copy of the http runtime or superscalar.
import { existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const repo = join(root, "..", "..", "..");
const engine = join(repo, "runtime", "engine", "typescript");
const scope = join(root, "node_modules", "@superschematic");

for (const required of [join(engine, "dist", "index.js"), join(engine, "node_modules", "@superschematic", "http-runtime", "dist")]) {
  if (!existsSync(required)) {
    console.error(`link-local-deps: ${required} is missing; run bun install && bun run build in runtime/engine/typescript first`);
    process.exit(1);
  }
}

mkdirSync(scope, { recursive: true });
const link = join(scope, "engine");
if (existsSync(link) || isDanglingLink(link)) {
  rmSync(link, { recursive: true, force: true });
}
symlinkSync(relative(scope, engine), link, "dir");

function isDanglingLink(path) {
  try {
    lstatSync(path);
    return true;
  } catch {
    return false;
  }
}
