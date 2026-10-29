/*
A UUID's two forms (D79): base62 on the wire and in the TypeScript types,
where Identity.UUID and Identity.UserID hold the scalar's canonical form,
and lowercase hyphenated text in Postgres, whose UUID columns read and write
it, and in SQLite, where a UUID column is TEXT. superscalar's ParseUUID
reads either, and so do these. The identity store, the queue clients and
the row codec (./postgres) convert through them, so every runtime path
writes one form to the database and hands the other to the types. It
imports nothing, so a Worker bundles it.
*/

const BASE62 = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';
const HYPHENATED_UUID = /^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$/u;
const MAX_UUID = (1n << 128n) - 1n;

/**
 * A UUID's value from its hyphenated form (36 characters) or its base62
 * form (1 to 22, case-sensitive), as superscalar's ParseUUID reads it, or
 * undefined for any other text.
 */
export function uuidValue(text: string): bigint | undefined {
  if (text.length === 36 && HYPHENATED_UUID.test(text)) return BigInt(`0x${text.replaceAll('-', '')}`);
  if (text.length < 1 || text.length > 22) return undefined;
  let n = 0n;
  for (const c of text) {
    const digit = BASE62.indexOf(c);
    if (digit < 0) return undefined;
    n = n * 62n + BigInt(digit);
  }
  return n > MAX_UUID ? undefined : n;
}

/** A UUID's value as its lowercase hyphenated text. */
export function hyphenateUUID(n: bigint): string {
  const hex = n.toString(16).padStart(32, '0');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

/** A UUID's value as its base62 text, the scalar's canonical form. */
export function base62UUID(n: bigint): string {
  if (n === 0n) return '0';
  let out = '';
  let rest = n;
  while (rest > 0n) {
    out = BASE62[Number(rest % 62n)] + out;
    rest /= 62n;
  }
  return out;
}

/**
 * A UUID as its database form, lowercase hyphenated text, from either of
 * its forms: what a statement binds for a UUID column. ctx.principal's
 * subject, a base62 Identity.UUID, goes through it to match its user row.
 * Throws for a value that is no UUID.
 */
export function uuidToDb(value: unknown): string {
  const n = typeof value === 'string' ? uuidValue(value) : undefined;
  if (n === undefined) throw new Error(`${JSON.stringify(value)} is no UUID, hyphenated or base62`);
  return hyphenateUUID(n);
}

/**
 * A UUID a row holds, hyphenated text as Postgres returns it, as its wire
 * form, base62, as the TypeScript types and the wire hold Identity.UUID. A
 * base62 value passes through. Throws for a value that is no UUID.
 */
export function uuidFromDb(value: unknown): string {
  const n = typeof value === 'string' ? uuidValue(value) : undefined;
  if (n === undefined) throw new Error(`${JSON.stringify(value)} is no UUID, hyphenated or base62`);
  return base62UUID(n);
}

/** Whether text is a UUID's hyphenated form, as Postgres writes one. */
export function isHyphenatedUUID(text: string): boolean {
  return HYPHENATED_UUID.test(text);
}
