import { createCipheriv, createDecipheriv, createHash, randomBytes, randomUUID } from 'node:crypto';
import { revealed, type Revealable } from './secret.js';

/*
The envelopes of a DB table's Encrypted<T> fields (D85), which the
repositories seal on write and open on read, byte for byte as the Go ORM
does through runtime/schema/go/fieldcrypt: a test of both reads one set of
vectors (runtime/schema/testdata/fieldcrypt_parity.json).

A value is sealed with AES-256-GCM under the keyring's current key, with a
random 12-byte nonce, and stored as

  v1:<key id>:<nonce>:<ciphertext and tag>

the nonce and the ciphertext in unpadded base64url. The additional data
binds it to its cell: the table type, the field, and the row's key as text,
so a ciphertext copied to another row or column does not open.

A keyring is the text of the DB service's key secret: one or more 32-byte
keys in base64, separated by commas. The first seals; each opens the
envelopes that name its id, the first 8 hex digits of a SHA-256 over it.
*/

/** The envelope's version, its first part. */
export const FIELD_ENVELOPE_VERSION = 'v1';

const KEY_SIZE = 32;
const NONCE_SIZE = 12;
const TAG_SIZE = 16;

/** Why a seal or an open failed, as the Go runtime's errors name it. */
export type FieldEncryptionReason = 'no-keys' | 'bad-keys' | 'unknown-key' | 'decrypt' | 'malformed';

const REASONS: Record<FieldEncryptionReason, string> = {
  'no-keys': "no field keys: open the ORM with the DB service's key secret",
  'bad-keys': 'the field keys are not one or more 32-byte keys in base64, separated by commas',
  'unknown-key': "no field key has the envelope's key id",
  decrypt: 'the envelope does not open for its row and column',
  malformed: 'the value is no field envelope',
};

/** A seal's or an open's failure: the table type and field, never the value or the key. */
export class FieldEncryptionError extends Error {
  readonly reason: FieldEncryptionReason;
  readonly table: string | undefined;
  readonly field: string | undefined;

  constructor(reason: FieldEncryptionReason, cell?: { readonly table: string; readonly field: string }, detail?: string) {
    const what = REASONS[reason] + (detail ? `: ${detail}` : '');
    super(cell ? `fieldcrypt: ${cell.table}.${cell.field}: ${what}` : `fieldcrypt: ${what}`);
    this.name = 'FieldEncryptionError';
    this.reason = reason;
    this.table = cell?.table;
    this.field = cell?.field;
  }
}

/** What an envelope is bound to: the table type and field, as the schema names them, and the row's key as text. */
export interface FieldCell {
  readonly table: string;
  readonly field: string;
  readonly row: string;
}

function additionalData(cell: FieldCell): Buffer {
  return Buffer.from(`superschematic field ${FIELD_ENVELOPE_VERSION}\0${cell.table}\0${cell.field}\0${cell.row}`, 'utf8');
}

/** The id of a 32-byte key: the first 8 hex digits of a SHA-256 over it. */
export function fieldKeyId(raw: Uint8Array): string {
  return createHash('sha256').update(Buffer.from('superschematic field key\0', 'utf8')).update(raw).digest('hex').slice(0, 8);
}

const BASE64_KEY = /^[A-Za-z0-9+/]{43}(=)?$/u;
const BASE64URL = /^[A-Za-z0-9_-]*$/u;

function decodeKey(text: string, n: number): Buffer {
  if (text === '') throw new FieldEncryptionError('bad-keys', undefined, `key ${n} is empty`);
  // Buffer.from reads any text as base64; the pattern holds it to 32 bytes
  // of the standard alphabet, padded or not, as the Go runtime reads it.
  if (!BASE64_KEY.test(text)) throw new FieldEncryptionError('bad-keys', undefined, `key ${n} is not 32 bytes in base64`);
  const raw = Buffer.from(text, 'base64');
  if (raw.length !== KEY_SIZE || raw.toString('base64').replace(/=+$/u, '') !== text.replace(/=+$/u, '')) {
    throw new FieldEncryptionError('bad-keys', undefined, `key ${n} is not 32 bytes in base64`);
  }
  return raw;
}

