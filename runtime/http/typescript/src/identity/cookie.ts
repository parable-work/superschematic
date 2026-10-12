import type { IdentityConfig } from './config.js';

/*
The session cookie a cookie login sets, and the one logout sets to clear it,
written as Go's net/http writes them, attributes in this order:

  <name>=<token>; Path=/; Domain=<domain>; Max-Age=<seconds>; HttpOnly; Secure; SameSite=<sameSite>

Domain only when the config names one, Secure only when the cookie is
Secure, and never Expires.
*/

/**
 * The Set-Cookie value of a cookie login: the token, with Max-Age the whole
 * seconds left in the session (0 once none is left).
 */
export function sessionCookie(config: IdentityConfig, token: string, maxAgeSeconds: number): string {
  return cookieOf(config, token, Math.max(0, Math.floor(maxAgeSeconds)));
}

/** The Set-Cookie value logout answers: no value and Max-Age=0, which a browser takes to delete the cookie. */
export function clearCookie(config: IdentityConfig): string {
  return cookieOf(config, '', 0);
}

function cookieOf(config: IdentityConfig, value: string, maxAge: number): string {
  const { cookie } = config;
  const parts = [`${cookie.name}=${value}`, 'Path=/'];
  if (cookie.domain !== '') parts.push(`Domain=${cookie.domain}`);
  parts.push(`Max-Age=${maxAge}`, 'HttpOnly');
  if (cookie.secure) parts.push('Secure');
  parts.push(`SameSite=${cookie.sameSite}`);
  return parts.join('; ');
}
