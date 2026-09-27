// Put the in-repository packages the engine imports into node_modules.
//
// @superschematic/schema-runtime and @superschematic/schema-ir are not
// package.json dependencies, for the reason the schema runtime gives in its
// own copy of this script (runtime/schema/typescript/scripts/link-local-deps.mjs):
// they are unpublished, and bun resolves a `file:` spec nested inside an
// installed package relative to the consumer. A consumer declares them, and
// superscalar, itself.
//
// schema-ir is a symlink. The schema runtime is a copy of its built
// package (package.json and dist/, so run bun run build in
// runtime/schema/typescript first), with its node_modules linked for
// superscalar, ajv and js-yaml. A symlink would not do: Bun 1.4.0 resolves
// the imports of a module under runtime/schema/typescript through that
// directory's tsconfig.json paths, which point superscalar at declaration
// files, while under node_modules no tsconfig applies.
import { cpSync, existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const repo = join(root, "..", "..", "..");
const scope = join(root, "node_modules", "@superschematic");
const schemaIR = join(repo, "ir", "typescript");
const schemaRuntime = join(repo, "runtime", "schema", "typescript");

for (const required of [join(schemaIR, "package.json"), join(schemaRuntime, "package.json")]) {
  if (!existsSync(required)) {
    console.error(`link-local-deps: ${required} is missing`);
    process.exit(1);
  }
}
for (const required of [join(schemaRuntime, "dist", "esm", "index.js"), join(schemaRuntime, "node_modules", "superscalar")]) {
  if (!existsSync(required)) {
    console.error(`link-local-deps: ${required} is missing; run bun install && bun run build in runtime/schema/typescript first`);
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
