import { parseGoURL } from './url.js';

/*
The identity config, the JSON the Go, TypeScript and Rust identity runtimes
read the same way (D50): the session's lifetime, idle timeout and touch
interval, the session cookie, the origins a cookie request may come from,
and the password hash's cost. Every member is optional and null takes its
default; parseIdentityConfig refuses unknown members at every level, a
number that is not an integer, and anything the rules below refuse, and
returns the config the runtime runs with, every member present.
runtime/http/testdata/README.md states the rules the parity vectors pin.
*/

/** The config's defaults. */
export const DEFAULT_SESSION_TTL_SECONDS = 14 * 24 * 60 * 60;
export const DEFAULT_TOUCH_INTERVAL_SECONDS = 60;
export const DEFAULT_SAME_SITE = 'Lax';
export const DEFAULT_ARGON2_MEMORY_KIB = 19456;
export const DEFAULT_ARGON2_ITERATIONS = 2;
export const DEFAULT_ARGON2_PARALLELISM = 1;

/**
 * The session cookie's names: __Host-session by default, __Secure-session
 * when the cookie names a domain, and session when it is not Secure, as on
 * the local target over plain HTTP.
 */
export const HOST_COOKIE_NAME = '__Host-session';
export const SECURE_COOKIE_NAME = '__Secure-session';
export const PLAIN_COOKIE_NAME = 'session';

/** The most memory a config may ask of every login: 4 GiB. */
const MAX_ARGON2_MEMORY_KIB = 4 * 1024 * 1024;

/** The largest value Go reads into the cost's uint32 members. */
const MAX_UINT32 = 0xffff_ffff;

export type SameSite = 'Lax' | 'Strict' | 'None';

/** An argon2id cost: memory in KiB, passes and lanes. */
export interface Argon2Params {
  readonly memoryKiB: number;
  readonly iterations: number;
  readonly parallelism: number;
}

/** The config the runtime runs with: every default filled in, the cookie's name resolved. */
export interface IdentityConfig {
  /** How long a session lasts after login. */
  readonly sessionTtlSeconds: number;
  /** A session not seen for this long ends; 0 is no idle timeout. */
  readonly idleTimeoutSeconds: number;
  /** How often a request writes a session's lastSeenAt, at most; 0 writes it on every request. */
  readonly touchIntervalSeconds: number;
  readonly cookie: {
    readonly name: string;
    /** The cookie's Domain attribute, so APIs on sibling hosts share the session; empty for none. */
    readonly domain: string;
    readonly secure: boolean;
    readonly sameSite: SameSite;
  };
  /** The origins (scheme://host[:port]) a cookie request may come from across origins, and the only ones CORS answers. */
  readonly trustedOrigins: readonly string[];
  readonly password: { readonly argon2: Argon2Params };
}

/** The JSON a deployment writes: every member optional, null for its default. */
export interface IdentityConfigInput {
  sessionTtlSeconds?: number | null;
  idleTimeoutSeconds?: number | null;
  touchIntervalSeconds?: number | null;
  cookie?: {
    name?: string | null;
    domain?: string | null;
    secure?: boolean | null;
    sameSite?: string | null;
  } | null;
  trustedOrigins?: readonly (string | null)[] | null;
  password?: { argon2?: { memoryKiB?: number | null; iterations?: number | null; parallelism?: number | null } | null } | null;
}

/** A config the runtime refuses, with every reason. */
export class IdentityConfigError extends Error {
  constructor(readonly problems: readonly string[]) {
    super(`identity: config: ${problems.join('; ')}`);
    this.name = 'IdentityConfigError';
  }
}

type JsonObject = Record<string, unknown>;

function isObject(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** Reads a config from its JSON text. A number with a fraction or an exponent is refused, as Go refuses 1.0 for an integer. */
export function parseIdentityConfigJSON(text: string): IdentityConfig {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch (error) {
    throw new IdentityConfigError([`not JSON: ${(error as Error).message}`]);
  }
  if (hasNonIntegerLexeme(text)) {
    throw new IdentityConfigError(['every number in the config is an integer, written without a fraction or an exponent']);
  }
  return parseIdentityConfig(value);
}

/** Whether JSON text holds a number written with a fraction or an exponent. */
function hasNonIntegerLexeme(text: string): boolean {
  let inString = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (inString) {
      if (c === '\\') i++;
      else if (c === '"') inString = false;
      continue;
    }
    if (c === '"') {
      inString = true;
      continue;
    }
    if (c === '-' || (c !== undefined && c >= '0' && c <= '9')) {
      let j = i;
      while (j < text.length && /[-+0-9.eE]/u.test(text[j]!)) j++;
      if (/[.eE]/u.test(text.slice(i, j))) return true;
      i = j - 1;
    }
  }
  return false;
}

/**
 * Reads a config from its JSON value (null is the empty config). It refuses
 * unknown members, a number that is not an integer, and every rule the
 * runtimes share, naming each problem; otherwise it returns the config with
 * every default filled in.
 */