function decodeBase64Url(text: string): Buffer | undefined {
  if (!BASE64URL.test(text) || text.length % 4 === 1) return undefined;
  const raw = Buffer.from(text, 'base64url');
  return raw.toString('base64url') === text ? raw : undefined;
}

/**
 * The keys a DB service's key secret names: the first seals, and each
 * opens the envelopes sealed under it. Build it with parseFieldKeys.
 */
export class FieldKeyring {
  readonly #keys: ReadonlyMap<string, Buffer>;
  readonly #current: string;

  private constructor(keys: ReadonlyMap<string, Buffer>, current: string) {
    this.#keys = keys;
    this.#current = current;
  }

  /** Reads a key secret: one or more 32-byte keys in base64, with or without padding, separated by commas, the first the current one. */
  static parse(secret: string): FieldKeyring {
    const keys = new Map<string, Buffer>();
    let current = '';
    secret.split(',').forEach((part, i) => {
      const raw = decodeKey(part.trim(), i + 1);
      const id = fieldKeyId(raw);
      if (keys.has(id)) throw new FieldEncryptionError('bad-keys', undefined, `key ${i + 1} repeats the key id ${id}`);
      keys.set(id, raw);
      if (current === '') current = id;
    });
    return new FieldKeyring(keys, current);
  }

  /** The id of the key that seals. */
  get currentId(): string {
    return this.#current;
  }

  /** The start of every envelope the current key seals, as a LIKE pattern. */
  get currentPrefix(): string {
    return `${FIELD_ENVELOPE_VERSION}:${this.#current}:%`;
  }

  /** Whether envelope was sealed under the current key. */
  isCurrent(envelope: string): boolean {
    return envelope.startsWith(`${FIELD_ENVELOPE_VERSION}:${this.#current}:`);
  }

  /** Encrypts plaintext for cell under the current key; nonce is for tests alone. */
  seal(cell: FieldCell, plaintext: string, nonce: Uint8Array = randomBytes(NONCE_SIZE)): string {
    const cipher = createCipheriv('aes-256-gcm', this.#keys.get(this.#current)!, nonce, { authTagLength: TAG_SIZE });
    cipher.setAAD(additionalData(cell));
    const sealed = Buffer.concat([cipher.update(plaintext, 'utf8'), cipher.final(), cipher.getAuthTag()]);
    return `${FIELD_ENVELOPE_VERSION}:${this.#current}:${Buffer.from(nonce).toString('base64url')}:${sealed.toString('base64url')}`;
  }

  /** Decrypts the envelope of cell, under the key its id names. */
  open(cell: FieldCell, envelope: string): string {
    const parts = envelope.split(':');
    if (parts.length !== 4 || parts[0] !== FIELD_ENVELOPE_VERSION) throw new FieldEncryptionError('malformed', cell);
    const nonce = decodeBase64Url(parts[2]!);
    const sealed = decodeBase64Url(parts[3]!);
    if (!nonce || nonce.length !== NONCE_SIZE || !sealed) throw new FieldEncryptionError('malformed', cell);
    const key = this.#keys.get(parts[1]!);
    if (!key) throw new FieldEncryptionError('unknown-key', cell);
    if (sealed.length < TAG_SIZE) throw new FieldEncryptionError('decrypt', cell);
    try {
      const decipher = createDecipheriv('aes-256-gcm', key, nonce, { authTagLength: TAG_SIZE });
      decipher.setAAD(additionalData(cell));
      decipher.setAuthTag(sealed.subarray(sealed.length - TAG_SIZE));
      return Buffer.concat([decipher.update(sealed.subarray(0, sealed.length - TAG_SIZE)), decipher.final()]).toString('utf8');
    } catch {
      throw new FieldEncryptionError('decrypt', cell);
    }
  }
}

/** The keyring of a key secret: its text, a SecretValue of it (an env config's), or a keyring already read. */
export type FieldKeys = string | Revealable<string> | FieldKeyring;

/** Reads keys into a keyring; FieldEncryptionError('bad-keys') for a malformed secret. */
export function parseFieldKeys(keys: FieldKeys): FieldKeyring {
  if (keys instanceof FieldKeyring) return keys;
  return FieldKeyring.parse(revealed(keys));
}

/** A random version 4 UUID, hyphenated: the key the repositories give a row of a table with an encrypted field when its create leaves the key unset. */
export function newRowKey(): string {
  return randomUUID();
}
