import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { callEntry, grantTokenHash, reachesOf } from './access';
import type { Principal } from './auth';

/*
The access vectors (D156, D180) the Go runtime writes
(`go test ./access -run TestAccessParityVectors -update` in
runtime/schema/go) and both runtimes run: a grant token's stored hash, a
role's reaches read by type, and the access-log entry of a call.
*/

interface Vectors {
  readonly tokenHashes: readonly { token: string; want: string }[];
  readonly reaches: readonly { name: string; entries: string[]; want: Record<string, string[]> }[];
  readonly callEntries: readonly { name: string; principal: Partial<Principal>; operation: string; method: string; want: Record<string, string> | null }[];
}

const vectors = JSON.parse(readFileSync(fileURLToPath(new URL('../../testdata/access_parity.json', import.meta.url)), 'utf8')) as Vectors;

describe('access parity', () => {
  test.each(vectors.tokenHashes.map(tc => [tc.token, tc] as const))('the hash of %s', (_, tc) => {
    expect(grantTokenHash(tc.token)).toBe(tc.want);
  });
  test.each(vectors.reaches.map(tc => [tc.name, tc] as const))('reaches: %s', (_, tc) => {
    expect(reachesOf(tc.entries)).toEqual(tc.want);
  });
  test.each(vectors.callEntries.map(tc => [tc.name, tc] as const))('call entry: %s', (_, tc) => {
    const principal = tc.principal.subject === undefined ? null : ({ permissions: [], ...tc.principal } as Principal);
    expect(callEntry(principal, tc.operation, tc.method) ?? null).toEqual(tc.want);
  });
});
