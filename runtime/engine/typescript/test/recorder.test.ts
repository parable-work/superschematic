// The scenario recorder (recorder.ts) held to the scenarios README's rule
// for times (D58): it records recorder-clocks.ts under Node.js with
// ENGINE_RECORD set. A test's fixed clock is written as it read it, and so
// is every number of its engine's steps, though a seq equals the clock's
// reading; only the wall clock's times are symbols; and a fixed clock that
// reads near the wall clock fails the test that opened it.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const root = fileURLToPath(new URL('..', import.meta.url));

type Step = Record<string, unknown>;

// recordClocks runs the recorder-clocks tests whose names match pattern
// under Node.js, recording to a fresh directory, and returns the runner's
// exit code and output and the scenarios written.
async function recordClocks(pattern: string): Promise<{ code: number | null; output: string; scenarios: Array<{ name: string; steps: Step[] }> }> {
  const out = mkdtempSync(join(tmpdir(), 'superschematic-engine-recorder-'));
  try {
    const env: Record<string, string | undefined> = { ...process.env, ENGINE_RECORD: out };
    delete env.NODE_TEST_CONTEXT;
    const output: string[] = [];
    const code = await new Promise<number | null>((resolve, reject) => {
      const child = spawn('node', ['--test', '--test-reporter=spec', `--test-name-pattern=${pattern}`, 'test/recorder-clocks.ts'], {
        cwd: root,
        env,
        stdio: ['ignore', 'pipe', 'pipe'],
      });
      child.stdout.on('data', (chunk: Buffer) => output.push(chunk.toString()));
      child.stderr.on('data', (chunk: Buffer) => output.push(chunk.toString()));
      child.on('error', reject);
      child.on('close', resolve);
    });
    let scenarios: Array<{ name: string; steps: Step[] }> = [];
    try {
      scenarios = (JSON.parse(readFileSync(join(out, 'scenarios', 'recorder-clocks.json'), 'utf8')) as { scenarios: typeof scenarios }).scenarios;
    } catch {
      // Nothing recorded.
    }
    return { code, output: output.join(''), scenarios };
  } finally {
    rmSync(out, { recursive: true, force: true });
  }
}

test('a fixed clock of 7 is written as 7, and so is a seq of 7, beside the wall clock whose times are symbols', { timeout: 60_000 }, async () => {
  const { code, output, scenarios } = await recordClocks('a fixed clock of 7');
  assert.equal(code, 0, output);
  assert.deepEqual(
    scenarios.map((scenario) => scenario.name),
    ['a fixed clock of 7 beside the wall clock']
  );
  const steps = scenarios[0]!.steps;
  const fixed = steps.filter((step) => step.as === 'e1' || step.engine === 'e1');
  const wall = steps.filter((step) => step.as === 'e2' || step.engine === 'e2');
  assert.deepEqual(fixed[0], { call: 'open', as: 'e1', args: [{ path: '{{dir:1}}/engine.db', policy: 'allowAll', clock: 'fixed', ids: 'seeded' }], now: 7 });
  const expected = (engine: Step[], call: string): Record<string, unknown> => engine.find((step) => step.call === call)?.expect as Record<string, unknown>;
  assert.equal(expected(fixed, 'schemas.define').definedAt, 7);
  const read = expected(fixed, 'instances.get');
  assert.deepEqual([read.version, read.seq, read.createdAt, read.updatedAt], [1, 7, 7, 7]);
  // No step of the fixed clock's engine holds a time symbol.
  assert.ok(!JSON.stringify(fixed).includes('{{time'), JSON.stringify(fixed));
  // The wall clock's readings are the scenario's time symbols, from the first.
  assert.deepEqual(wall[0], { call: 'open', as: 'e2', args: [{ path: '{{dir:2}}/engine.db', policy: 'allowAll', ids: 'seeded', clock: 'wall' }] });
  assert.equal(expected(wall, 'schemas.define').definedAt, '{{time:1}}');
  const created = expected(wall, 'instances.create');
  assert.deepEqual([created.version, created.seq, created.createdAt, created.updatedAt], [1, 1, '{{time:2}}', '{{time:2}}']);
});

test('a fixed clock that reads near the wall clock fails the test, which the recorder does not write', { timeout: 60_000 }, async () => {
  const { code, output, scenarios } = await recordClocks('a fixed clock that reads today');
  assert.notEqual(code, 0, output);
  assert.match(output, /the recorder cannot tell the times of "a fixed clock that reads today beside the wall clock" apart/u);
  assert.deepEqual(scenarios, []);
});
