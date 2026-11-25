import { HttpProblem } from './problem.js';
import { StackConfigError, type StackEnv } from './stackconfig.js';

/*
The oldest version of each client an API serves (D143).

A generated SDK whose caller names its client sends
`X-Client-Version: <name>/<version>` on every call. An API whose config
says `clientVersions: true` reads MIN_CLIENT_VERSIONS, a list such as
`cli=0.5.0,desktop=2.1.0`, and answers a request whose header names a
listed client at a lower version with 426 and the problem
`client_too_old`, whose details hold the client, its version and the
minimum. Versions compare as semantic versions (semver.org 2.0.0). A
request with no header, a client the list does not name, or a version that
is no semantic version passes: a browser sends none.

The Go and Rust runtimes read the setting and decide each request alike
(runtime/http/testdata/client_version_parity.json).
*/

/** The request header a client names itself in. */
export const CLIENT_VERSION_HEADER = 'X-Client-Version';
/** The environment variable that lists the minimums. */
export const MIN_CLIENT_VERSIONS_SETTING = 'MIN_CLIENT_VERSIONS';
/** The problem code of a client that is too old. */
export const CLIENT_TOO_OLD = 'client_too_old';

/** semver.org's own pattern of a semantic version 2.0.0. */
const SEMVER =
  /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$/;

/** A client's name: a letter or a digit, then up to 63 more of letters, digits, `.`, `_` and `-`. */
const CLIENT_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

interface SemanticVersion {
  /** Major, minor and patch, each as its digits. */
  readonly core: readonly [string, string, string];
  /** The pre-release identifiers, none for a release. */
  readonly pre: readonly string[];
}

function parseVersion(text: string): SemanticVersion | undefined {
  const match = SEMVER.exec(text);
  if (!match) return undefined;
  return { core: [match[1]!, match[2]!, match[3]!], pre: match[4] ? match[4].split('.') : [] };
}

/** Compares two numbers written as digits without leading zeros, of any length. */
function compareNumbers(a: string, b: string): number {
  if (a.length !== b.length) return a.length < b.length ? -1 : 1;
  return a < b ? -1 : a > b ? 1 : 0;
}

const isNumber = (identifier: string): boolean => /^[0-9]+$/.test(identifier);

function compareParsed(a: SemanticVersion, b: SemanticVersion): number {
  for (let i = 0; i < 3; i++) {
    const c = compareNumbers(a.core[i]!, b.core[i]!);
    if (c !== 0) return c;
  }
  if (a.pre.length === 0 || b.pre.length === 0) return a.pre.length === b.pre.length ? 0 : a.pre.length === 0 ? 1 : -1;
  for (let i = 0; i < a.pre.length && i < b.pre.length; i++) {
    const left = a.pre[i]!;
    const right = b.pre[i]!;
    let c: number;
    if (isNumber(left) && isNumber(right)) c = compareNumbers(left, right);
    else if (isNumber(left)) c = -1;
    else if (isNumber(right)) c = 1;
    // Identifiers are ASCII, so code units order them as their bytes do.
    else c = left < right ? -1 : left > right ? 1 : 0;
    if (c !== 0) return c;
  }
  // Every shared identifier is equal: the longer list is the newer.
  return a.pre.length < b.pre.length ? -1 : a.pre.length > b.pre.length ? 1 : 0;
}

/**
 * Orders two versions by semantic version precedence (semver.org 2.0.0):
 * -1 when a is older than b, 1 when it is newer and 0 when neither is.
 * Build metadata takes no part. undefined when either is no semantic
 * version.
 */
export function compareVersions(a: string, b: string): -1 | 0 | 1 | undefined {
  const left = parseVersion(a);
  const right = parseVersion(b);
  if (!left || !right) return undefined;
  return compareParsed(left, right) as -1 | 0 | 1;
}

/** The oldest version of each client an API serves, as MIN_CLIENT_VERSIONS lists them. */
export interface MinClientVersions {
  /** Each client's minimum, as it was written. */
  readonly minimums: Readonly<Record<string, string>>;
}

