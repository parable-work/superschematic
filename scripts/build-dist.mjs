// Build a package's dist/ without ever removing it.
//
//   node ../../../scripts/build-dist.mjs <compiler> [args...]
//
// Run in the package's directory, it sweeps staging directories a killed
// build left more than an hour ago, then runs `<compiler> [args...]
// --outDir <staging>` (tsc takes --outDir over its tsconfig's outDir) into a
// fresh .dist-build-* directory beside dist/, on the same filesystem. Each
// file the compiler wrote whose content differs from dist/'s moves into
// dist/ with a rename, which replaces that one file in one step; a file
// whose content is unchanged is left as it is, mtime and all. Only then is
// what dist/ held that this build did not write removed. A failed build
// leaves dist/ as it was.
//
// What a reader that holds no lock gets while this runs (make ts beside a
// `go test` run, say): dist/ is never missing, and no file is ever missing
// or half written, where `rm -rf dist && tsc` left dist/ missing for as long
// as tsc ran, which flaked parallel Go test packages (CONTRIBUTING.md,
// "Shared build outputs"). It is not a snapshot of the whole tree: while a
// build whose output changed is moving files in, a reader can load a new
// module beside an old one it imports, and one that still holds the old
// graph can find a module the new build dropped gone. Swapping the whole
// directory instead (rename dist to dist.old, then the staging directory to
// dist) leaves a moment with no dist/ at all on every build, an identical
// rebuild included, which is the common case and the failure this replaces;
// swapping a dist symlink would ship a symlink to the package's consumers,
// which install it with `file:` and npm pack. A rebuild with the same output
// changes nothing here, so a reader can only see a mix while the sources
// themselves change. The Go tests read the HTTP runtime only after
// testpaths.BuildHTTPRuntime, under a lock no build of theirs runs outside.
//
// dist/ is this compiler's output alone: a file another step wrote there is
// removed by the next build.
//
// Two builds of the same package at once still race on its node_modules
// (bun install and the link-local-deps scripts replace entries there),
// which is why the Go tests build through testpaths.BuildTypeScriptPackage,
// which holds a cross-process lock and skips a build that is already
// current.
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, readdirSync, renameSync, rmSync, rmdirSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";

const [compiler, ...args] = process.argv.slice(2);
if (!compiler) {
  console.error("usage: node build-dist.mjs <compiler> [args...]");
  process.exit(2);
}

const dist = "dist";
const stagingPrefix = ".dist-build-";

// A staging directory a killed build left behind; a live build's is younger.
const hourAgo = Date.now() - 60 * 60 * 1000;
for (const entry of readdirSync(".", { withFileTypes: true })) {
  if (entry.isDirectory() && entry.name.startsWith(stagingPrefix)) {
    try {
      if (statSync(entry.name).mtimeMs < hourAgo) {
        rmSync(entry.name, { recursive: true, force: true });
      }
    } catch {
      // Another build swept or finished it first.
    }
  }
}

const staging = `${stagingPrefix}${process.pid}-${Date.now()}`;
rmSync(staging, { recursive: true, force: true });

const result = spawnSync(compiler, [...args, "--outDir", staging], { stdio: "inherit" });
if (result.error || result.status !== 0) {
  rmSync(staging, { recursive: true, force: true });
  if (result.error) {
    console.error(`build-dist: ${compiler}: ${result.error.message}`);
  }
  process.exit(result.status || 1);
}
if (!existsSync(staging)) {
  console.error(`build-dist: ${compiler} wrote nothing`);
  process.exit(1);
}

// Each file the build wrote, relative to staging, moved into dist/.
const written = new Set();
for (const file of files(staging)) {
  written.add(file);
  const target = join(dist, file);
  const built = join(staging, file);
  if (sameContent(built, target)) {
    continue;
  }
  mkdirSync(dirname(target), { recursive: true });
  renameSync(built, target);
}
rmSync(staging, { recursive: true, force: true });

// What an earlier build wrote that this one did not, and the directories
// left empty.
for (const file of files(dist)) {
  if (!written.has(file)) {
    rmSync(join(dist, file), { force: true });
  }
}
pruneEmpty(dist);

function sameContent(a, b) {
  try {
    return readFileSync(a).equals(readFileSync(b));
  } catch {
    return false;
  }
}

function files(root) {
  const out = [];
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      if (entry.isDirectory()) {
        walk(path);
      } else {
        out.push(relative(root, path));
      }
    }
  };
  if (existsSync(root)) {
    walk(root);
  }
  return out;
}

function pruneEmpty(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      const path = join(dir, entry.name);
      pruneEmpty(path);
      if (readdirSync(path).length === 0) {
        rmdirSync(path);
      }
    }
  }
}
