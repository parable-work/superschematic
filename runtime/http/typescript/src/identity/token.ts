import { createHash, randomBytes } from 'node:crypto';

/*
Session tokens and the credential a request carries (D50). A token is 32
random bytes in base64url without padding; the database keeps the
lowercase hexadecimal SHA-256 of its text. A request's credential is its
Authorization header when it has one, which must then be a usable bearer
token, and the session cookie only without one: an Authorization that
fails is refused, with no fallback to the cookie.
*/

/** The size of a session token's random value. Its text is TOKEN_LENGTH characters of base64url without padding. */
export const TOKEN_BYTES = 32;
export const TOKEN_LENGTH = 43;

/** How a session travels: the Authorization header or the cookie. It is also LoginInput's session member. */
export type Transport = 'bearer' | 'cookie';

/** Returns a new session token: TOKEN_BYTES random bytes from random (node:crypto's when absent), in base64url without padding. */
export function newToken(random: (size: number) => Uint8Array = randomBytes): string {
  const bytes = random(TOKEN_BYTES);
  if (bytes.length !== TOKEN_BYTES) throw new Error(`identity: a token needs ${TOKEN_BYTES} random bytes, not ${bytes.length}`);
  return Buffer.from(bytes).toString('base64url');
}

/** The hash a session's row keeps of its token: the lowercase hexadecimal SHA-256 of the token's text, a Crypto.SHA256. */
export function hashToken(token: string): string {
  return createHash('sha256').update(token, 'utf8').digest('hex');
}

/** Whether s has a token's shape: TOKEN_LENGTH characters of the base64url alphabet. */
export function isToken(s: string): boolean {
  return s.length === TOKEN_LENGTH && /^[A-Za-z0-9_-]*$/u.test(s);
}

/** What extractCredential found on a request. */
export type CredentialOutcome =
  /** A token in the Authorization header or the cookie. */
  | { readonly outcome: 'usable'; readonly transport: Transport; readonly token: string }
  /**
   * An Authorization header that is not a usable bearer token, or, without
   * one, a session cookie whose value is not a token: 401, with no fallback
   * to the cookie.
   */
  | { readonly outcome: 'invalid'; readonly transport: Transport; readonly token: null }
  /** No Authorization header and no session cookie. */
  | { readonly outcome: 'none'; readonly transport: null; readonly token: null };

/**
 * A request's headers, in order, a name repeated when the request repeats
 * it. A Headers object joins a repeated header into one value, as fetch
 * does, which still refuses two Authorization headers.
 */
export type HeaderSource = Headers | Iterable<readonly [string, string]>;

function headerValues(headers: HeaderSource, name: string): string[] {
  const wanted = name.toLowerCase();
  const values: string[] = [];
  for (const [key, value] of headers as Iterable<readonly [string, string]>) {
    if (key.toLowerCase() === wanted) values.push(value);
  }
  return values;
}

/**
 * Reads a request's session credential. Authorization comes first: when the
 * request has one it is the credential, and the cookie is not read. It is
 * usable when it appears once and is the scheme Bearer in any case, one or
 * more spaces, and a token; anything else is invalid. Without Authorization
 * the first cookie named cookieName (compared exactly) across every Cookie
 * header is the credential, usable when its value is a token.
 */
export function extractCredential(headers: HeaderSource, cookieName: string): CredentialOutcome {
  const authorization = headerValues(headers, 'authorization');
  if (authorization.length > 0) {
    const token = bearerToken(authorization[0]!);
    if (token === undefined || authorization.length !== 1) return { outcome: 'invalid', transport: 'bearer', token: null };
    return { outcome: 'usable', transport: 'bearer', token };
  }
  const value = readCookie(headerValues(headers, 'cookie'), cookieName);
  if (value === undefined) return { outcome: 'none', transport: null, token: null };
  if (!isToken(value)) return { outcome: 'invalid', transport: 'cookie', token: null };
  return { outcome: 'usable', transport: 'cookie', token: value };
}

/** Reads "Bearer <token>": the scheme in any case, one or more spaces, and a token. */
function bearerToken(value: string): string | undefined {
  const space = value.indexOf(' ');
  if (space < 0 || value.slice(0, space).toLowerCase() !== 'bearer') return undefined;
  const token = value.slice(space + 1).replace(/^ +/u, '');
  return isToken(token) ? token : undefined;
}

// What Go's net/http reads of Cookie headers: each header is split on ';',
// each part trimmed of ASCII space and split at its first '='; a part whose
// name is not a token is skipped, as is one whose value (unquoted when it
// is in double quotes) has a byte a cookie value may not, and past 3000
// cookies none is read.
const MAX_COOKIES = 3000;

function trimASCIISpace(s: string): string {
  return s.replace(/^[ \t\r\n]+|[ \t\r\n]+$/gu, '');
}

function isHeaderToken(s: string): boolean {
  return /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/u.test(s);
}

function cookieValue(raw: string): string | undefined {
  const value = raw.length > 1 && raw.startsWith('"') && raw.endsWith('"') ? raw.slice(1, -1) : raw;
  for (let i = 0; i < value.length; i++) {
    const c = value.charCodeAt(i);
    if (c < 0x20 || c >= 0x7f || c === 0x22 || c === 0x3b || c === 0x5c) return undefined;
  }
  return value;
}

/** The value of the first cookie named name across the Cookie headers, as Go's Request.Cookie reads it; undefined for none. */
function readCookie(lines: readonly string[], name: string): string | undefined {
  let count = 0;
  for (const line of lines) count += line.split(';').length;
  if (count > MAX_COOKIES) return undefined;
  for (const line of lines) {
    for (const rawPart of trimASCIISpace(line).split(';')) {
      const part = trimASCIISpace(rawPart);
      if (part === '') continue;
      const eq = part.indexOf('=');
      const partName = trimASCIISpace(eq < 0 ? part : part.slice(0, eq));
      if (!isHeaderToken(partName) || partName !== name) continue;
      const value = cookieValue(eq < 0 ? '' : part.slice(eq + 1));
      if (value !== undefined) return value;
    }
  }
  return undefined;
}
