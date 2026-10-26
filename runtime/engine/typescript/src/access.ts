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
tools document asks one more kind when it lists a caller's tools: a
listing question, marked `listing`, about an engine tool that names its
schema or its namespace only when called (define_schema and the
namespace tools), whether the caller may do that at all here; a refusal
hides the tool, and its call still asks the question the call asks. A
behavior's namespace operation (D77), which runs on the behavior's tables
across every schema of a namespace, asks a fourth kind: `read` or `write`
with the behavior and the operation, and no schema; the tools document
asks it as a listing question too. The engine has no roles and no default
policy; `allowAll` is explicit, for tests and local use, and
`permissionPolicy` is opt-in: it allows an action on a schema to a caller
holding `<schema>.<action>`, such as a user whose roles grant it (D50), a
behavior's namespace operation that reads to anyone and one that writes to
a caller holding `<behavior>.<operation>`, and refuses the other
questions that name no schema.

Principal has the shape of the HTTP runtime's (@superschematic/http-runtime),
so a principal its Authenticator returns can be passed on as it is. This
entry point imports only that package's framework-free entry point, for
the default permission matcher; the engine's ./http entry point imports
its Hono adapter (http/app.ts). The Rust engine (D58) reads allowAll and
permissionPolicy() as data, "allowAll" and "permission", and asks any
other policy on the JavaScript thread during a synchronous call.

A call a service makes (D37) carries the calling service in `service`,
beside the end user it acts for, whose subject and permissions the
principal keeps. A service that brings no end user stands in for one: its
principal's subject names the service (`service:<deployable>`,
servicePrincipal) and it holds no permissions, since services hold none
(D37). The policy and the behaviors see the service through the
principal, and a behavior's can() is false for a service standing in.
*/

import { hasAnyPermission, type PermissionMatcher } from '@superschematic/http-runtime';

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

/**
 * One question the engine asks the policy: about a schema of a namespace,
 * about a namespace itself, while it lists a caller's tools whether the
 * caller may do what an engine tool does at all, or about a behavior's
 * namespace operation. A union on `action`, with `behavior` telling the
 * last kind from a schema's question of the same action.
 */
export type AccessRequest = SchemaAccessRequest | NamespaceAccessRequest | ListingAccessRequest | BehaviorAccessRequest;

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
  /** None: a schema's question names no behavior of its own. */
  readonly behavior?: undefined;
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
  readonly behavior?: undefined;
}

/**
 * A question the tools document asks when it lists a caller's tools, of an
 * engine tool that names its schema or its namespace only when called:
 * `define` for define_schema, whether the principal may define some
 * schema of the namespace; `manage` with the operation for the namespace
 * tools (create_namespace, archive_namespace, unarchive_namespace,
 * list_namespaces), whether it may do that to some namespace. It names no
 * schema, and `listing` marks it. A policy that cannot answer without a
 * name answers false, which hides the tool from the caller with the
 * reason; a call of the tool still asks the question its call asks, with
 * the name it gives.
 */
export interface ListingAccessRequest {
  readonly principal: Principal;
  readonly action: 'define' | 'manage';
  /** The namespace whose tools are listed: where a define would store its draft, or whose endpoint serves the namespace tools. */
  readonly namespace: string;
  /** For manage, what the tool does to a namespace; absent for define. */
  readonly operation?: NamespaceOperation;
  /** None: the tool names its schema only when called. */
  readonly schema?: undefined;
  readonly behavior?: undefined;
  readonly listing: true;
}

/**
 * A question about a behavior's namespace operation (D77): one its
 * declaration gives `scope: "namespace"`, which runs on the behavior's
 * tables across every schema of the namespace that composes it, with no
 * schema of its own. `read` or `write`, as its declaration says. The
 * engine asks it at the call, and, with `listing`, while the tools
 * document lists the operation's tool; a refusal hides the tool. The rows
 * a read returns are already only those of the schemas the principal may
 * read (sql.readable(), asked read once per schema), so a policy can let
 * every read one through; a writing one writes only the behavior's own
 * tables.
 */