export function parseIdentityConfig(input: unknown): IdentityConfig {
  const problems: string[] = [];
  const add = (problem: string) => problems.push(problem);
  // No argument is every default; a null config is refused, as an object
  // is what a deployment writes.
  const root = objectOf(input === undefined ? {} : input, 'the config', add);

  const integer = (from: JsonObject | undefined, path: string, name: string, max = Number.MAX_SAFE_INTEGER, min = Number.MIN_SAFE_INTEGER): number | undefined => {
    const value = from?.[name];
    if (value === undefined || value === null) return undefined;
    if (typeof value !== 'number' || !Number.isInteger(value)) {
      add(`${path}${name} must be an integer`);
      return undefined;
    }
    if (value < min || value > max) {
      add(`${path}${name} is out of range`);
      return undefined;
    }
    return value;
  };
  const text = (from: JsonObject | undefined, path: string, name: string): string => {
    const value = from?.[name];
    if (value === undefined || value === null) return '';
    if (typeof value !== 'string') {
      add(`${path}${name} must be a string`);
      return '';
    }
    return value;
  };

  checkMembers(root, '', ['sessionTtlSeconds', 'idleTimeoutSeconds', 'touchIntervalSeconds', 'cookie', 'trustedOrigins', 'password'], add);
  const ttl = integer(root, '', 'sessionTtlSeconds');
  const idle = integer(root, '', 'idleTimeoutSeconds');
  const touch = integer(root, '', 'touchIntervalSeconds');

  const cookieInput = root?.cookie === null || root?.cookie === undefined ? undefined : objectOf(root.cookie, 'cookie', add);
  checkMembers(cookieInput, 'cookie.', ['name', 'domain', 'secure', 'sameSite'], add);
  const cookieName = text(cookieInput, 'cookie.', 'name');
  const domain = text(cookieInput, 'cookie.', 'domain');
  const sameSiteInput = text(cookieInput, 'cookie.', 'sameSite');
  let secure = true;
  const secureInput = cookieInput?.secure;
  if (secureInput !== undefined && secureInput !== null) {
    if (typeof secureInput !== 'boolean') add('cookie.secure must be a boolean');
    else secure = secureInput;
  }

  const trustedOrigins: string[] = [];
  const originsInput = root?.trustedOrigins;
  if (originsInput !== undefined && originsInput !== null) {
    if (!Array.isArray(originsInput)) {
      add('trustedOrigins must be a list of strings');
    } else {
      for (const origin of originsInput) {
        if (origin !== null && typeof origin !== 'string') add('trustedOrigins must be a list of strings');
        else trustedOrigins.push(origin ?? '');
      }
    }
  }

  const passwordInput = root?.password === null || root?.password === undefined ? undefined : objectOf(root.password, 'password', add);
  checkMembers(passwordInput, 'password.', ['argon2'], add);
  const argon2Input =
    passwordInput?.argon2 === null || passwordInput?.argon2 === undefined ? undefined : objectOf(passwordInput.argon2, 'password.argon2', add);
  checkMembers(argon2Input, 'password.argon2.', ['memoryKiB', 'iterations', 'parallelism'], add);
  const memoryKiB = integer(argon2Input, 'password.argon2.', 'memoryKiB', MAX_UINT32, 0);
  const iterations = integer(argon2Input, 'password.argon2.', 'iterations', MAX_UINT32, 0);
  const parallelism = integer(argon2Input, 'password.argon2.', 'parallelism', MAX_UINT32, 0);

  // The rules, as Go's Config.Validate states them.
  if (ttl !== undefined && ttl <= 0) add(`sessionTtlSeconds must be positive, not ${ttl}`);
  if (idle !== undefined && idle < 0) add(`idleTimeoutSeconds must not be negative, not ${idle}`);
  if (touch !== undefined && touch < 0) add(`touchIntervalSeconds must not be negative, not ${touch}`);
  const idleSeconds = idle ?? 0;
  const touchSeconds = touch ?? DEFAULT_TOUCH_INTERVAL_SECONDS;
  if (idleSeconds > 0 && touchSeconds >= idleSeconds) {
    add(
      `touchIntervalSeconds (${touchSeconds}) must be less than idleTimeoutSeconds (${idleSeconds}), or a session idles out between two writes of lastSeenAt`
    );
  }

  let sameSite: SameSite = DEFAULT_SAME_SITE;
  switch (sameSiteInput) {
    case '':
      break;
    case 'Lax':
    case 'Strict':
      sameSite = sameSiteInput;
      break;
    case 'None':
      sameSite = sameSiteInput;
      if (!secure) add('cookie.sameSite None needs cookie.secure, which browsers require of it');
      break;
    default:
      add(`cookie.sameSite must be Lax, Strict or None, not ${JSON.stringify(sameSiteInput)}`);
  }
  if (domain !== '' && !isCookieDomain(domain)) {
    add(
      `cookie.domain ${JSON.stringify(domain)} is not a domain name (letters, digits and hyphens in dot-separated labels, with no leading dot, port or scheme)`
    );
  }
  if (cookieName !== '') {
    if (!isCookieName(cookieName)) add(`cookie.name ${JSON.stringify(cookieName)} is not a cookie name`);
    else if (cookieName.startsWith('__Host-') && (!secure || domain !== ''))
      add(`cookie.name ${JSON.stringify(cookieName)} has the __Host- prefix, which needs cookie.secure and no cookie.domain`);
    else if (cookieName.startsWith('__Secure-') && !secure)
      add(`cookie.name ${JSON.stringify(cookieName)} has the __Secure- prefix, which needs cookie.secure`);
  }

  trustedOrigins.forEach((origin, i) => {
    const problem = originProblem(origin);
    if (problem) add(`trustedOrigins[${i}]: ${problem}`);
  });

  const argon2: Argon2Params = {
    memoryKiB: memoryKiB ?? DEFAULT_ARGON2_MEMORY_KIB,
    iterations: iterations ?? DEFAULT_ARGON2_ITERATIONS,
    parallelism: parallelism ?? DEFAULT_ARGON2_PARALLELISM,
  };
  const argon2Problem = argon2ParamsProblem(argon2);
  if (argon2Problem) add(argon2Problem);

  if (problems.length > 0) throw new IdentityConfigError(problems);
  return {
    sessionTtlSeconds: ttl ?? DEFAULT_SESSION_TTL_SECONDS,
    idleTimeoutSeconds: idleSeconds,
    touchIntervalSeconds: touchSeconds,
    cookie: { name: cookieNameOf(cookieName, domain, secure), domain, secure, sameSite },
    trustedOrigins,
    password: { argon2 },
  };
}

