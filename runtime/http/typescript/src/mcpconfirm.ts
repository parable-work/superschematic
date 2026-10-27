import { revealed, type Revealable } from './secret.js';
import { base64urlDecode, base64urlEncode } from './serviceauth.js';

/*
The confirmation an MCP endpoint asks for before it runs a tool whose
invocation policy is ask (D93). A confirmation is a token the endpoint
mints and redeems, statelessly but for a single-use claim:

  v1.<mode>.<expires>.<nonce>.<binding>.<mac>

- mode is elicit, for the requestState of a 2026-07-28 client that asks
  the person itself (elicitation/create in an input_required result), or
  token, for the confirmationToken argument of any other client; one
  mode's token does not redeem the other's, so a requestState never runs a
  call the person was not asked about.
- expires is the Unix second it stops being good.
- nonce is 16 random bytes, the key the single-use claim takes.
- binding is the HMAC-SHA-256 of the scope (the API's name), the tool, the
  caller and the call's exact arguments as canonical JSON (canonicalJson):
  a token minted for one call redeems no other.
- mac is the HMAC-SHA-256 of everything before it, so no field can be
  changed and no token made without the key.

Both MACs are keyed with the endpoint's confirmation key, which a stack
binds to every replica as one secret, so a token one replica minted
redeems on another. The claim that makes it single-use is kept by an
McpConfirmationStore: in process by default, so a token is single-use per
replica, or in a store the replicas share when the deployment supplies
one. Every refusal names why: invalid (not a token this key minted, or
malformed), expired, mismatch (minted for another call, caller or mode),
or used.
*/

/** The tools/call argument a confirmation token comes back in, from a client that does not elicit. */
export const MCP_CONFIRMATION_ARGUMENT = 'confirmationToken';

/** The key of the one input request an input_required result asks an eliciting client. */
export const MCP_CONFIRMATION_INPUT = 'confirm';

/** How long a confirmation is good for unless the options say: ten minutes. */
export const DEFAULT_MCP_CONFIRMATION_TTL_SECONDS = 600;

/** The fewest bytes a confirmation key holds, the HMAC-SHA-256 block of entropy it needs. */
export const MIN_MCP_CONFIRMATION_KEY_BYTES = 32;

/** The problem code of a call that waits for the person's confirmation, and of one they declined. */
export const MCP_CONFIRMATION_REQUIRED = 'confirmation_required';
export const MCP_CONFIRMATION_DECLINED = 'confirmation_declined';

/** How a confirmation comes back: as an eliciting client's requestState, or as a token argument. */
export type McpConfirmationMode = 'elicit' | 'token';

/** Why a token does not redeem. */
export type McpConfirmationRefusal = 'invalid' | 'expired' | 'mismatch' | 'used';

/** What a confirmation is bound to: the tool, the caller and the call's exact arguments. */
export interface McpConfirmationBinding {
  readonly tool: string;
  /** Who the endpoint's auth gate established; empty for no one. */
  readonly caller: string;
  /** The arguments without the confirmation token's. */
  readonly args: Readonly<Record<string, unknown>>;
}

/** A minted confirmation. */
export interface McpConfirmationToken {
  readonly token: string;
  /** When it stops being good, as RFC 3339 UTC. */
  readonly expiresAt: string;
}

/**
 * Where a token's nonce is claimed, so each token redeems once. A
 * deployment with several replicas supplies one backed by something they
 * share (Redis, say) for a token to be single-use across them; the
 * interface is the one method the endpoint calls.
 */
export interface McpConfirmationStore {
  /** Takes id until now + ttlMs unless it is taken: true when this call took it. */
  claim(id: string, ttlMs: number, now: number): boolean | Promise<boolean>;
}

/**
 * Claims in process memory. maxClaims bounds it: past it, expired claims
 * are dropped, and while it is full of live ones a claim is refused rather
 * than one forgotten, which would let its token redeem again.
 */
export class MemoryMcpConfirmationStore implements McpConfirmationStore {
  private readonly claims = new Map<string, number>();

  constructor(private readonly maxClaims = 100_000) {}

  claim(id: string, ttlMs: number, now: number): boolean {
    const until = this.claims.get(id);
    if (until !== undefined && until > now) return false;
    if (until === undefined && this.claims.size >= this.maxClaims) {
      for (const [held, end] of this.claims) {
        if (end <= now) this.claims.delete(held);
      }
      if (this.claims.size >= this.maxClaims) return false;
    }
    this.claims.set(id, now + ttlMs);
    return true;
  }
}

/** What McpConfirmations is built with. */
export interface McpConfirmationOptions {
  /** The confirmation key: at least MIN_MCP_CONFIRMATION_KEY_BYTES of UTF-8, a Secret<string> config field's value as it is. */
  readonly key: string | Revealable<string>;
  /** What a token is good for beside its tool, the API's name, so one API's token confirms no other's call under a shared key. */
  readonly scope?: string;
  /** Seconds a confirmation is good for; DEFAULT_MCP_CONFIRMATION_TTL_SECONDS by default. */
  readonly ttlSeconds?: number;
  /** Where nonces are claimed; a MemoryMcpConfirmationStore by default. */
  readonly store?: McpConfirmationStore;
  /** Clock in milliseconds, for tests. */
  readonly now?: () => number;
}

