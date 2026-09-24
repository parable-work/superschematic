// Symlink the in-repository scalar library into node_modules.
//
// superscalar is not a package.json dependency on purpose, as in the schema
// runtime (runtime/schema/typescript/scripts/link-local-deps.mjs): it is not
// published yet, and bun resolves a `file:` spec nested inside an installed
// package relative to the consumer, so a nested spec fails. A consumer
// declares superscalar itself; this package's own tsc and tests resolve it
// through this link to the checkout scripts/superscalar-dep.sh stands up
// under third_party/.
import { existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const repo = join(root, "..", "..", "..");
const links = [
  { link: join(root, "node_modules", "superscalar"), target: join(repo, "third_party", "superscalar", "bindings", "typescript") },
];

for (const { link, target } of links) {
  if (!existsSync(join(target, "package.json"))) {
    console.error(`link-local-deps: ${target} has no package.json (run scripts/superscalar-dep.sh)`);
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
