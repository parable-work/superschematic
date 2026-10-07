/*
Who may do what is the deployment's access policy (D16). Every engine
entry point takes the principal it acts for and asks the policy before it
reads or writes: `read` for a schema or its instances and events, `write`
to create, update or delete an instance, `define` for a draft, `publish`
for a new version. A behavior operation asks `write` when its declaration
says it writes and `read` otherwise, and names the operation. Those
questions name a schema. One names none: `manage`, asked about a
namespace itself, with what is done to it as its operation (`create`,
`archive`, `unarchive`, or `list` to see it among the namespaces). The
engine has no roles and no default policy; `allowAll` is explicit, for
tests and local use.

Principal has the shape of the HTTP runtime's (@superschematic/http-runtime),
so a principal its Authenticator returns can be passed on as it is. This
entry point does not import that package, which brings Hono as a peer
dependency; only the engine's ./http entry point does (http/app.ts).

A call a service makes (D37) carries the calling service in `service`,
beside the end user it acts for, whose subject and permissions the
principal keeps. A service that brings no end user stands in for one: its
principal's subject names the service (`service:<deployable>`,
servicePrincipal) and it holds no permissions, since services hold none
(D37). The policy and the behaviors see the service through the
principal, and a behavior's can() is false for a service standing in.
*/

import { EngineError } from './errors.js';

/** The caller an engine call acts for. */
export interface Principal {
  /** Stable subject identifier, recorded as the actor of what the call writes. */
  readonly subject: string;
  readonly permissions: readonly string[];
  /** Verified claims for the policy's own checks. */
  readonly claims?: Readonly<Record<string, unknown>>;
  /**
   * The service that made the call (D37): beside the end user it acts
   * for, or standing in for one. Absent for a call no service made.
   */
  readonly service?: PrincipalService;
}

/** The calling service of a principal: the HTTP runtime's ServiceCaller, and whether it stands in for an end user. */
export interface PrincipalService {
  /** The calling deployable's name. */
  readonly deployable: string;
  /** The API services it serves. */
  readonly serves: readonly string[];
  /** Its credential's subject at its issuer. */
  readonly subject: string;
  /**
   * True when no end user came with the call: the principal is the
   * service's own (servicePrincipal), with no permissions. False when the
   * principal is the end user the service acts for.
   */
  readonly standsIn: boolean;
}

/** The prefix of a service's subject when it stands in for an end user. */
export const SERVICE_SUBJECT_PREFIX = 'service:';

/**
 * servicePrincipal is the principal of a service that brings no end user
 * and so stands in for one (D37): its subject is `service:<deployable>`,
 * apart from any end user's, and it holds no permissions.
 */
export function servicePrincipal(caller: { readonly deployable: string; readonly serves: readonly string[]; readonly subject: string }): Principal {
  return {
    subject: `${SERVICE_SUBJECT_PREFIX}${caller.deployable}`,
    permissions: [],
    service: { deployable: caller.deployable, serves: [...caller.serves], subject: caller.subject, standsIn: true },
  };
}

/** What a question about one schema asks. */
export type SchemaAction = 'read' | 'write' | 'define' | 'publish';

/** Every action the policy answers: a schema's, or `manage`, a namespace's own. */
export type Action = SchemaAction | 'manage';

/** What `manage` asks to do with a namespace: make it, archive it, unarchive it, or see it among the namespaces. */
export type NamespaceOperation = 'create' | 'archive' | 'unarchive' | 'list';

/** One question the engine asks the policy: about a schema of a namespace, or about a namespace itself. */
export type AccessRequest = SchemaAccessRequest | NamespaceAccessRequest;

/** A question about one schema of a namespace: its instances, events, drafts and versions. */
export interface SchemaAccessRequest {
  readonly principal: Principal;
  readonly action: SchemaAction;
  /** The namespace the call names. */
  readonly namespace: string;
  /** The schema name the call is about. */
  readonly schema: string;
  /** For a behavior operation, its name; absent for every other call. */
  readonly operation?: string;
}

