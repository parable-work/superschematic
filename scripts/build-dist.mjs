// Build a package's dist/ so that no reader ever finds it missing.
//
//   node ../../../scripts/build-dist.mjs <compiler> [args...]
//
// run in the package's directory, runs `<compiler> [args...] --outDir
// <staging>` (tsc takes --outDir over its tsconfig's outDir), staging a
// fresh directory beside dist/ on the same filesystem. It then moves each
// file the compiler wrote into dist/ with a rename, which replaces a file in
// one step, and only then removes what dist/ held that this build did not
// write. A process reading dist/ meanwhile (another `go test` package
// installing the runtime into its workspace, or loading it) sees each file
// either as it was or as it is now, never missing. Building with
// `rm -rf dist && tsc` left dist/ missing for as long as tsc ran, which
// flaked parallel Go test packages (CONTRIBUTING.md, "Shared build
// outputs"). A failed build leaves dist/ as it was.
//
// The rename makes one build safe for readers; two builds of the same
// package at once still race on its node_modules (bun install and the
// link-local-deps scripts replace entries there), which is why the Go tests
// build through testpaths.BuildTypeScriptPackage, which holds a
// cross-process lock and skips a build that is already current.
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readdirSync, renameSync, rmSync, rmdirSync } from "node:fs";
import { dirname, join, relative } from "node:path";

const [compiler, ...args] = process.argv.slice(2);
if (!compiler) {
  console.error("usage: node build-dist.mjs <compiler> [args...]");
  process.exit(2);
}

const dist = "dist";
const staging = `.dist-build-${process.pid}-${Date.now()}`;
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
  mkdirSync(dirname(target), { recursive: true });
  renameSync(join(staging, file), target);
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