const encoder = new TextEncoder();
const MAC_DOMAIN = 'superschematic mcp confirmation v1\0';
const BINDING_DOMAIN = 'superschematic mcp confirmation binding v1\0';
const MODES: readonly string[] = ['elicit', 'token'];

/** Mints and redeems the confirmations of an MCP endpoint (D93). */
export class McpConfirmations {
  private readonly key: Promise<CryptoKey>;
  private readonly scope: string;
  private readonly ttlSeconds: number;
  private readonly store: McpConfirmationStore;
  private readonly now: () => number;

  /** Throws for a key shorter than MIN_MCP_CONFIRMATION_KEY_BYTES or a TTL that is no positive whole number of seconds. */
  constructor(options: McpConfirmationOptions) {
    const key = encoder.encode(revealed(options.key));
    if (key.byteLength < MIN_MCP_CONFIRMATION_KEY_BYTES) {
      throw new Error(`McpConfirmations: the confirmation key holds ${key.byteLength} bytes; it needs at least ${MIN_MCP_CONFIRMATION_KEY_BYTES}, random`);
    }
    const ttl = options.ttlSeconds ?? DEFAULT_MCP_CONFIRMATION_TTL_SECONDS;
    if (!Number.isInteger(ttl) || ttl < 1) throw new Error(`McpConfirmations: ttlSeconds ${ttl} is no positive whole number`);
    this.key = crypto.subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign', 'verify']);
    this.scope = options.scope ?? '';
    this.ttlSeconds = ttl;
    this.store = options.store ?? new MemoryMcpConfirmationStore();
    this.now = options.now ?? Date.now;
  }

  /** A confirmation of the call binding describes, which mode redeems. */
  async mint(mode: McpConfirmationMode, binding: McpConfirmationBinding): Promise<McpConfirmationToken> {
    const expires = Math.floor(this.now() / 1000) + this.ttlSeconds;
    const nonce = base64urlEncode(crypto.getRandomValues(new Uint8Array(16)));
    const head = `v1.${mode}.${expires}.${nonce}.${await this.bind(binding)}`;
    const mac = base64urlEncode(new Uint8Array(await crypto.subtle.sign('HMAC', await this.key, encoder.encode(MAC_DOMAIN + head))));
    return { token: `${head}.${mac}`, expiresAt: new Date(expires * 1000).toISOString().replace('.000Z', 'Z') };
  }

  /**
   * Redeems token, sent back in mode for the call binding describes:
   * undefined when it confirms the call, which claims it, or why not. Only
   * a token that confirms the call is claimed, so one sent with other
   * arguments stays good for its own.
   */
  async redeem(token: unknown, mode: McpConfirmationMode, binding: McpConfirmationBinding): Promise<McpConfirmationRefusal | undefined> {
    if (typeof token !== 'string') return 'invalid';
    const parts = token.split('.');
    if (parts.length !== 6 || parts[0] !== 'v1' || !MODES.includes(parts[1]) || !/^[0-9]{1,12}$/u.test(parts[2])) return 'invalid';
    const [, minted, expiresText, nonce, bound, mac] = parts;
    const signature = base64urlDecode(mac);
    if (signature?.byteLength !== 32 || base64urlDecode(nonce)?.byteLength !== 16) return 'invalid';
    const head = parts.slice(0, 5).join('.');
    if (!(await crypto.subtle.verify('HMAC', await this.key, signature, encoder.encode(MAC_DOMAIN + head)))) return 'invalid';
    if (minted !== mode) return 'mismatch';
    const now = this.now();
    const expiresMs = Number(expiresText) * 1000;
    if (expiresMs <= now) return 'expired';
    if (bound !== (await this.bind(binding))) return 'mismatch';
    if (!(await this.store.claim(nonce, expiresMs - now, now))) return 'used';
    return undefined;
  }

  // bind is the binding's MAC: the scope, the tool, the caller and the
  // arguments, each NUL-terminated but the last.
  private async bind(binding: McpConfirmationBinding): Promise<string> {
    const text = `${BINDING_DOMAIN}${this.scope}\0${binding.tool}\0${binding.caller}\0${canonicalJson(binding.args)}`;
    return base64urlEncode(new Uint8Array(await crypto.subtle.sign('HMAC', await this.key, encoder.encode(text))));
  }
}

/**
 * value as JSON with every object's keys sorted and its undefined members
 * left out: one text for one value, whatever order a client wrote it in.
 */
export function canonicalJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(item => (item === undefined ? 'null' : canonicalJson(item))).join(',')}]`;
  if (value !== null && typeof value === 'object') {
    const record = value as Record<string, unknown>;
    const members = Object.keys(record)
      .filter(key => record[key] !== undefined)
      .sort()
      .map(key => `${JSON.stringify(key)}:${canonicalJson(record[key])}`);
    return `{${members.join(',')}}`;
  }
  return JSON.stringify(value) ?? 'null';
}
