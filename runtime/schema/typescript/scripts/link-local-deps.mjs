// Symlink the in-repo packages this one imports into node_modules.
//
// superscalar and @superschematic/schema-ir are not package.json dependencies on
// purpose. bun installs a `file:` dependency into the consumer's node_modules
// (a copy, or per-file symlinks into the source tree, depending on platform)
// and resolves any `file:` spec nested inside it relative to the consumer, so
// a nested spec fails. The consumer therefore declares all three packages
// itself. For this package's own tsc and tests the same resolution comes from
// these symlinks; bun install leaves them alone, and bun's install into a
// consumer drops them. superscalar resolves to the checkout
// scripts/superscalar-dep.sh stands up under third_party/.
import { existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const repo = join(root, "..", "..", "..");
const links = [
  { link: join(root, "node_modules", "superscalar"), target: join(repo, "third_party", "superscalar", "bindings", "typescript") },
  { link: join(root, "node_modules", "@superschematic", "schema-ir"), target: join(repo, "ir", "typescript") },
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
