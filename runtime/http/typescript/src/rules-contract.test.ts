import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { CONTRACT } from './rules-contract.testing';

/*
The behaviors' core's JSON contract (D103), which rules.ts's Core* types
copy, held to the Rust types it is a copy of: superschematic-rules writes a
value of each type, every member present and every variant of each enum
(`UPDATE_RULES_CONTRACT=1 cargo test -p superschematic-rules --test
contract_vectors` in runtime/engine/rust), and rules-contract.testing.ts
writes the same values out as the Core* types type them, every member of
each present and every variant of each union, which tsc holds to the types.
A member or a variant the Rust contract gains, loses or renames fails the
comparison here or tsc until rules.ts copies it.
*/

interface Fixture {
  readonly values: Readonly<Record<string, readonly unknown[]>>;
}

const fixture = JSON.parse(readFileSync(fileURLToPath(new URL('../../testdata/rules_contract.json', import.meta.url)), 'utf8')) as Fixture;

describe("the behaviors' core's contract", () => {
  test('the fixture and the Core* values name the same types', () => {
    expect(Object.keys(fixture.values).sort()).toEqual(Object.keys(CONTRACT).sort());
  });

  test.each(Object.keys(CONTRACT))('%s is the Rust type as serde writes it', name => {
    expect(fixture.values[name]).toEqual(CONTRACT[name as keyof typeof CONTRACT] as readonly unknown[]);
  });

  test('tsc holds the values to the Core* types', () => {
    const tsc = fileURLToPath(new URL('../node_modules/typescript/bin/tsc', import.meta.url));
    const project = fileURLToPath(new URL('../tsconfig.contract.json', import.meta.url));
    const run = Bun.spawnSync(['node', tsc, '-p', project], { stdout: 'pipe', stderr: 'pipe' });
    expect(run.stdout.toString() + run.stderr.toString()).toBe('');
    expect(run.exitCode).toBe(0);
  }, 60_000);
});
