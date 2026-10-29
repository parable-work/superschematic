// The Workers check (D64, section 18.5 of docs/stack-model.md): bundles each
// entry of this package a Worker imports, ., ./hono, ./openapi, ./mcp,
// ./workers, ./d1, ./r2 and ./workers-telemetry, as a Worker's bundle.ts
// bundles it, with
// `bun build --target=browser --conditions=workerd`, every .wasm module and
// node:async_hooks kept external, and fails on what workerd cannot run
// without nodejs_compat:
//   - an import from node: other than node:async_hooks, which a Worker has
//     under the nodejs_als compatibility flag (D64, amended); Bun's browser
//     target replaces a node: module with a polyfill or an empty module, so
//     the check reads the bundle's metafile, not its text;
//   - an import from bun:, or the Bun global;
//   - a WebAssembly module other than superscalar's,
//     superscalar_wasm_bg.wasm, which a Worker's upload carries beside its
//     bundle.
// It bundles dist/, so run it after `bun run build`. Arguments name other
// files to bundle in place of the entries, which its test plants.
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const ENTRIES = ['index', 'hono', 'openapi', 'mcp', 'workers', 'd1', 'r2', 'workers-telemetry'];
/** The one node: module a Worker imports, under nodejs_als. */
const ASYNC_HOOKS = 'node:async_hooks';
const SUPERSCALAR_WASM = 'superscalar_wasm_bg.wasm';

const files = process.argv.length > 2 ? process.argv.slice(2) : ENTRIES.map(name => join(root, 'dist', `${name}.js`));

/** Each refusal of the bundle of file. */
function check(file) {
  const out = mkdtempSync(join(tmpdir(), 'workers-check-'));
  try {
    const metafile = join(out, 'meta.json');
    const built = spawnSync(
      'bun',
      [
        'build',
        '--target=browser',
        '--conditions=workerd',
        '--conditions=worker',
        '--external=*.wasm',
        '--external=cloudflare:*',
        // node:* rather than node:async_hooks alone, which Bun's browser
        // target swaps for an empty module all the same.
        '--external=node:*',
        '--minify',
        `--metafile=${metafile}`,
        `--outdir=${join(out, 'bundle')}`,
        file,
      ],
      { cwd: root, encoding: 'utf8' }
    );
    if (built.status !== 0) {
      // Bun refuses some imports for a browser itself, such as bun:'s.
      const reason = (built.stderr || built.stdout || '').split('\n').find(line => line.startsWith('error:')) ?? `bun build exited ${built.status}`;
      return [`the bundle does not build for workerd: ${reason.replace(/^error: /u, '')}`];
    }
    const meta = JSON.parse(readFileSync(metafile, 'utf8'));
    const problems = [];
    for (const [key, { imports = [] }] of Object.entries(meta.inputs)) {
      // A polyfill Bun put in for a node: module imports more of them; the
      // module that imported the first is the one at fault.
      if (key.startsWith('node:')) continue;
      const input = key.startsWith('../') ? basename(key) : key;
      for (const { path, original } of imports) {
        // Bun names a bare builtin by its node: name, and swaps a bare
        // async_hooks, which no external pattern keeps, for an empty module.
        if (path === ASYNC_HOOKS && original !== undefined && !original.startsWith('node:')) {
          problems.push(`${input} imports ${original} without node:, which the bundle swaps for an empty module; import ${ASYNC_HOOKS}`);
        } else if (path.startsWith('node:') && path !== ASYNC_HOOKS) problems.push(`${input} imports ${path}, which workerd has only under nodejs_compat`);
        else if (path.startsWith('bun:')) problems.push(`${input} imports ${path}, which only Bun has`);
        else if (path.endsWith('.wasm') && basename(path) !== SUPERSCALAR_WASM) {
          problems.push(`${input} imports the WebAssembly module ${path}; a Worker carries superscalar's alone`);
        }
      }
    }
    const bundle = join(out, 'bundle');
    for (const name of readdirSync(bundle).filter(name => name.endsWith('.js'))) {
      // Minified, the bundle keeps the name of a global it reads.
      if (/(?<![\w$.'"`])Bun(?![\w$])/u.test(readFileSync(join(bundle, name), 'utf8'))) {
        problems.push(`the bundle reads the Bun global, which only Bun has`);
      }
    }
    return [...new Set(problems)].sort();
  } finally {
    rmSync(out, { recursive: true, force: true });
  }
}

let failed = false;
for (const file of files) {
  const problems = check(file);
  const name = file.startsWith(root) ? file.slice(root.length + 1) : file;
  if (problems.length === 0) {
    console.log(`workers-check: ${name} bundles for workerd`);
    continue;
  }
  failed = true;
  for (const problem of problems) console.error(`workers-check: ${name}: ${problem}`);
}
if (failed) process.exit(1);
