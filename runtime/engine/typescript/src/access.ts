/*
Who may do what is the deployment's access policy (D16). Every engine
entry point takes the principal it acts for and asks the policy before it
reads or writes: `read` for a schema or its instances and events, `write`
to create, update or delete an instance, `define` for a draft, `publish`
for a new version. A behavior operation asks `write` when its declaration
says it writes and `read` otherwise, and names the operation. The engine
has no roles and no default policy; `allowAll` is explicit, for tests and
local use.

Principal has the shape of the HTTP runtime's (@superschematic/http-runtime),
so a principal its Authenticator returns can be passed on as it is. This
entry point does not import that package, which brings Hono as a peer
dependency; only the engine's ./http entry point does (http/app.ts).
*/

import { EngineError } from './errors.js';

/** The caller an engine call acts for. */
export interface Principal {
  /** Stable subject identifier, recorded as the actor of what the call writes. */
  readonly subject: string;
  readonly permissions: readonly string[];
  /** Verified claims for the policy's own checks. */
  readonly claims?: Readonly<Record<string, unknown>>;
}

export type Action = 'read' | 'write' | 'define' | 'publish';

/** One question the engine asks the policy. */
export interface AccessRequest {
  readonly principal: Principal;
  readonly action: Action;
  /** The namespace the call names. */
  readonly namespace: string;
  /** The schema name the call is about. */
  readonly schema: string;
  /** For a behavior operation, its name; absent for every other call. */
  readonly operation?: string;
}

/** Answers true to allow. It runs synchronously, before the engine's transaction. */
export type AccessPolicy = (request: AccessRequest) => boolean;

/** allowAll allows everything: for tests and local use. */
export const allowAll: AccessPolicy = () => true;

/** checkPrincipal refuses a call without a principal that names its subject. */
export function checkPrincipal(principal: Principal): void {
  if (
    typeof principal !== 'object' ||
    principal === null ||
    typeof principal.subject !== 'string' ||
    principal.subject === ''
  ) {
    throw new EngineError('invalid_argument', 'an engine call needs a principal with a subject');
  }
}

/** Access asks the deployment's policy and throws forbidden on a refusal. */
export class Access {
  constructor(private readonly policy: AccessPolicy) {
    if (typeof policy !== 'function') {
      throw new TypeError('an engine needs an access policy (policy); pass allowAll for tests and local use');
    }
  }

  /** allows asks the policy; only a literal true allows. */
  allows(principal: Principal, action: Action, namespace: string, schema: string, operation?: string): boolean {
    const answer: unknown = this.policy(
      operation === undefined ? { principal, action, namespace, schema } : { principal, action, namespace, schema, operation }
    );
    if (typeof answer === 'object' && answer !== null && typeof (answer as { then?: unknown }).then === 'function') {
      throw new TypeError('an access policy is synchronous: it returned a promise');
    }
    return answer === true;
  }

  /** require throws forbidden unless the policy allows the action. */
  require(principal: Principal, action: Action, namespace: string, schema: string, operation?: string): void {
    if (!this.allows(principal, action, namespace, schema, operation)) {
      const what = operation === undefined ? `${action} ${schema}` : `call ${operation} (${action}) on ${schema}`;
      throw new EngineError('forbidden', `${principal.subject} may not ${what} in namespace ${namespace}`);
    }
  }
}