/** The reason argon2 does not take a cost, as a config states it; undefined when it does. */
export function argon2ParamsProblem(a: Argon2Params): string | undefined {
  if (a.iterations < 1) return 'password.argon2.iterations must be at least 1';
  if (a.parallelism < 1 || a.parallelism > 255) return `password.argon2.parallelism must be 1 to 255, not ${a.parallelism}`;
  if (a.memoryKiB < 8 * a.parallelism)
    return `password.argon2.memoryKiB must be at least 8 times parallelism (${8 * a.parallelism}), not ${a.memoryKiB}`;
  if (a.memoryKiB > MAX_ARGON2_MEMORY_KIB) return `password.argon2.memoryKiB must be at most ${MAX_ARGON2_MEMORY_KIB} (4 GiB), not ${a.memoryKiB}`;
  return undefined;
}

/** The cookie's name: the configured one, else session when not Secure, __Secure-session with a domain, and __Host-session. */
function cookieNameOf(name: string, domain: string, secure: boolean): string {
  if (name !== '') return name;
  if (!secure) return PLAIN_COOKIE_NAME;
  if (domain !== '') return SECURE_COOKIE_NAME;
  return HOST_COOKIE_NAME;
}

function objectOf(value: unknown, what: string, add: (problem: string) => void): JsonObject | undefined {
  if (isObject(value)) return value;
  add(`${what} must be a JSON object`);
  return undefined;
}

function checkMembers(object: JsonObject | undefined, path: string, known: readonly string[], add: (problem: string) => void): void {
  if (!object) return;
  for (const member of Object.keys(object)) {
    if (!known.includes(member)) add(`unknown member ${path}${member}`);
  }
}

/**
 * The reason an origin is not scheme://host[:port], the form a browser's
 * Origin header has, as Go's url.Parse reads it: no path (a trailing slash
 * included), query, fragment or user. Undefined for an origin.
 */
export function originProblem(origin: string): string | undefined {
  const u = parseGoURL(origin);
  if (!u) return `${JSON.stringify(origin)} is not a URL`;
  if (u.scheme === '' || u.host === '' || u.opaque !== '') return `${JSON.stringify(origin)} is not scheme://host[:port]`;
  if (u.hasUser) return `${JSON.stringify(origin)} has a user, which an origin never does`;
  if (u.path !== '' || u.rawQuery !== '' || u.fragment !== '' || /[?#]/u.test(origin)) {
    return `${JSON.stringify(origin)} has a path, query or fragment, which an origin never does`;
  }
  return undefined;
}

/** Whether name is an RFC 6265 cookie name: a token of visible ASCII without separators. */
export function isCookieName(name: string): boolean {
  if (name === '') return false;
  for (let i = 0; i < name.length; i++) {
    const c = name.charCodeAt(i);
    if (c <= 0x20 || c >= 0x7f || '()<>@,;:\\"/[]?={}'.includes(name[i]!)) return false;
  }
  return true;
}

/**
 * Whether domain is a name a Domain attribute takes: dot-separated labels of
 * letters, digits and hyphens, no label empty, starting or ending with a
 * hyphen, or longer than 63 bytes, and 253 bytes at most.
 */
export function isCookieDomain(domain: string): boolean {
  if (domain.length > 253) return false;
  return domain.split('.').every(label => label.length > 0 && label.length <= 63 && /^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$/u.test(label));
}
