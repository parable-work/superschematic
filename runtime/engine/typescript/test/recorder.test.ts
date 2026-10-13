// The scenario recorder (recorder.ts) held to the scenarios README's rules
// for times and hooks (D58): it records recorder-clocks.ts and
// recorder-hooks.ts under Node.js with ENGINE_RECORD set. A test's fixed
// clock is written as it read it, and so is every number of its engine's
// steps, though a seq equals the clock's reading; only the wall clock's
// times are symbols; and a fixed clock that reads near the wall clock fails
// the test that opened it. A behavior that runs a closure the test set, or
// reads a value the test changed, marks its scenario js, and the step it
// ran in; one with nothing set, or whose closure nothing runs, does not.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const root = fileURLToPath(new URL('..', import.meta.url));

type Step = Record<string, unknown>;

type Scenario = { name: string; js?: string[]; testBehaviors?: string[]; steps: Step[] };

// record runs the tests of a recorder fixture (recorder-clocks,
// recorder-hooks) whose names match pattern under Node.js, recording to a
// fresh directory, and returns the runner's exit code and output and the
// scenarios written.
async function record(fixture: string, pattern: string): Promise<{ code: number | null; output: string; scenarios: Scenario[] }> {
  const out = mkdtempSync(join(tmpdir(), 'superschematic-engine-recorder-'));
  try {
    const env: Record<string, string | undefined> = { ...process.env, ENGINE_RECORD: out };
    delete env.NODE_TEST_CONTEXT;
    const output: string[] = [];
    const code = await new Promise<number | null>((resolve, reject) => {
      const child = spawn('node', ['--test', '--test-reporter=spec', `--test-name-pattern=${pattern}`, `test/${fixture}.ts`], {
        cwd: root,
        env,
        stdio: ['ignore', 'pipe', 'pipe'],
      });
      child.stdout.on('data', (chunk: Buffer) => output.push(chunk.toString()));
      child.stderr.on('data', (chunk: Buffer) => output.push(chunk.toString()));
      child.on('error', reject);
      child.on('close', resolve);
    });
    let scenarios: Scenario[] = [];
    try {
      scenarios = (JSON.parse(readFileSync(join(out, 'scenarios', `${fixture}.json`), 'utf8')) as { scenarios: typeof scenarios }).scenarios;
    } catch {
      // Nothing recorded.
    }
    return { code, output: output.join(''), scenarios };
  } finally {
    rmSync(out, { recursive: true, force: true });
  }
}

test('a fixed clock of 7 is written as 7, and so is a seq of 7, beside the wall clock whose times are symbols', { timeout: 60_000 }, async () => {
  const { code, output, scenarios } = await record('recorder-clocks', 'a fixed clock of 7');
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
  const { code, output, scenarios } = await record('recorder-clocks', 'a fixed clock that reads today');
  assert.notEqual(code, 0, output);
  assert.match(output, /the recorder cannot tell the times of "a fixed clock that reads today beside the wall clock" apart/u);
  assert.deepEqual(scenarios, []);
});

// hooks records recorder-hooks.ts once, for the tests below.
let hooks: ReturnType<typeof record> | undefined;

async function hookScenario(name: string): Promise<Scenario> {
  hooks ??= record('recorder-hooks', '.');
  const { code, output, scenarios } = await hooks;
  assert.equal(code, 0, output);
  const scenario = scenarios.find((entry) => entry.name === name);
  assert.ok(scenario, `no scenario "${name}" in ${scenarios.map((entry) => entry.name).join(', ')}`);
  return scenario;
}

// marked lists the calls of a scenario's steps marked js, by index.
const marked = (scenario: Scenario): Array<[number, unknown]> =>
  scenario.steps.flatMap((step, index): Array<[number, unknown]> => (step.js === true ? [[index, step.call]] : []));

test('a scenario whose behavior runs a closure the test set is js, and so is the step it ran in', { timeout: 60_000 }, async () => {
  const scenario = await hookScenario('a reaction runs a closure the test set');
  assert.deepEqual(scenario.testBehaviors, ['test.Ledger']);
  assert.deepEqual(scenario.js, ['testHook.probe.react']);
  // The runner's first pass, before the closure was set, runs nothing of it.
  assert.deepEqual(
    scenario.steps.filter((step) => step.call === 'runner.runDue').map((step) => step.js),
    [undefined, true]
  );
  assert.deepEqual(marked(scenario), [[5, 'runner.runDue']]);
});

test('a scenario whose behavior has no closure set, or one that nothing runs, is not js', { timeout: 60_000 }, async () => {
  for (const name of ['a reaction with no closure set', 'a closure the test set that no runner pass runs']) {
    const scenario = await hookScenario(name);
    assert.deepEqual(scenario.testBehaviors, ['test.Ledger'], name);
    assert.equal(scenario.js, undefined, name);
    assert.deepEqual(marked(scenario), [], name);
  }
});

test('a value hook marks the steps that read it changed, and not those that read its declared value', { timeout: 60_000 }, async () => {
  const scenario = await hookScenario('a guard that reads a value hook the test closes and opens again');
  assert.deepEqual(scenario.js, ['testHook.gate.closed']);
  assert.deepEqual(
    scenario.steps.filter((step) => step.call === 'instances.update').map((step) => [step.js, (step.error as { class?: string } | undefined)?.class]),
    [
      [undefined, undefined],
      [true, 'BehaviorVetoError'],
      [undefined, undefined],
    ]
  );
});