export interface BehaviorAccessRequest {
  readonly principal: Principal;
  readonly action: 'read' | 'write';
  readonly namespace: string;
  /** The behavior that declares the operation. */
  readonly behavior: string;
  readonly operation: string;
  /** None: the operation runs across schemas. */
  readonly schema?: undefined;
  /** True while the tools document lists the operation's tool. */
  readonly listing?: true;
}

/** Answers true to allow. It runs synchronously, before the engine's transaction. */
export type AccessPolicy = (request: AccessRequest) => boolean;

/** allowAll allows everything: for tests and local use. */
export const allowAll: AccessPolicy = () => true;

// The policies permissionPolicy made without a matcher of their own, which
// an engine answers as its "permission" rule, with its permissionMatcher.
const enginePermissionPolicies = new WeakSet<AccessPolicy>();

/**
 * permissionAllows is permissionPolicy's rule: the principal holds
 * `<schema>.<action>`, by matcher. A behavior's namespace operation (D77)
 * that reads is allowed, since its rows are those of the schemas the
 * principal may read, and one that writes to a holder of
 * `<behavior>.<operation>`, a listing question about one the same. Any
 * other question that names no schema, a namespace's own (`manage`) or a
 * listing question, it cannot answer by a schema's permission, so it
 * refuses it.
 */
function permissionAllows(request: AccessRequest, matcher: PermissionMatcher): boolean {
  if (request.behavior !== undefined) {
    return request.action === 'read' || matcher(request.principal.permissions, [`${request.behavior}.${request.operation}`]);
  }
  if (request.schema === undefined) {
    return false;
  }
  return matcher(request.principal.permissions, [`${request.schema}.${request.action}`]);
}

/**
 * permissionPolicy is an opt-in policy (D50): it allows an action to a
 * caller holding `<schema>.<action>` (`Task.read`, `Task.write`,
 * `Task.define`, `Task.publish`), so a role that grants `Task` grants every
 * action on Task. A behavior operation asks the action its declaration
 * says, so `Task.write` allows a transition; the behavior's own permission,
 * such as a Workflow transition's, is checked besides. A behavior's
 * namespace operation (D77) that reads it allows to anyone, and one that
 * writes to a caller holding `<behavior>.<operation>`
 * (`Comments.claimDeliveries`). It matches with the engine's
 * permissionMatcher, or with the one it is given. It refuses any other
 * question that names no schema: a namespace's own (`manage`), which a
 * deployment's own policy answers, and a listing question, so the tools
 * document hides define_schema and the namespace tools, though a define
 * call, which names its schema, asks `<schema>.define`. Called by a policy
 * of the deployment's own rather than by an engine, it matches with the
 * HTTP runtime's hasAnyPermission, or the matcher it is given.
 */
export function permissionPolicy(options: { permissionMatcher?: PermissionMatcher } = {}): AccessPolicy {
  const matcher = options.permissionMatcher;
  if (matcher !== undefined && typeof matcher !== 'function') {
    throw new TypeError('permissionMatcher is a function (held, required) => boolean');
  }
  const policy: AccessPolicy = (request) => permissionAllows(request, matcher ?? hasAnyPermission);
  if (matcher === undefined) {
    enginePermissionPolicies.add(policy);
  }
  return policy;
}

/**
 * isEnginePermissionPolicy is whether permissionPolicy made the policy
 * without a matcher of its own: the Rust engine's "permission" shorthand,
 * which its facade (src/native) passes in its place.
 */
export function isEnginePermissionPolicy(policy: AccessPolicy): boolean {
  return enginePermissionPolicies.has(policy);
}

/** standsIn reports whether a principal is a service standing in for an end user. */
export function standsIn(principal: Principal): boolean {
  return principal.service?.standsIn === true;
}
