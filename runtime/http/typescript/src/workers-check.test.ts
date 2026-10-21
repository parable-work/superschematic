import { afterAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

/*
The Workers check, scripts/workers-check.mjs (D64), on files planted here:
it refuses an import from node: but node:async_hooks, even one Bun's
browser target would replace with an empty module, the Bun global, and a WebAssembly module
other than superscalar's, and passes what workerd runs. `bun run test`
runs the check on the package's own entries after the build.
*/

const script = fileURLToPath(new URL('../scripts/workers-check.mjs', import.meta.url));
const dir = mkdtempSync(join(tmpdir(), 'workers-check-test-'));
afterAll(() => rmSync(dir, { recursive: true, force: true }));

function run(name: string, source: string): { code: number; output: string } {
  const file = join(dir, name);
  writeFileSync(file, source);
  const done = Bun.spawnSync(['node', script, file], { stdout: 'pipe', stderr: 'pipe' });
  return { code: done.exitCode, output: done.stdout.toString() + done.stderr.toString() };
}

describe('workers-check', () => {
  test('refuses a planted node:fs import', () => {
    const { code, output } = run('fs.ts', "import { readFileSync } from 'node:fs';\nexport const read = () => readFileSync('/etc/hostname');\n");
    expect(code).toBe(1);
    expect(output).toContain('fs.ts imports node:fs, which workerd has only under nodejs_compat');
  });

  test('passes node:async_hooks, which a Worker has under nodejs_als', () => {
    const { code, output } = run('als.ts', "import { AsyncLocalStorage } from 'node:async_hooks';\nexport const store = new AsyncLocalStorage<string>();\n");
    expect(output).not.toContain('nodejs_compat');
    expect(code).toBe(0);
  });

  test('refuses a Node built-in named without node:', () => {
    const { code, output } = run('crypto.ts', "import { createHash } from 'crypto';\nexport const hash = () => createHash('sha256');\n");
    expect(code).toBe(1);
    expect(output).toContain('crypto.ts imports node:crypto');
  });

  test('refuses the Bun global and bun: modules', () => {
    const global = run('global.ts', 'export const version = () => Bun.version;\n');
    expect(global.code).toBe(1);
    expect(global.output).toContain('the bundle reads the Bun global');
    const sqlite = run('sqlite.ts', "import { Database } from 'bun:sqlite';\nexport const open = () => new Database(':memory:');\n");
    expect(sqlite.code).toBe(1);
    expect(sqlite.output).toContain('sqlite.ts: the bundle does not build for workerd: Browser build cannot import Bun builtin: "bun:sqlite"');
  });

  test("refuses a WebAssembly module other than superscalar's", () => {
    const other = run('other-wasm.ts', "import module from './other.wasm';\nexport default module;\n");
    expect(other.code).toBe(1);
    expect(other.output).toContain('imports the WebAssembly module ./other.wasm');
    const superscalar = run('superscalar-wasm.ts', "import module from './superscalar_wasm_bg.wasm';\nexport default module;\n");
    expect(superscalar.code).toBe(0);
  });

  test('passes what workerd runs', () => {
    const { code, output } = run('fine.ts', "export const digest = (text: string) => crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));\n");
    expect(output).toContain('fine.ts bundles for workerd');
    expect(code).toBe(0);
  });
});
