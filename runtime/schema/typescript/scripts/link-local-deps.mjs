// Symlink the in-repo packages this one imports into node_modules.
//
// @psgen/scalar-lib and @psgen/schema-ir are not package.json dependencies on
// purpose. bun installs a `file:` dependency into the consumer's node_modules
// (a copy, or per-file symlinks into the source tree, depending on platform)
// and resolves any `file:` spec nested inside it relative to the consumer, so
// a nested spec fails (apps/ installs this package that way). The consumer
// therefore declares all three packages itself (apps/package.json). For this
// package's own tsc and tests the same resolution comes from these symlinks;
// bun install leaves them alone, and bun's install into a consumer drops them.
import { existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const scope = join(root, "node_modules", "@psgen");
mkdirSync(scope, { recursive: true });

const scalarLib = join(root, "..", "..", "..", "parable-scalars", "typescript");
const links = [
  { link: join(scope, "scalar-lib"), target: scalarLib },
  { link: join(scope, "schema-ir"), target: join(root, "..", "..", "schema-ir", "typescript") },
];

for (const { link, target } of links) {
  if (!existsSync(join(target, "package.json"))) {
    console.error(`link-local-deps: ${target} has no package.json`);
    process.exit(1);
  }
  mkdirSync(dirname(link), { recursive: true });
  if (existsSync(link) || isDanglingLink(link)) {
    rmSync(link, { recursive: true, force: true });
  }
  symlinkSync(relative(dirname(link), target), link, "dir");
}

function isDanglingLink(path) {
  try {
    lstatSync(path);
    return true;
  } catch {
    return false;
  }
}
