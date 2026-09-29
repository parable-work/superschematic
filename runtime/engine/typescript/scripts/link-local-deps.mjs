// Put the in-repository packages the engine imports into node_modules.
//
// @superschematic/schema-runtime and @superschematic/schema-ir are not
// package.json dependencies, for the reason the schema runtime gives in its
// own copy of this script (runtime/schema/typescript/scripts/link-local-deps.mjs):
// they are unpublished, and bun resolves a `file:` spec nested inside an
// installed package relative to the consumer. A consumer declares them, and
// superscalar, itself. @superschematic/http-runtime is an optional peer
// dependency, which bun does not install.
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
import { cpSync, existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const repo = join(root, "..", "..", "..");
const scope = join(root, "node_modules", "@superschematic");
const schemaIR = join(repo, "ir", "typescript");
const schemaRuntime = join(repo, "runtime", "schema", "typescript");
const httpRuntime = join(repo, "runtime", "http", "typescript");
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
