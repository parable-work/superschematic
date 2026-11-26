/*
A field envelope's seal's or open's failure (D85), apart from the cipher in
./fieldcrypt, which imports node:crypto: the repositories' runtime throws it
for a table whose Encrypted<T> field it has no keys for, and a Worker
bundles that runtime over D1, which has no node:crypto (D182).
*/

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
