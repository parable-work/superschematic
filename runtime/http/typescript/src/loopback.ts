import { signedTokenSource, type ServiceTokenSource } from './credentials.js';
import { base64urlEncode, ed25519Pkcs8FromSeed, ed25519Thumbprint, type ServiceAuthConfig, type ServiceAuthIssuer } from './serviceauth.js';
import { SERVICE_AUTHORIZATION_HEADER, type ServiceCredentialConfig } from './stackconfig.js';

/*
A server's credential for its own calls between the APIs it serves (section
9.9 of docs/stack-model.md). Such a call reaches the server on its own
loopback address through the callee's SDK client, and no connector gives it
a credential, since no edge between two deployables carries it. So the
server makes one: when it starts, it generates an Ed25519 key pair that only
its process holds. Each client of an API the server serves signs the generic
connector's token with it (D63, section 9.2): iss, sub and aud the server's
deployable, five minutes long. The service authenticator of each API with a
service clause accepts the key as one more issuer beside its callers
field's (accept), whose one caller is the server, serving every API it
serves, as a caller on another server is the deployable and every API it
serves.

No other process holds the key, so nothing outside the process can sign as
the server, and the key dies with the process: a token an earlier process,
or another instance of the server, signed does not verify. The token goes
only to a loopback URL (credentialFor), so it never leaves the host. Nothing
is configured or stored. Like the verifier, this imports nothing from node:,
so a Workers server builds its key the same way.
*/

/** The longest a loopback token lives: signedTokenSource signs it for 300 seconds, and the loopback issuer refuses a longer one. */
export const LOOPBACK_LIFETIME_SECONDS = 300;

export interface LoopbackOptions {
  /** The key's 32-byte Ed25519 seed, for tests; random by default. */
  readonly seed?: Uint8Array;
  /** Clock in milliseconds of the token source, for tests; Date.now by default. */
  readonly now?: () => number;
}

/** A server's loopback key: the issuer it adds to a callers field, and the token its in-server clients send. */
export interface Loopback {
  /**
   * The loopback issuer: the server's deployable as issuer, audience and
   * the one caller, named in sub, serving every API the server serves; the
   * key's public half as the one key; EdDSA; and 300 seconds at most.
   */
  readonly issuer: ServiceAuthIssuer;
  /**
   * An API's callers field with the loopback issuer added after its own.
   * Throws for a field that already names an issuer, or an alias, the
   * loopback issuer's name: no connector writes one, since a server is no
   * caller of itself through an edge, so such a field was written by hand.
   */
  accept(config: ServiceAuthConfig): ServiceAuthConfig;
  /** Signs the loopback tokens; every in-server client shares it. */
  readonly token: ServiceTokenSource;
  /** The token in Service-Authorization, as an SDK's serviceCredential takes it. */
  readonly credential: ServiceCredentialConfig;
  /**
   * The credential an in-server client sends to url, the URL its edge
   * derives: the loopback credential for a loopback URL (isLoopbackUrl),
   * and undefined for any other, which may reach what is not this process.
   */
  credentialFor(url: string): ServiceCredentialConfig | undefined;
}

/** Generates the loopback key of the server named deployable, which serves the APIs in serves. */
export async function loopbackServiceAuth(deployable: string, serves: readonly string[], options: LoopbackOptions = {}): Promise<Loopback> {
  if (deployable === '') throw new Error("loopbackServiceAuth: a loopback key needs the server's deployable");
  const served = [...serves];
  const seed = options.seed ?? crypto.getRandomValues(new Uint8Array(32));
  const key = await crypto.subtle.importKey('pkcs8', ed25519Pkcs8FromSeed(seed), { name: 'Ed25519' }, true, ['sign']);
  const { x } = await crypto.subtle.exportKey('jwk', key);
  if (typeof x !== 'string') throw new Error('loopbackServiceAuth: the key exports no public key');
  const kid = ed25519Thumbprint(x);
  const token = signedTokenSource(
    { kty: 'OKP', crv: 'Ed25519', d: base64urlEncode(seed), x, kid },
    { issuer: deployable, subject: deployable, audience: deployable },
    options.now ? { now: options.now } : {}
  );
  const issuer = (): ServiceAuthIssuer => ({
    issuer: deployable,
    audience: deployable,
    algorithms: ['EdDSA'],
    keys: [{ kty: 'OKP', kid, crv: 'Ed25519', x }],
    subjectClaim: 'sub',
    maxLifetimeSeconds: LOOPBACK_LIFETIME_SECONDS,
    callers: { [deployable]: { deployable, serves: [...served] } },
  });
  const credential: ServiceCredentialConfig = { token, headers: [SERVICE_AUTHORIZATION_HEADER] };
  return {
    get issuer() {
      return issuer();
    },
    accept(config) {
      config.issuers.forEach((entry, index) => {
        if (entry.issuer === deployable || (entry.issuerAliases ?? []).includes(deployable)) {
          throw new Error(
            `loopbackServiceAuth: issuers[${index}] is named ${JSON.stringify(deployable)}, the server's own loopback issuer; a callers field lists the server's callers, never the server`
          );
        }
      });
      return { ...config, issuers: [...config.issuers, issuer()] };
    },
    token,
    credential,
    credentialFor: url => (isLoopbackUrl(url) ? credential : undefined),
  };
}

/**
 * Whether url is an http or https URL whose host only this host answers,
 * read literally, as the Go and Rust runtimes read it
 * (serviceauth_parity.json's loopback.urls): the scheme http or https in any
 * case, `://`, then a host that is localhost in any case, [::1], or four
 * decimal octets from 0 to 255 whose first is 127, written without leading
 * zeros; then nothing, or a colon and a port of decimal digits, before the
 * path, query or fragment. A URL with user information, a percent-encoded or
 * shortened host, or any other spelling of a loopback address is not one.
 */
export function isLoopbackUrl(url: string): boolean {
  const scheme = /^https?:\/\//iu.exec(url);
  if (!scheme) return false;
  const rest = url.slice(scheme[0].length);
  const cut = rest.search(/[/?#]/u);
  const authority = cut < 0 ? rest : rest.slice(0, cut);
  let host = authority;
  let port = '';
  if (authority.startsWith('[')) {
    const end = authority.indexOf(']');
    if (end < 0) return false;
    host = authority.slice(0, end + 1);
    port = authority.slice(end + 1);
  } else {
    const colon = authority.indexOf(':');
    if (colon >= 0) {
      host = authority.slice(0, colon);
      port = authority.slice(colon);
    }
  }
  if (port !== '' && !/^:[0-9]+$/u.test(port)) return false;
  return host.toLowerCase() === 'localhost' || host === '[::1]' || isLoopbackIpv4(host);
}

/** Whether host is four decimal octets from 0 to 255, without leading zeros, the first 127. */
function isLoopbackIpv4(host: string): boolean {
  const octets = host.split('.');
  return octets.length === 4 && octets[0] === '127' && octets.every(octet => /^(0|[1-9][0-9]{0,2})$/u.test(octet) && Number(octet) <= 255);
}