/**
 * A question about a namespace itself, which names no schema: whether the
 * principal may create it, archive it, unarchive it, or see it when it
 * lists the namespaces.
 */
export interface NamespaceAccessRequest {
  readonly principal: Principal;
  readonly action: 'manage';
  /** The namespace: the one a create would make, or the one archived, unarchived or listed. */
  readonly namespace: string;
  readonly operation: NamespaceOperation;
  /** None: a namespace's own question names no schema. */
  readonly schema?: undefined;
}

/** Answers true to allow. It runs synchronously, before the engine's transaction. */
export type AccessPolicy = (request: AccessRequest) => boolean;

/** allowAll allows everything: for tests and local use. */
export const allowAll: AccessPolicy = () => true;

/**
 * checkPrincipal refuses a call without a principal that names its
 * subject, and one whose service is malformed or, standing in for an end
 * user, holds permissions.
 */
export function checkPrincipal(principal: Principal): void {
  if (
    typeof principal !== 'object' ||
    principal === null ||
    typeof principal.subject !== 'string' ||
    principal.subject === ''
  ) {
    throw new EngineError('invalid_argument', 'an engine call needs a principal with a subject');
  }
  const service: unknown = principal.service;
  if (service === undefined) {
    return;
  }
  const { deployable, serves, subject, standsIn } = (typeof service === 'object' && service !== null ? service : {}) as Partial<PrincipalService>;
  if (
    typeof deployable !== 'string' ||
    deployable === '' ||
    !Array.isArray(serves) ||
    !serves.every((api) => typeof api === 'string') ||
    typeof subject !== 'string' ||
    typeof standsIn !== 'boolean'
  ) {
    throw new EngineError('invalid_argument', "a principal's service is { deployable, serves, subject, standsIn }");
  }
  if (standsIn && (!Array.isArray(principal.permissions) || principal.permissions.length > 0)) {
    throw new EngineError('invalid_argument', `service ${deployable} stands in for an end user and holds no permissions (D37)`);
  }
}

/** standsIn reports whether a principal is a service standing in for an end user. */
export function standsIn(principal: Principal): boolean {
  return principal.service?.standsIn === true;
}

/** Access asks the deployment's policy and throws forbidden on a refusal. */
export class Access {
  constructor(private readonly policy: AccessPolicy) {
    if (typeof policy !== 'function') {
      throw new TypeError('an engine needs an access policy (policy); pass allowAll for tests and local use');
    }
  }

  /** allows asks the policy about a schema; only a literal true allows. */
  allows(principal: Principal, action: SchemaAction, namespace: string, schema: string, operation?: string): boolean {
    return this.ask(operation === undefined ? { principal, action, namespace, schema } : { principal, action, namespace, schema, operation });
  }

  /** require throws forbidden unless the policy allows the action on the schema. */
  require(principal: Principal, action: SchemaAction, namespace: string, schema: string, operation?: string): void {
    if (!this.allows(principal, action, namespace, schema, operation)) {
      const what = operation === undefined ? `${action} ${schema}` : `call ${operation} (${action}) on ${schema}`;
      throw new EngineError('forbidden', `${principal.subject} may not ${what} in namespace ${namespace}`);
    }
  }

  /** allowsManage asks the policy about a namespace itself (`manage`); only a literal true allows. */
  allowsManage(principal: Principal, namespace: string, operation: NamespaceOperation): boolean {
    return this.ask({ principal, action: 'manage', namespace, operation });
  }

  /** requireManage throws forbidden unless the policy allows the operation on the namespace. */
  requireManage(principal: Principal, namespace: string, operation: NamespaceOperation): void {
    if (!this.allowsManage(principal, namespace, operation)) {
      throw new EngineError('forbidden', `${principal.subject} may not ${operation} namespace ${namespace}`);
    }
  }

  private ask(request: AccessRequest): boolean {
    const answer: unknown = this.policy(request);
    if (typeof answer === 'object' && answer !== null && typeof (answer as { then?: unknown }).then === 'function') {
      throw new TypeError('an access policy is synchronous: it returned a promise');
    }
    return answer === true;
  }
}
