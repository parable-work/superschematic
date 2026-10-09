import {
  googleIdTokenSource,
  signedTokenSource,
  tokenFileSource,
  type Ed25519PrivateJwk,
  type GoogleIdTokenSourceOptions,
  type ServiceTokenSource,
  type SignedTokenSourceOptions,
  type TokenFileSourceOptions,
} from './credentials.js';
import type { PublicJwk, ServiceAuthAlgorithm, ServiceAuthConfig, ServiceAuthIssuer, ServiceCallerConfig } from './serviceauth.js';

/*
The config fields an API's edges derive in a stack (section 3.4 of
docs/stack-model.md), the twin of the Go runtime's stackconfig package: a
database connection for the API's database, a service endpoint for each API
it calls, a bucket connection for each bucket it lists (D54), and for an API
with a service clause its callers field, what its server verifies a service
credential against. The generated config loader (the API package's
loadEnvConfig) and entrypoint read each from the environment variables a
platform sets, one per member of the value the edges' connectors derived:
the field's name, an underscore and the member's path in upper snake case
(SHOP_DB_DATABASE_URL, SHOP_API_SERVICE_CREDENTIAL_SOURCE,
SHOP_MEDIA_BUCKET_NAME, SHOP_API_CALLERS_ISSUERS_0_AUDIENCE). The members
are those of ir.DatabaseConnection, ir.ServiceEndpoint, ir.BucketConnection
and ir.ServiceAuth.

Each reader refuses what Go's refuses, with the same messages: the vectors
in runtime/http/testdata/stackconfig_parity.json hold both runtimes to one
encoding (D51). Like the service auth verifier, nothing here imports from
node:, so it runs on Node.js, Bun and Workers.
*/

/** An environment map: process.env, or a record a test builds. */
export type StackEnv = Readonly<Record<string, string | undefined>>;

/** A Cloud SQL connector configuration, which connects with IAM database authentication. */
export interface CloudSqlConnection {
  /** The instance connection name, project:region:instance. */
  readonly instance: string;
  /** The database's name on the instance. */
  readonly database: string;
  /** The IAM database user. */
  readonly user: string;
}

/** A database connection: a connection string, or a Cloud SQL connector configuration. */
export type Database = { readonly url: string } | { readonly cloudSql: CloudSqlConnection };

/** The credential sources the HTTP runtimes ship (D37). */
export const CREDENTIAL_SOURCES = ['google-id-token', 'token-file', 'signed-token'] as const;

/** One of CREDENTIAL_SOURCES. */
export type ServiceCredentialSource = (typeof CREDENTIAL_SOURCES)[number];

/** The header a callee reads the service credential from, and the one a credential with no headers travels in. */
export const SERVICE_AUTHORIZATION_HEADER = 'Service-Authorization';

/**
 * The source of a service credential and the headers that carry it, with
 * the members its source reads. Without headers it travels in
 * Service-Authorization alone.
 */
export type ServiceCredential =
  | {
      /** A Google ID token for audience, from the metadata server. */
      readonly source: 'google-id-token';
      readonly audience: string;
      readonly headers?: readonly string[];
    }
  | {
      /** A token read from tokenFile. */
      readonly source: 'token-file';
      readonly tokenFile: string;
      readonly headers?: readonly string[];
    }
  | {
      /** A token signed with key, a private JWK's JSON, as issuer for audience. */
      readonly source: 'signed-token';
      readonly audience: string;
      readonly issuer: string;
      readonly key: string;
      readonly headers?: readonly string[];
    };

/** A service endpoint: how the server reaches an API it calls. */
export interface Service {
  /** The callee's base URL. */
  readonly url: string;
  /** The source of the service credential the client sends; absent for none. */
  readonly credential?: ServiceCredential;
}

/**
 * A bucket connection: how the server reaches a bucket an API it serves
 * lists in its buckets (D54). It holds no credential: on gcp the workload's
 * own account reaches the bucket, and an emulator checks none.
 */
export interface BucketConnection {
  /** The bucket's name with its provider. */
  readonly name: string;
  /** The base URL of an emulator that serves the provider's API in its place, such as the local target's fake-gcs-server; absent reaches the provider itself. */
  readonly endpoint?: string;
}

/** What an API's callers field adds to the API's name in upper snake case: SHOP_API_CALLERS is shop-api's. */
export const CALLERS_SUFFIX = '_CALLERS';