/** What a client_too_old problem carries in details. */
export interface ClientTooOld {
  readonly client: string;
  /** The version the client sent. */
  readonly version: string;
  /** The oldest version of the client the API serves. */
  readonly minimum: string;
}

/**
 * Reads MIN_CLIENT_VERSIONS: entries `<client>=<version>` separated by
 * commas, with space allowed around each entry, name and version. An
 * empty or absent setting lists none. An entry without one `=`, a name
 * that is not letters, digits, `.`, `_` and `-`, a minimum that is no
 * semantic version and a client listed twice throw: a server does not
 * start on a minimum it cannot enforce.
 */
export function parseMinClientVersions(setting: string | null | undefined): MinClientVersions {
  const minimums: Record<string, string> = Object.create(null) as Record<string, string>;
  for (const raw of (setting ?? '').split(',')) {
    const entry = raw.trim();
    if (entry === '') continue;
    const parts = entry.split('=');
    if (parts.length !== 2) {
      throw new Error(`${MIN_CLIENT_VERSIONS_SETTING}: ${JSON.stringify(entry)} is not <client>=<version>, as in cli=0.5.0,desktop=2.1.0`);
    }
    const name = parts[0]!.trim();
    const version = parts[1]!.trim();
    if (!CLIENT_NAME.test(name)) {
      throw new Error(
        `${MIN_CLIENT_VERSIONS_SETTING}: ${JSON.stringify(name)} is not a client's name: a letter or a digit, then letters, digits, '.', '_' and '-', 64 characters at most`
      );
    }
    if (!parseVersion(version)) {
      throw new Error(`${MIN_CLIENT_VERSIONS_SETTING}: the minimum of ${name}, ${JSON.stringify(version)}, is not a semantic version such as 0.5.0`);
    }
    if (name in minimums) throw new Error(`${MIN_CLIENT_VERSIONS_SETTING} lists ${name} twice`);
    minimums[name] = version;
  }
  return { minimums };
}

function processEnv(): StackEnv {
  return (globalThis as { process?: { env?: StackEnv } }).process?.env ?? {};
}

/**
 * Reads the minimums from env's MIN_CLIENT_VERSIONS, process.env by
 * default, as a generated EnvConfig does: none when it is unset, and a
 * StackConfigError that says what is wrong with a list no server starts
 * with.
 */
export function loadMinClientVersions(env: StackEnv = processEnv()): MinClientVersions {
  try {
    return parseMinClientVersions(env[MIN_CLIENT_VERSIONS_SETTING]);
  } catch (error) {
    throw new StackConfigError([error instanceof Error ? error.message : String(error)]);
  }
}

/**
 * Decides a request by its X-Client-Version header, `<name>/<version>`:
 * the refusal of a listed client at a version under its minimum, and
 * undefined for every other request.
 */
export function clientTooOld(minimums: MinClientVersions, header: string | null | undefined): ClientTooOld | undefined {
  if (!header) return undefined;
  const trimmed = header.replace(/^[ \t]+|[ \t]+$/g, '');
  const slash = trimmed.indexOf('/');
  if (slash < 0) return undefined;
  const client = trimmed.slice(0, slash);
  const version = trimmed.slice(slash + 1);
  if (!Object.prototype.hasOwnProperty.call(minimums.minimums, client)) return undefined;
  const minimum = minimums.minimums[client]!;
  const order = compareVersions(version, minimum);
  if (order === undefined || order >= 0) return undefined;
  return { client, version, minimum };
}

/** The 426 a client under its minimum is answered with. */
export function clientTooOldProblem(refusal: ClientTooOld): HttpProblem {
  return new HttpProblem(
    426,
    `${refusal.client} ${refusal.version} is older than ${refusal.minimum}, the oldest version of ${refusal.client} this API serves. Upgrade ${refusal.client}.`,
    { code: CLIENT_TOO_OLD, details: { client: refusal.client, version: refusal.version, minimum: refusal.minimum } }
  );
}
