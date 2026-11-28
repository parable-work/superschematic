import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { MEDIA_TYPE, isFileName, mediaTypeAllowed } from './rowfeatures';

/*
The rules of a file of a row (D154) the Go runtime writes
(`go test ./rowrefs -run TestAttachmentsParityVectors -update` in
runtime/schema/go) and both runtimes run: a file's name, counted in code
points (D14, amended), a media type, and a declaration's mediaTypes
admitting one.
*/

interface Vectors {
  readonly names: readonly { name: string; text: string; want: boolean }[];
  readonly mediaTypes: readonly { name: string; mediaType: string; want: boolean }[];
  readonly allowed: readonly { name: string; allowed: string[]; mediaType: string; want: boolean }[];
}

const vectors = JSON.parse(readFileSync(fileURLToPath(new URL('../../testdata/attachments_parity.json', import.meta.url)), 'utf8')) as Vectors;

describe('attachments parity', () => {
  test.each(vectors.names.map(tc => [tc.name, tc] as const))('name: %s', (_, tc) => {
    expect(isFileName(tc.text)).toBe(tc.want);
  });
  test.each(vectors.mediaTypes.map(tc => [tc.name, tc] as const))('media type: %s', (_, tc) => {
    expect(MEDIA_TYPE.test(tc.mediaType)).toBe(tc.want);
  });
  test.each(vectors.allowed.map(tc => [tc.name, tc] as const))('allowed: %s', (_, tc) => {
    expect(mediaTypeAllowed(tc.allowed, tc.mediaType)).toBe(tc.want);
  });
});
