/*
The canonical JSON of a value the value store holds: RFC 8785, the JSON
Canonicalization Scheme. Compact, object members sorted by their keys'
UTF-16 code units, strings and numbers as ECMAScript's JSON.stringify
writes them. Its SHA-256 is the value's hash, so a reader in any language
with a JCS library computes the same hash from the same value, and a client
that fetches a value by its hash can check what it got.

The canonical form is as long as JSON.stringify's compact form of the same
value: only the order of members differs. So the store measures a value
against its threshold with the runtime's own serializer and writes the
canonical form only for a value it keeps.
*/

import { createHash } from 'node:crypto';

/**
 * canonicalJSON writes a JSON value as RFC 8785 does. A member whose value
 * is undefined is left out, as JSON.stringify leaves it; anything that is
 * not JSON throws TypeError.
 */
export function canonicalJSON(value: unknown): string {
  const out: string[] = [];
  write(value, out);
  return out.join('');
}

function write(value: unknown, out: string[]): void {
  if (value === null) {
    out.push('null');
    return;
  }
  switch (typeof value) {
    case 'boolean':
      out.push(value ? 'true' : 'false');
      return;
    case 'number':
      if (!Number.isFinite(value)) {
        throw new TypeError(`${String(value)} is not a JSON number`);
      }
      out.push(JSON.stringify(value));
      return;
    case 'string':
      out.push(JSON.stringify(value));
      return;
    case 'object': {
      if (Array.isArray(value)) {
        out.push('[');
        value.forEach((element, index) => {
          if (index > 0) {
            out.push(',');
          }
          write(element === undefined ? null : element, out);
        });
        out.push(']');
        return;
      }
      const object = value as Record<string, unknown>;
      const keys = Object.keys(object)
        .filter((key) => object[key] !== undefined)
        .sort();
      out.push('{');
      keys.forEach((key, index) => {
        if (index > 0) {
          out.push(',');
        }
        out.push(JSON.stringify(key), ':');
        write(object[key], out);
      });
      out.push('}');
      return;
    }
    default:
      throw new TypeError(`a ${typeof value} is not a JSON value`);
  }
}

/** sha256Hex is the SHA-256 of a text's UTF-8 bytes, in lowercase hex. */
export function sha256Hex(text: string): string {
  return createHash('sha256').update(text, 'utf8').digest('hex');
}