/**
 * Thrown by the readers: every refused variable, one message each, which
 * names the variable and never carries a value the platform may have set
 * from a secret store. The message joins them with newlines, as Go's
 * errors.Join does.
 */
export class StackConfigError extends Error {
  readonly problems: readonly string[];

  constructor(problems: readonly string[]) {
    super(problems.join('\n'));
    this.name = 'StackConfigError';
    this.problems = problems;
  }
}

/** A list's length variable is at most this, so a mistaken value cannot make the loader read an unbounded number of variables. */
const MAX_COUNT = 1000;

const WHOLE_NUMBER = /^[+-]?[0-9]+$/u;

function processEnv(): StackEnv {
  return (globalThis as { process?: { env?: StackEnv } }).process?.env ?? {};
}

/** Go's %q for the values these messages quote. */
function quote(value: string): string {
  return JSON.stringify(value);
}

/** A variable's value, with an unset one and an empty one alike empty, as Go's os.Getenv reads them. */
function getenv(env: StackEnv, name: string): string {
  return env[name] ?? '';
}

/**
 * Reads the database field named field: field_URL, or the three
 * field_CLOUD_SQL_ variables. Throws StackConfigError when neither is set,
 * both are, or only some of the three.
 */
export function loadDatabase(field: string, env: StackEnv = processEnv()): Database {
  const url = getenv(env, `${field}_URL`);
  const cloud = [`${field}_CLOUD_SQL_INSTANCE`, `${field}_CLOUD_SQL_DATABASE`, `${field}_CLOUD_SQL_USER`];
  const values = cloud.map(name => getenv(env, name));
  const set = cloud.filter((_, i) => values[i] !== '');
  const unset = cloud.filter((_, i) => values[i] === '');
  if (url !== '' && set.length > 0) {
    throw new StackConfigError([
      `environment variables ${field}_URL and ${set.join(', ')} are both set; a database connection is a connection string or a Cloud SQL connector configuration, not both`,
    ]);
  }
  if (url !== '') return { url };
  if (set.length === 0) {
    throw new StackConfigError([`required environment variable ${field}_URL, or ${cloud.join(', ')}, is not set`]);
  }
  if (unset.length > 0) {
    throw new StackConfigError([
      `environment variables ${set.join(', ')} are set and ${unset.join(', ')} is not; a Cloud SQL connection sets all three`,
    ]);
  }
  const [instance, database, user] = values as [string, string, string];
  return { cloudSql: { instance, database, user } };
}

