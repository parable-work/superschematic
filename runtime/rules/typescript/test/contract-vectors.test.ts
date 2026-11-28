// The contract's types held to the Rust types they copy: superschematic-rules
// writes a value of each type, every member present and every variant of each
// enum, to runtime/http/testdata/rules_contract.json (its
// tests/contract_vectors.rs), and contract-vectors.testing.ts writes the same
// values out as src/contract.ts types them, which tsc -p tsconfig.test.json
// holds to the types.
import { expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { CONTRACT } from './contract-vectors.testing.ts';

interface Fixture {
  readonly values: Readonly<Record<string, readonly unknown[]>>;
}

const fixture = JSON.parse(
  readFileSync(fileURLToPath(new URL('../../../http/testdata/rules_contract.json', import.meta.url)), 'utf8'),
) as Fixture;

const names = Object.keys(CONTRACT);

test('the fixture and the contract values name the same types', () => {
  expect(Object.keys(fixture.values).map(name => name.replace(/^Core/, '')).sort()).toEqual([...names].sort());
});

test.each(names)('%s is the Rust type as serde writes it', name => {
  expect(fixture.values[`Core${name}`]).toEqual(CONTRACT[name as keyof typeof CONTRACT] as readonly unknown[]);
});
