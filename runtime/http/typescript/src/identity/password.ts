import * as nodeCrypto from 'node:crypto';
import { backend } from 'superscalar/backend';
import { argon2ParamsProblem, type Argon2Params } from './config.js';

/*
Passwords: argon2id written as PHC strings, so every runtime verifies every
other's hashes (D50), and Auth.Password's rule, which the catalog scalar
carries and superscalar's binding checks, so every server and SDK checks one
rule. The hash is node:crypto's argon2, which Node.js 24 and Bun have; it
runs off the main thread.

A PHC string is read only in the one form this module writes:

  $argon2id$v=19$m=<memoryKiB>,t=<iterations>,p=<parallelism>$<salt>$<hash>

with the parameters as decimal integers without sign or leading zero, in
that order, and the salt (8 bytes or more) and the hash (4 bytes or more)
in standard base64 without padding. A cost argon2 does not take, or above
the config's bounds, is refused too, so a stored hash cannot make a login
run without end. Anything else is malformed and matches nothing.
*/

/** The sizes of the hashes this module writes. A hash another runtime wrote with other sizes still verifies, and is written again at login. */
export const SALT_BYTES = 16;
export const KEY_BYTES = 32;

/** The catalog scalar every password is: 8 to 128 characters (Unicode code points), with no composition rule. */
export const PASSWORD_SCALAR = 'Auth.Password';

/** argon2's version, 0x13. */
const ARGON2_VERSION = 19;

interface Argon2Options {
  message: Uint8Array;
  nonce: Uint8Array;
  parallelism: number;
  tagLength: number;
  memory: number;
  passes: number;
}

type Argon2 = (algorithm: 'argon2id', options: Argon2Options, callback: (error: Error | null, key: Uint8Array) => void) => void;

/** node:crypto's argon2, which @types/node 22 does not declare. */
function argon2Of(): Argon2 {
  const argon2 = (nodeCrypto as unknown as { argon2?: Argon2 }).argon2;
  if (typeof argon2 !== 'function') {
    throw new Error('identity: node:crypto has no argon2 here; the identity runtime needs Node.js 24.7 or later, or Bun');
  }
  return argon2;
}

function deriveKey(password: string, salt: Uint8Array, params: Argon2Params, length: number): Promise<Uint8Array> {
  const argon2 = argon2Of();
  return new Promise((resolve, reject) => {
    argon2(
      'argon2id',
      {
        message: new TextEncoder().encode(password),
        nonce: salt,
        parallelism: params.parallelism,
        tagLength: length,
        memory: params.memoryKiB,
        passes: params.iterations,
      },
      (error, key) => (error ? reject(error) : resolve(key))
    );
  });
}

/**
 * Whether a password is an Auth.Password, through superscalar's binding:
 * undefined when it is, else the scalar's message.
 */
export function passwordProblem(password: string): string | undefined {
  try {
    backend.validate(PASSWORD_SCALAR, password);
    return undefined;
  } catch (error) {
    return error instanceof Error ? error.message : String(error);
  }
}

/** Whether a password is an Auth.Password. */
export function isPassword(password: string): boolean {
  return passwordProblem(password) === undefined;
}

/**
 * Hashes a password with argon2id at params and a fresh 16-byte salt, and
 * writes the PHC string. The password is hashed as its UTF-8 bytes, with no
 * normalization.
 */
export function hashPassword(password: string, params: Argon2Params): Promise<string> {
  return hashPasswordWithSalt(password, nodeCrypto.randomBytes(SALT_BYTES), params);
}

/** hashPassword with the caller's salt. It is deterministic, for the parity vectors; a login never reuses a salt. */
export async function hashPasswordWithSalt(password: string, salt: Uint8Array, params: Argon2Params): Promise<string> {
  const key = await deriveKey(password, salt, params, KEY_BYTES);
  return encodePHC(params, salt, key);
}

