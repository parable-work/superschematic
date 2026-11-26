// Recurrence's golden expansions (D152) through the wasm32 module: every
// case of runtime/engine/testdata/rules/recurrence/expand.golden.json
// expanded by RulesCore.expand and held to the output the file writes, a
// refused input to { refused: reason }. Rust and Go hold their cores to the
// same file, so the three return the same dates.
import { expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { initSync, RulesError, type ExpandInput } from '../dist/index.js';

interface Case {
  readonly name: string;
  readonly input: ExpandInput;
  readonly output: unknown;
}

const golden = JSON.parse(readFileSync(new URL('../../../engine/testdata/rules/recurrence/expand.golden.json', import.meta.url), 'utf8')) as { cases: Case[] };
const core = initSync();

function answer(input: ExpandInput): unknown {
  try {
    return core.expand(input);
  } catch (error) {
    if (error instanceof RulesError && error.code === 'invalid_input' && error.message.startsWith('expand: ')) {
      return { refused: error.message.slice('expand: '.length) };
    }
    throw error;
  }
}

test('the golden file holds the three anchors', () => {
  expect(golden.cases.length).toBeGreaterThanOrEqual(10);
  expect(new Set(golden.cases.map(c => c.input.anchor))).toEqual(new Set(['calendar', 'afterDone', 'relative']));
});

for (const c of golden.cases) {
  test(c.name, () => {
    expect(answer(c.input)).toEqual(c.output);
  });
}

test('an input that is no recurrence is refused as invalid_input', () => {
  expect(() => core.expand({ anchor: 'weekly' } as unknown as ExpandInput)).toThrow(RulesError);
});
