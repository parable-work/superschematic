/**
 * psgen sidecar-document harness: the entry point bun runs to execute a
 * service's document module (a registered DocumentSpec's File, such as
 * deploy.values.ts) and capture its default export as JSON.
 *
 * Like harness.ts, this file is embedded in the psgen binary and written to
 * a temp path at build time, so it must be fully standalone: it imports
 * nothing. The document is an arbitrary value expression, so there is no
 * registry to read -- the contract is simply the module's default export,
 * serialized verbatim.
 *
 * Usage: bun run harness_document.ts <path/to/document.ts>
 * Output: one JSON document on stdout ({ document, authoringImports,
 * diagnostics }); exit 1 if any diagnostics were collected.
 *
 * authoringImports is the document's transitive module graph: every source file the bundler resolves for the entry module,
 * as absolute paths. The build cache hashes the files that fall outside the
 * schema's own directory, so cross-directory authoring imports (a
 * deploy.values.ts importing the platform model) invalidate the cache
 * without a declared dependency. The crawl uses Bun.build with an external
 * sourcemap -- resolver-faithful and side-effect-free (nothing executes) --
 * and a crawl failure is a hard diagnostic: silently under-recording would
 * mean stale caches.
 *
 * The guard below is the same dynamic side-effect guard the schema harness
 * installs: document modules must be side-effect-free at import (no network,
 * no clock, no randomness, no Bun I/O helpers).
 */

function deny(api: string): never {
  throw new Error(
    `psgen: document modules must be side-effect-free at import: ${api} is unavailable during execution`
  );
}

function installGuard(): void {
  (globalThis as { fetch: unknown }).fetch = () => deny("fetch");
  Math.random = () => deny("Math.random");

  const RealDate = Date;
  globalThis.Date = new Proxy(RealDate, {
    construct(target, args, newTarget) {
      if (args.length === 0) {
        deny("new Date()");
      }
      return Reflect.construct(target, args as unknown[], newTarget);
    },
    apply() {
      deny("Date()");
    },
    get(target, prop, receiver) {
      if (prop === "now") {
        return () => deny("Date.now");
      }
      return Reflect.get(target, prop, receiver);
    }
  }) as DateConstructor;

  const bunGlobal = (globalThis as { Bun?: Record<string, unknown> }).Bun;
  if (bunGlobal !== undefined) {
    const banned = [
      "file",
      "write",
      "spawn",
      "spawnSync",
      "connect",
      "listen",
      "udpSocket",
      "serve",
      "$",
      "mmap"
    ];
    for (const api of banned) {
      try {
        bunGlobal[api] = () => deny(`Bun.${api}`);
      } catch {
        // Non-writable property; nothing else to do.
      }
    }
  }
}

type Diagnostic = { file: string; message: string };

function errorMessage(err: unknown): string {
  if (err instanceof Error) {
    return err.message;
  }
  return String(err);
}

// crawlImports bundles the entry module in memory and reads the sourcemap's
// sources list: the exact file set the resolver walked. Runs before the
// side-effect guard is installed and executes none of the crawled code.
async function crawlImports(entry: string): Promise<string[]> {
  const path = await import("node:path");
  const result = await Bun.build({
    entrypoints: [entry],
    target: "bun",
    sourcemap: "external"
  });
  if (!result.success) {
    const messages = result.logs.map((log) => String(log.message)).join("; ");
    throw new Error(`import crawl failed: ${messages}`);
  }
  const files = new Set<string>();
  for (const artifact of result.outputs) {
    if (artifact.kind !== "sourcemap") {
      continue;
    }
    const map = JSON.parse(await artifact.text()) as { sources?: string[]; sourceRoot?: string };
    const sources = map.sources === undefined ? [] : map.sources;
    const sourceRoot = map.sourceRoot === undefined ? "" : map.sourceRoot;
    for (const source of sources) {
      let file = source;
      if (file.startsWith("file://")) {
        file = new URL(file).pathname;
      }
      if (!path.isAbsolute(file)) {
        file = path.resolve(process.cwd(), sourceRoot, file);
      }
      files.add(file);
    }
  }
  return [...files].sort();
}

async function main(): Promise<void> {
  const file = process.argv[2];
  const diagnostics: Diagnostic[] = [];
  let document: unknown = null;
  let authoringImports: string[] = [];

  if (file === undefined) {
    diagnostics.push({ file: "", message: "usage: harness_document.ts <path/to/document.ts>" });
  } else {
    try {
      authoringImports = await crawlImports(file);
    } catch (err) {
      diagnostics.push({ file, message: errorMessage(err) });
    }

    installGuard();

    try {
      const mod = (await import(file)) as { default?: unknown };
      const doc = mod.default;
      if (doc === undefined) {
        diagnostics.push({ file, message: "the module must default-export its document" });
      } else if (doc === null || typeof doc !== "object" || Array.isArray(doc)) {
        diagnostics.push({ file, message: "the default export must be an object" });
      } else {
        document = doc;
      }
    } catch (err) {
      diagnostics.push({ file, message: errorMessage(err) });
    }
  }

  console.log(JSON.stringify({ document, authoringImports, diagnostics }));
  if (diagnostics.length > 0) {
    process.exit(1);
  }
}

await main();
