import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { inspect } from 'node:util';
import { fileURLToPath } from 'node:url';
import { FieldEncryptionError, FieldKeyring, fieldKeyId, newRowKey, parseFieldKeys } from './fieldcrypt';
import { SECRET_MASK, SecretValue, revealed } from './secret';

/*
The Encrypted<T> envelopes (D85) the Go runtime writes (`go test
./fieldcrypt -run TestParityVectors -update` in runtime/schema/go), which
this runtime must seal byte for byte and open alike, refusing the same
envelopes for the same reasons.
*/

const VECTORS = fileURLToPath(new URL('../../../schema/testdata/fieldcrypt_parity.json', import.meta.url));

interface Vectors {
  readonly keyrings: { keyring: string; ids?: string[]; error?: string }[];
  readonly seals: { name: string; keyring: string; table: string; field: string; row: string; plaintext: string; nonce: string; envelope: string }[];
  readonly opens: { name: string; keyring: string | null; table: string; field: string; row: string; envelope: string; plaintext?: string; error?: string }[];
}

const vectors = JSON.parse(readFileSync(VECTORS, 'utf8')) as Vectors;

function reasonOf(fn: () => unknown): string {
  try {
    fn();
    return '';
  } catch (error) {
    if (error instanceof FieldEncryptionError) return error.reason;
    throw error;
  }
}

describe('field envelopes match the Go runtime (D85)', () => {
  for (const k of vectors.keyrings) {
    test(`keyring ${JSON.stringify(k.keyring)}`, () => {
      expect(reasonOf(() => FieldKeyring.parse(k.keyring))).toBe(k.error ?? '');
      if (k.error) return;
      const ring = FieldKeyring.parse(k.keyring);
      expect(ring.currentId).toBe(k.ids![0]!);
      const ids = k.keyring.split(',').map(part => fieldKeyId(Buffer.from(part.trim(), 'base64')));
      expect(ids).toEqual(k.ids!);
    });
  }
  for (const s of vectors.seals) {
    test(`seal: ${s.name}`, () => {
      const ring = FieldKeyring.parse(s.keyring);
      const cell = { table: s.table, field: s.field, row: s.row };
      expect(ring.seal(cell, s.plaintext, Buffer.from(s.nonce, 'base64url'))).toBe(s.envelope);
      expect(ring.open(cell, s.envelope)).toBe(s.plaintext);
    });
  }
  for (const o of vectors.opens) {
    test(`open: ${o.name}`, () => {
      const cell = { table: o.table, field: o.field, row: o.row };
      if (o.keyring === null) {
        // A repository without keys refuses before it opens anything.
        expect(o.error).toBe('no-keys');
        return;
      }
      const ring = FieldKeyring.parse(o.keyring);
      if (o.error) {
        expect(reasonOf(() => ring.open(cell, o.envelope))).toBe(o.error);
      } else {
        expect(ring.open(cell, o.envelope)).toBe(o.plaintext!);
      }
    });
  }
});

describe('a keyring', () => {
  const key = vectors.keyrings[0]!.keyring;

  test('seals each value under a nonce of its own', () => {
    const ring = parseFieldKeys(key);
    const cell = { table: 'PlaidItem', field: 'accessToken', row: newRowKey() };
    const a = ring.seal(cell, 'secret');
    const b = ring.seal(cell, 'secret');
    expect(a).not.toBe(b);
    expect(ring.isCurrent(a)).toBe(true);
    expect(a.startsWith(ring.currentPrefix.slice(0, -1))).toBe(true);
    expect(ring.open(cell, b)).toBe('secret');
  });

  test('takes the secret as an env config holds it', () => {
    expect(parseFieldKeys(new SecretValue(key)).currentId).toBe(vectors.keyrings[0]!.ids![0]!);
    const ring = parseFieldKeys(key);
    expect(parseFieldKeys(ring)).toBe(ring);
  });

  test('names the cell, never the value or the key, in its errors', () => {
    const ring = parseFieldKeys(key);
    let error: unknown;
    try {
      ring.open({ table: 'PlaidItem', field: 'accessToken', row: 'r' }, 'access-sandbox-plain');
    } catch (e) {
      error = e;
    }
    expect(error).toBeInstanceOf(FieldEncryptionError);
    const e = error as FieldEncryptionError;
    expect([e.reason, e.table, e.field]).toEqual(['malformed', 'PlaidItem', 'accessToken']);
    expect(e.message).not.toContain('access-sandbox-plain');
    expect(reasonOf(() => parseFieldKeys('not base64!'))).toBe('bad-keys');
    try {
      parseFieldKeys(`${key},not base64!`);
    } catch (err) {
      expect((err as Error).message).not.toContain(key);
    }
  });

  test('generates version 4 row keys', () => {
    expect(newRowKey()).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u);
  });
});

describe('a SecretValue', () => {
  test('prints its mask, and reveal() returns the value', () => {
    const secret = new SecretValue('access-sandbox-1');
    expect(JSON.stringify({ secret })).toBe(`{"secret":"${SECRET_MASK}"}`);
    expect(String(secret)).toBe(SECRET_MASK);
    expect(`${secret}`).toBe(SECRET_MASK);
    expect(inspect({ secret })).not.toContain('access-sandbox-1');
    expect(secret.reveal()).toBe('access-sandbox-1');
    expect(revealed(secret)).toBe('access-sandbox-1');
    expect(revealed('plain')).toBe('plain');
    // Another copy of the class, as an env config generates one, reveals alike.
    expect(revealed({ reveal: () => 'other' })).toBe('other');
  });
});