/** What verifying a password against a stored hash found. */
export interface PasswordVerification {
  /** The stored string is not one this module reads, so it matches nothing. */
  readonly malformed: boolean;
  /** The password matches, compared in constant time. */
  readonly ok: boolean;
  /** Only when ok: the hash's cost differs from the current one, or its salt or hash are not the sizes this module writes. */
  readonly rehash: boolean;
}

/** Verifies a password against the PHC string phc, and says whether to write the hash again at current's cost. */
export async function verifyPassword(phc: string, password: string, current: Argon2Params): Promise<PasswordVerification> {
  const parsed = parsePHC(phc);
  if (!parsed) return { malformed: true, ok: false, rehash: false };
  const key = await deriveKey(password, parsed.salt, parsed.params, parsed.key.length);
  if (!nodeCrypto.timingSafeEqual(key, parsed.key)) return { malformed: false, ok: false, rehash: false };
  const { params } = parsed;
  const rehash =
    params.memoryKiB !== current.memoryKiB ||
    params.iterations !== current.iterations ||
    params.parallelism !== current.parallelism ||
    parsed.salt.length !== SALT_BYTES ||
    parsed.key.length !== KEY_BYTES;
  return { malformed: false, ok: true, rehash };
}

function encodePHC(params: Argon2Params, salt: Uint8Array, key: Uint8Array): string {
  return `$argon2id$v=${ARGON2_VERSION}$m=${params.memoryKiB},t=${params.iterations},p=${params.parallelism}$${rawBase64(salt)}$${rawBase64(key)}`;
}

/** Standard base64 without padding. */
function rawBase64(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString('base64').replace(/=+$/u, '');
}

/**
 * Decodes standard base64 without padding, strictly: the standard alphabet
 * alone, no padding, and zero bits past the last byte. Undefined otherwise.
 */
function decodeRawBase64(text: string): Uint8Array | undefined {
  if (!/^[A-Za-z0-9+/]*$/u.test(text) || text.length % 4 === 1) return undefined;
  const bytes = Buffer.from(text, 'base64');
  if (rawBase64(bytes) !== text) return undefined;
  return new Uint8Array(bytes);
}

/** Whether s is a decimal integer without a sign or a leading zero. */
function isDecimal(s: string): boolean {
  return /^(?:0|[1-9][0-9]*)$/u.test(s);
}

interface ParsedPHC {
  params: Argon2Params;
  salt: Uint8Array;
  key: Uint8Array;
}

function parsePHC(phc: string): ParsedPHC | undefined {
  const fields = phc.split('$');
  if (fields.length !== 6 || fields[0] !== '' || fields[1] !== 'argon2id' || fields[2] !== `v=${ARGON2_VERSION}`) return undefined;
  const params = fields[3]!.split(',');
  if (params.length !== 3) return undefined;
  const values: number[] = [];
  for (const [i, name] of ['m', 't', 'p'].entries()) {
    const param = params[i]!;
    const eq = param.indexOf('=');
    if (eq < 0 || param.slice(0, eq) !== name) return undefined;
    const value = param.slice(eq + 1);
    // Go reads each into a uint32.
    if (!isDecimal(value) || value.length > 10 || Number(value) > 0xffff_ffff) return undefined;
    values.push(Number(value));
  }
  const cost: Argon2Params = { memoryKiB: values[0]!, iterations: values[1]!, parallelism: values[2]! };
  if (argon2ParamsProblem(cost)) return undefined;
  const salt = decodeRawBase64(fields[4]!);
  if (!salt || salt.length < 8) return undefined;
  const key = decodeRawBase64(fields[5]!);
  if (!key || key.length < 4) return undefined;
  return { params: cost, salt, key };
}

/**
 * A hash of a random password at params, which a login for an unknown
 * account verifies against, so its timing does not tell it from a wrong
 * password.
 */
export function dummyHash(params: Argon2Params): Promise<string> {
  return hashPassword(rawBase64(nodeCrypto.randomBytes(32)), params);
}
