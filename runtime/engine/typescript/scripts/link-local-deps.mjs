// Put the in-repository packages the engine imports into node_modules.
//
// The engine is the Rust engine's Node-API addon (D58): its TypeScript
// reads schemas no more, so it needs neither the schema runtime, the
// schema IR nor the version graph. It imports @superschematic/http-runtime:
// the main entry point its framework-free entry point, for the default
// permission matcher, and ./http and ./mcp its Hono adapter. The runtime is
// unpublished, so it is an optional peer dependency only so bun does not
// look for it in the registry; a consumer declares it.
//
// The http runtime is a copy of its built package (run bun run build in
// runtime/http/typescript first), with no node_modules of its own: it
// resolves hono and superscalar from this package's node_modules, as it
// would in a consumer's, so the engine and the runtime share one Hono. A
// symlink would resolve them from runtime/http/typescript/node_modules, a
// second copy of Hono. superscalar, which the runtime's parameter decoding
// imports, is a symlink to the checkout scripts/superscalar-dep.sh stands
// up.
import { cpSync, existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const repo = join(root, "..", "..", "..");
const scope = join(root, "node_modules", "@superschematic");
const httpRuntime = join(repo, "runtime", "http", "typescript");
const superscalar = join(repo, "third_party", "superscalar", "bindings", "typescript");

if (!existsSync(join(httpRuntime, "package.json"))) {
  console.error(`link-local-deps: ${join(httpRuntime, "package.json")} is missing`);
  process.exit(1);
}
if (!existsSync(join(superscalar, "package.json"))) {
  console.error(`link-local-deps: ${superscalar} has no package.json (run scripts/superscalar-dep.sh)`);
  process.exit(1);
}
if (!existsSync(join(httpRuntime, "dist", "index.js"))) {
  console.error(`link-local-deps: ${join(httpRuntime, "dist", "index.js")} is missing; run bun install && bun run build in runtime/http/typescript first`);
  process.exit(1);
}

mkdirSync(scope, { recursive: true });

// What earlier builds linked, which the engine no longer imports.
for (const stale of ["schema-ir", "schema-runtime", "versiongraph"]) {
  remove(join(scope, stale));
}

const httpCopy = join(scope, "http-runtime");
remove(httpCopy);
mkdirSync(httpCopy);
cpSync(join(httpRuntime, "package.json"), join(httpCopy, "package.json"));
cpSync(join(httpRuntime, "dist"), join(httpCopy, "dist"), { recursive: true });

const superscalarLink = join(root, "node_modules", "superscalar");
remove(superscalarLink);
symlinkSync(relative(dirname(superscalarLink), superscalar), superscalarLink, "dir");

function remove(path) {
  if (existsSync(path) || isDanglingLink(path)) {
    rmSync(path, { recursive: true, force: true });
  }
}

function isDanglingLink(path) {
  try {
    lstatSync(path);
    return true;
  } catch {
    return false;
  }
}