/** The shape of a bucket's endpoint: an http or https URL with a host. */
const ENDPOINT = /^https?:\/\/[^/?#]+/u;

/**
 * Reads the bucket field named field: field_NAME, and field_ENDPOINT when
 * an emulator serves the bucket (D54). Throws StackConfigError without a
 * name, and for an endpoint that is no http or https URL.
 */
export function loadBucket(field: string, env: StackEnv = processEnv()): BucketConnection {
  const name = getenv(env, `${field}_NAME`);
  const endpoint = getenv(env, `${field}_ENDPOINT`);
  const problems: string[] = [];
  if (name === '') problems.push(`required environment variable ${field}_NAME is not set`);
  if (endpoint !== '' && !ENDPOINT.test(endpoint)) {
    problems.push(`environment variable ${field}_ENDPOINT is ${quote(endpoint)}; want an http or https URL`);
  }
  if (problems.length > 0) throw new StackConfigError(problems);
  return endpoint === '' ? { name } : { name, endpoint: endpoint.replace(/\/+$/u, '') };
}

/** The credential members, in the order the messages name them. */
const CREDENTIAL_MEMBERS = ['AUDIENCE', 'TOKEN_FILE', 'ISSUER', 'KEY'] as const;

type CredentialMember = (typeof CREDENTIAL_MEMBERS)[number];

/** The members each source reads. */
const SOURCE_READS: Readonly<Record<ServiceCredentialSource, readonly CredentialMember[]>> = {
  'google-id-token': ['AUDIENCE'],
  'token-file': ['TOKEN_FILE'],
  'signed-token': ['AUDIENCE', 'ISSUER', 'KEY'],
};

function isSource(value: string): value is ServiceCredentialSource {
  return (CREDENTIAL_SOURCES as readonly string[]).includes(value);
}

/**
 * Reads the service field named field: field_URL, and the
 * field_CREDENTIAL_ variables when field_CREDENTIAL_SOURCE is set. Throws
 * StackConfigError without a URL, for a credential member set without a
 * source, for an unknown source, for a member the source reads that is
 * unset or one it does not read that is set, and for headers that lack
 * Service-Authorization.
 */
export function loadService(field: string, env: StackEnv = processEnv()): Service {
  const url = getenv(env, `${field}_URL`);
  if (url === '') throw new StackConfigError([`required environment variable ${field}_URL is not set`]);
  const prefix = `${field}_CREDENTIAL_`;
  const members = Object.fromEntries(CREDENTIAL_MEMBERS.map(name => [name, getenv(env, prefix + name)])) as Record<CredentialMember, string>;
  const headers = getenv(env, `${prefix}HEADERS`);
  const source = getenv(env, `${prefix}SOURCE`);
  if (source === '') {
    for (const name of CREDENTIAL_MEMBERS) {
      if (members[name] !== '') {
        throw new StackConfigError([`environment variable ${prefix}${name} is set and ${prefix}SOURCE is not`]);
      }
    }
    if (headers !== '') {
      throw new StackConfigError([`environment variable ${prefix}HEADERS is set and ${prefix}SOURCE is not`]);
    }
    return { url };
  }
  if (!isSource(source)) {
    throw new StackConfigError([
      `environment variable ${prefix}SOURCE is ${quote(source)}; want ${CREDENTIAL_SOURCES[0]}, ${CREDENTIAL_SOURCES[1]} or ${CREDENTIAL_SOURCES[2]}`,
    ]);
  }
  const problems: string[] = [];
  const reads = SOURCE_READS[source];
  for (const name of CREDENTIAL_MEMBERS) {
    const needed = reads.includes(name);
    if (needed && members[name] === '') {
      problems.push(`required environment variable ${prefix}${name} is not set: a ${source} credential reads it`);
    } else if (!needed && members[name] !== '') {
      problems.push(`environment variable ${prefix}${name} is set, which a ${source} credential does not read`);
    }
  }
  let headerNames: string[] | undefined;
  if (headers !== '') {
    headerNames = [];
    let hasServiceHeader = false;
    for (const raw of headers.split(',')) {
      const header = raw.trim();
      if (header === '') {
        problems.push(`environment variable ${prefix}HEADERS has an empty header name`);
        continue;
      }
      hasServiceHeader ||= header.toLowerCase() === SERVICE_AUTHORIZATION_HEADER.toLowerCase();
      headerNames.push(header);
    }
    if (!hasServiceHeader) {
      problems.push(`environment variable ${prefix}HEADERS lacks ${SERVICE_AUTHORIZATION_HEADER}, the header the callee reads`);
    }
  }
  if (problems.length > 0) throw new StackConfigError(problems);
  const carried = headerNames ? { headers: headerNames } : {};
  let credential: ServiceCredential;
  switch (source) {
    case 'google-id-token':
      credential = { source, audience: members.AUDIENCE, ...carried };
      break;
    case 'token-file':
      credential = { source, tokenFile: members.TOKEN_FILE, ...carried };
      break;
    case 'signed-token':
      credential = { source, audience: members.AUDIENCE, issuer: members.ISSUER, key: members.KEY, ...carried };
      break;
  }
  return { url, credential };
}

/** Reads a callers field's variables and records which it read. */
class CallerVars {
  readonly read = new Set<string>();
  readonly problems: string[] = [];

  constructor(readonly vars: ReadonlyMap<string, string>) {}

  get(name: string): string | undefined {
    this.read.add(name);
    return this.vars.get(name);
  }

  fail(message: string): void {
    this.problems.push(message);
  }

  /** A variable that must be set and not empty. */
  required(name: string): string {
    const value = this.get(name) ?? '';
    if (value === '') this.fail(`required environment variable ${name} is not set`);
    return value;
  }

  /** A list's length. */
  count(name: string, least: number): number {
    const value = this.required(name);
    if (value === '') return 0;
    const n = WHOLE_NUMBER.test(value) ? Number(value) : Number.NaN;
    if (!Number.isSafeInteger(n) || n < least || n > MAX_COUNT) {
      this.fail(`environment variable ${name} is ${quote(value)}; want a number of entries from ${least} to ${MAX_COUNT}`);
      return 0;
    }
    return n;
  }

  /** A comma-separated list, which must not be empty when set. */
  list(name: string, required: boolean): string[] | undefined {
    const value = this.get(name) ?? '';
    if (value === '') {
      if (required) this.fail(`required environment variable ${name} is not set`);
      return undefined;
    }
    const out: string[] = [];
    for (const raw of value.split(',')) {
      const item = raw.trim();
      if (item === '') {
        this.fail(`environment variable ${name} has an empty entry`);
        continue;
      }
      out.push(item);
    }
    return out;
  }

  /** The issuer whose variables begin with p. */
  issuer(p: string): ServiceAuthIssuer {
    const issuer = this.required(`${p}ISSUER`);
    const issuerAliases = this.list(`${p}ISSUER_ALIASES`, false);
    const audience = this.required(`${p}AUDIENCE`);
    const algorithms = (this.list(`${p}ALGORITHMS`, true) ?? []) as ServiceAuthAlgorithm[];
    const jwksUrl = this.get(`${p}JWKS_URL`);
    let keys: PublicJwk[] | undefined;
    if (this.get(`${p}KEYS`) !== undefined) {
      const n = this.count(`${p}KEYS`, 1);
      for (let j = 0; j < n; j++) {
        const name = `${p}KEYS_${j}_JWK`;
        const text = this.required(name);
        if (text === '') continue;
        const key = parseJwk(text);
        if (typeof key === 'string') {
          this.fail(`environment variable ${name} is not a JWK: ${key}`);
          continue;
        }
        (keys ??= []).push(key);
      }
    }
    const subjectClaim = this.get(`${p}SUBJECT_CLAIM`);
    let maxLifetimeSeconds: number | undefined;
    const lifetime = this.get(`${p}MAX_LIFETIME_SECONDS`);
    if (lifetime !== undefined) {
      const n = WHOLE_NUMBER.test(lifetime) ? Number(lifetime) : Number.NaN;
      if (!Number.isSafeInteger(n) || n < 1) {
        this.fail(`environment variable ${p}MAX_LIFETIME_SECONDS is ${quote(lifetime)}; want a whole number of seconds above 0`);
      } else {
        maxLifetimeSeconds = n;
      }
    }
    const callers = new Map<string, ServiceCallerConfig>();
    const n = this.count(`${p}CALLERS`, 1);
    for (let k = 0; k < n; k++) {
      const q = `${p}CALLERS_${k}_`;
      const subject = this.required(`${q}SUBJECT`);
      const caller: ServiceCallerConfig = { deployable: this.required(`${q}DEPLOYABLE`), serves: this.list(`${q}SERVES`, true) ?? [] };
      if (callers.has(subject) && subject !== '') {
        this.fail(`environment variable ${q}SUBJECT is ${subject}, another caller's of the issuer ${issuer}`);
        continue;
      }
      callers.set(subject, caller);
    }
    // A member the environment leaves empty is absent, as the Go config's
    // zero value is: the verifier reads both alike.
    return {
      issuer,
      ...(issuerAliases ? { issuerAliases } : {}),
      audience,
      algorithms,
      ...(jwksUrl ? { jwksUrl } : {}),
      ...(keys ? { keys } : {}),
      ...(subjectClaim ? { subjectClaim } : {}),
      ...(maxLifetimeSeconds ? { maxLifetimeSeconds } : {}),
      callers: Object.fromEntries(callers),
    };
  }
}

/** A JWK's JSON parsed to an object, or the reason it is not one. */
function parseJwk(text: string): PublicJwk | string {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch (err) {
    return err instanceof Error ? err.message : String(err);
  }
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return 'not a JSON object';
  return value as PublicJwk;
}

/**
 * Reads the callers field named field: what the server of an API with a
 * service clause verifies a caller's service credential against (sections
 * 3.4 and 9.2 of docs/stack-model.md). Its value is an ir.ServiceAuth, one
 * variable per member, a list of objects as a variable holding its length
 * and each object's members under its index:
 *
 *     field_ISSUERS                          the number of issuers
 *     field_ISSUERS_<i>_ISSUER               the iss the issuer writes
 *     field_ISSUERS_<i>_ISSUER_ALIASES       other iss values, comma-separated
 *     field_ISSUERS_<i>_AUDIENCE             the aud a token must hold
 *     field_ISSUERS_<i>_ALGORITHMS           RS256, ES256 or EdDSA, comma-separated
 *     field_ISSUERS_<i>_JWKS_URL             where the issuer's keys are, or
 *     field_ISSUERS_<i>_KEYS                 the number of keys, each
 *     field_ISSUERS_<i>_KEYS_<j>_JWK         a public JWK as JSON
 *     field_ISSUERS_<i>_SUBJECT_CLAIM        the claim that names the caller
 *     field_ISSUERS_<i>_MAX_LIFETIME_SECONDS the longest a token may live
 *     field_ISSUERS_<i>_CALLERS              the number of callers, each
 *     field_ISSUERS_<i>_CALLERS_<k>_SUBJECT     the claim's value
 *     field_ISSUERS_<i>_CALLERS_<k>_DEPLOYABLE  the deployable it is
 *     field_ISSUERS_<i>_CALLERS_<k>_SERVES      the APIs it serves, comma-separated
 *
 * It returns the field as the ServiceAuthConfig serviceAuthenticator takes,
 * each issuer's callers keyed by subject. No issuers means no server calls
 * the API in the environment: the authenticator then refuses every service
 * credential. A variable under the field's name that is no member, or a
 * list whose length its variables do not match, is refused.
 */
export function loadCallers(field: string, env: StackEnv = processEnv()): ServiceAuthConfig {
  const vars = new Map<string, string>();
  for (const [name, value] of Object.entries(env)) {
    if (value !== undefined && name.startsWith(`${field}_`)) vars.set(name, value);
  }
  const c = new CallerVars(vars);
  const issuers: ServiceAuthIssuer[] = [];
  const n = c.count(`${field}_ISSUERS`, 0);
  for (let i = 0; i < n; i++) issuers.push(c.issuer(`${field}_ISSUERS_${i}_`));
  const unknown = [...vars.keys()].filter(name => !c.read.has(name)).sort();
  for (const name of unknown) c.fail(`environment variable ${name} is no member of the callers field ${field}`);
  if (c.problems.length > 0) throw new StackConfigError(c.problems);
  return { issuers };
}

/** What an SDK's `serviceCredential` takes: the token source, and the headers that carry the token. */
export interface ServiceCredentialConfig {
  readonly token: ServiceTokenSource;
  readonly headers: readonly string[];
}

/** Options the credential sources take, for tests. */
export interface ServiceCredentialOptions {
  readonly googleIdToken?: GoogleIdTokenSourceOptions;
  readonly tokenFile?: TokenFileSourceOptions;
  readonly signedToken?: SignedTokenSourceOptions;
}

/**
 * The SDK `serviceCredential` of a loaded credential, over the runtime's
 * sources (section 9.6 of docs/stack-model.md): a Google ID token for its
 * audience, the token in its file, or a token signed with its key as its
 * issuer, for its audience. The headers are the credential's, or
 * Service-Authorization alone. Undefined for no credential. Throws when a
 * signed-token key is not an Ed25519 private JWK's JSON.
 */
export function serviceCredentialFor(credential: ServiceCredential, options?: ServiceCredentialOptions): ServiceCredentialConfig;
export function serviceCredentialFor(credential: ServiceCredential | undefined, options?: ServiceCredentialOptions): ServiceCredentialConfig | undefined;
export function serviceCredentialFor(credential: ServiceCredential | undefined, options: ServiceCredentialOptions = {}): ServiceCredentialConfig | undefined {
  if (!credential) return undefined;
  const headers = credential.headers && credential.headers.length > 0 ? credential.headers : [SERVICE_AUTHORIZATION_HEADER];
  switch (credential.source) {
    case 'google-id-token':
      return { token: googleIdTokenSource(credential.audience, options.googleIdToken), headers };
    case 'token-file':
      return { token: tokenFileSource(credential.tokenFile, options.tokenFile), headers };
    case 'signed-token': {
      let key: unknown;
      try {
        key = JSON.parse(credential.key);
      } catch {
        throw new Error('serviceCredentialFor: the signed-token key is not a JWK');
      }
      const claims = { issuer: credential.issuer, subject: credential.issuer, audience: credential.audience };
      return { token: signedTokenSource(key as Ed25519PrivateJwk, claims, options.signedToken), headers };
    }
  }
  const source: never = credential;
  throw new Error(`service credential source ${quote((source as { source: string }).source)} is not one the runtime ships`);
}
