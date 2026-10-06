// Put the in-repository packages the engine imports into node_modules.
//
// @superschematic/schema-runtime and @superschematic/schema-ir are not
// package.json dependencies, for the reason the schema runtime gives in its
// own copy of this script (runtime/schema/typescript/scripts/link-local-deps.mjs):
// they are unpublished, and bun resolves a `file:` spec nested inside an
// installed package relative to the consumer. A consumer declares them, and
// superscalar, itself. @superschematic/http-runtime is unpublished too: the
// main entry point imports its framework-free entry point for the default
// permission matcher, and ./http its Hono adapter. It is an optional peer
// dependency only so bun does not look for it in the registry; a consumer
// declares it as it declares the other two.
//
// schema-ir is a symlink. The schema runtime is a copy of its built
// package (package.json and dist/, so run bun run build in
// runtime/schema/typescript first), with its node_modules linked for
// superscalar, ajv and js-yaml. A symlink would not do: Bun 1.4.0 resolves
// the imports of a module under runtime/schema/typescript through that
// directory's tsconfig.json paths, which point superscalar at declaration
// files, while under node_modules no tsconfig applies.
//
// The http runtime is a copy of its built package too (run bun run build
// in runtime/http/typescript first), with no node_modules of its own: it
// resolves hono and superscalar from this package's node_modules, as it
// would in a consumer's, so the engine and the runtime share one Hono. A
// symlink would resolve them from runtime/http/typescript/node_modules, a
// second copy of Hono. superscalar is a symlink to the checkout
// scripts/superscalar-dep.sh stands up.
//
// @superschematic/versiongraph is a dependency (D32): Branches, a core
// behavior, runs the version graph's engine, and the engine instantiates
// its wasm core when it first runs a schema that composes Branches. Its
// spec is a file: path into this checkout, as the TypeScript types tsgen
// writes for a graph depend on it, so bun install puts a copy of the
// package in node_modules, taken when it runs. The build here replaces
// that copy with the package as runtime/versiongraph/typescript last
// built it, its package.json and dist/ with the wasm module (run bun
// install && bun run build there first, which needs cargo and the
// wasm32-unknown-unknown target), as it copies the http runtime: the
// version graph imports nothing at run time, so it needs no node_modules
// of its own.
import { cpSync, existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const repo = join(root, "..", "..", "..");
const scope = join(root, "node_modules", "@superschematic");
const schemaIR = join(repo, "ir", "typescript");
const schemaRuntime = join(repo, "runtime", "schema", "typescript");
const httpRuntime = join(repo, "runtime", "http", "typescript");
const versiongraph = join(repo, "runtime", "versiongraph", "typescript");
const superscalar = join(repo, "third_party", "superscalar", "bindings", "typescript");

for (const required of [join(schemaIR, "package.json"), join(schemaRuntime, "package.json"), join(httpRuntime, "package.json")]) {
  if (!existsSync(required)) {
    console.error(`link-local-deps: ${required} is missing`);
    process.exit(1);
  }
}
if (!existsSync(join(superscalar, "package.json"))) {
  console.error(`link-local-deps: ${superscalar} has no package.json (run scripts/superscalar-dep.sh)`);
  process.exit(1);
}
for (const required of [join(schemaRuntime, "dist", "esm", "index.js"), join(schemaRuntime, "node_modules", "superscalar")]) {
  if (!existsSync(required)) {
    console.error(`link-local-deps: ${required} is missing; run bun install && bun run build in runtime/schema/typescript first`);
    process.exit(1);
  }
}
if (!existsSync(join(httpRuntime, "dist", "index.js"))) {
  console.error(`link-local-deps: ${join(httpRuntime, "dist", "index.js")} is missing; run bun install && bun run build in runtime/http/typescript first`);
  process.exit(1);
}
for (const required of [join(versiongraph, "dist", "engine.js"), join(versiongraph, "dist", "sqlite.js"), join(versiongraph, "dist", "superschematic_versiongraph.wasm")]) {
  if (!existsSync(required)) {
    console.error(`link-local-deps: ${required} is missing; run bun install && bun run build in runtime/versiongraph/typescript first`);
    process.exit(1);
  }
}

mkdirSync(scope, { recursive: true });

const irLink = join(scope, "schema-ir");
remove(irLink);
symlinkSync(relative(scope, schemaIR), irLink, "dir");

const runtimeCopy = join(scope, "schema-runtime");
remove(runtimeCopy);
mkdirSync(runtimeCopy);
cpSync(join(schemaRuntime, "package.json"), join(runtimeCopy, "package.json"));
cpSync(join(schemaRuntime, "dist"), join(runtimeCopy, "dist"), { recursive: true });
symlinkSync(relative(runtimeCopy, join(schemaRuntime, "node_modules")), join(runtimeCopy, "node_modules"), "dir");

const httpCopy = join(scope, "http-runtime");
remove(httpCopy);
mkdirSync(httpCopy);
cpSync(join(httpRuntime, "package.json"), join(httpCopy, "package.json"));
cpSync(join(httpRuntime, "dist"), join(httpCopy, "dist"), { recursive: true });

const versiongraphCopy = join(scope, "versiongraph");
remove(versiongraphCopy);
mkdirSync(versiongraphCopy);
cpSync(join(versiongraph, "package.json"), join(versiongraphCopy, "package.json"));
cpSync(join(versiongraph, "dist"), join(versiongraphCopy, "dist"), { recursive: true });

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
