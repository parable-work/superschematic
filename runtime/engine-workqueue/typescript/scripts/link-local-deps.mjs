// Put @superschematic/engine into node_modules, from this checkout.
//
// The engine is a peer dependency: a deployment registers these behaviors
// with the engine it opens, so both must be the one copy in its
// node_modules. A second copy would bring its own error classes, and a
// behavior's `instanceof BehaviorVetoError` would miss the engine's. The
// peer dependency is optional only so bun does not look for the
// unpublished engine in the registry, as the engine's own optional peer
// dependency on @superschematic/http-runtime is; a consumer declares it.
//
// The link is a symlink to runtime/engine/typescript, which must be built
// first (bun install && bun run build there, after the schema and http
// runtimes; runtime/engine/README.md, "Development"). The engine's imports
// resolve from its own node_modules, which its build fills
// (runtime/engine/typescript/scripts/link-local-deps.mjs), so this package
// needs no copy of the schema runtime, the http runtime or superscalar. A
// symlink suits the engine, unlike the schema runtime the engine copies:
// its directory has no tsconfig.json paths for Bun to apply.
import { existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const repo = join(root, "..", "..", "..");
const engine = join(repo, "runtime", "engine", "typescript");
const scope = join(root, "node_modules", "@superschematic");

for (const required of [join(engine, "dist", "index.js"), join(engine, "node_modules", "@superschematic", "schema-runtime", "dist")]) {
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
