import type { HttpMethod } from "./enums";

export type RateLimitConfig = {
  readonly requestsPerMinute: number;
};

export type BodyLimitConfig = {
  readonly megabytes: number;
};

export type TimeoutConfig = {
  readonly seconds: number;
};

export type HmacVerifiedConfig = {
  readonly provider: string;
};

/**
 * An API service's handle: the sentinel an API service exports from its
 * src/service.generated.ts, built by service() in
 * @superschematic/schema-config. Every ServiceHandle<"API"> is one, and a
 * handle of another kind is not. The shape is restated here because the
 * authoring packages do not depend on one another.
 */
export type ServiceHandleRef = {
  readonly __brand: "ServiceHandle";
  readonly name: string;
  readonly kind: "API";
};

/** The services an operation's service clause admits. */
export type ServiceCallersConfig = {
  /**
   * API service handles: the servers that serve those APIs may call. Each
   * must be an API service. Without from, every server with a calls edge
   * to this API in the stack may; from narrows the edges, never widens
   * them.
   */
  readonly from?: readonly ServiceHandleRef[];
};

export type DocsLifecycle = "draft" | "experimental" | "active" | "deprecated" | "retired";

export type DocsVisibility = "public" | "internal" | "preview";

export type DocsMappingStatus = "mapped" | "uncertain";

/** The replay guarantee an operation gives a caller that repeats a call. */
export type DocsReplayMode = "read_only" | "idempotent" | "compare_and_swap";

/** The reader-facing documentation of one operation. */
export type DocsConfig = {
  /** Short name: the OpenAPI summary. */
  readonly title: string;
  /** The OpenAPI description; it replaces the operation's comment there. */
  readonly description: string;
  /** Stable dotted identifier, at least two lowercase segments: "orders.returns.create". */
  readonly capability: string;
  /** deprecated and retired mark the OpenAPI operation deprecated. */
  readonly lifecycle: DocsLifecycle;
  /** How the operation appears in documentation; it grants no access. */
  readonly visibility: DocsVisibility;
  /** The primary reader. Open to any value unless an extension restricts it. */
  readonly audience?: string;
  /** Defaults to "mapped". */
  readonly mappingStatus?: DocsMappingStatus;
  /** What replaces a deprecated or retired operation. */
  readonly replacement?: string;
  /** The date the operation stops being served, YYYY-MM-DD. */
  readonly sunset?: string;
  /** The replay guarantee; generators never infer one. */
  readonly replayMode?: DocsReplayMode;
  /** RFC 6901 pointers into the generated tool arguments to the idempotency keys. */
  readonly idempotencyKeyPointers?: readonly string[];
  /** RFC 6901 pointers into the generated tool arguments to the expected revision. */
  readonly expectedRevisionPointers?: readonly string[];
  /** When a caller, a person or a model, should choose this operation. */
  readonly useWhen?: string;
  /** When a caller should choose another operation instead. */
  readonly doNotUseWhen?: string;
  /** The outcome a caller should expect after a successful call. */
  readonly success?: string;
  /** The operation's expected errors and the usual correction for each. */
  readonly errors?: readonly DocsError[];
};

/** One expected error of an operation. Codes are unique, ignoring case. */
export type DocsError = {
  readonly code: string;
  readonly description: string;
  readonly commonCorrection: string;
};

/**
 * The core's invocation policy values: "auto" (the default) lets an MCP
 * client run the tool when a model calls it; "ask" has the client ask the
 * person first.
 */
export type MCPInvocationPolicy = "auto" | "ask";

/**
 * The options a visible tool's @mcp takes besides its handle and _meta. The
 * core declares its invocation policy key here.
 *
 * An extension that registers its own invocation policy
 * (Registry.RegisterToolInvocationPolicy) adds its key from its authoring
 * package by module augmentation:
 *
 *     declare module "@superschematic/api" {
 *       interface MCPToolOptions {
 *         readonly review?: "never" | "always";
 *       }
 *     }
 *
 * The registry decides which key a build accepts. With an extension's policy
 * registered the core key still type-checks and fails the load.
 */
export interface MCPToolOptions {
  /** Whether an MCP client runs the tool when a model calls it ("auto", the default) or asks the person first ("ask"). */
  readonly invocationPolicy?: MCPInvocationPolicy;
}

/**
 * An operation's MCP classification. A visible tool takes its title and
 * description from @docs and its icon from @icon; a hidden operation says
 * why it is not a tool.
 */
export type MCPConfig =
  | (MCPToolOptions & {
      /** The tool's wire identifier: lowercase snake_case, at most 48 characters, unique in the API. */
      readonly handle: string;
      readonly hidden?: false;
      /** Copied into the tool's MCP _meta object as written. */
      readonly _meta?: Readonly<Record<string, unknown>>;
    })
  | {
      readonly hidden: true;
      /** Why the operation is not a tool. */
      readonly reason: string;
    };

const noopClassDecorator: ClassDecorator = () => {};
const noopMethodDecorator: MethodDecorator = () => {};
const noopPropertyDecorator: PropertyDecorator = () => {};
const noopClassOrMethodDecorator: ClassDecorator & MethodDecorator = () => {};

export function rest(_method: HttpMethod, _path?: string): MethodDecorator {
  return noopMethodDecorator;
}

export function docs(_config: DocsConfig): MethodDecorator {
  return noopMethodDecorator;
}

export function mcp(_config: MCPConfig): MethodDecorator {
  return noopMethodDecorator;
}

/** The glyph of the operation's MCP tool. Any name unless an extension restricts it. */
export function icon(_name: string): MethodDecorator {
  return noopMethodDecorator;
}

export function requirePermission(_perms: readonly string[]): MethodDecorator {
  return noopMethodDecorator;
}

export const requireOwnership: MethodDecorator = noopMethodDecorator;
export const auth: MethodDecorator = noopMethodDecorator;
export const encrypted: MethodDecorator = noopMethodDecorator;
export const publicRoute: MethodDecorator = noopMethodDecorator;
export const webhook: MethodDecorator = noopMethodDecorator;

export function hmacVerified(_cfg: HmacVerifiedConfig): MethodDecorator {
  return noopMethodDecorator;
}

/**
 * Only a listed service may call. With a user clause (@auth, an
 * Authenticated set, @requirePermission, @requireOwnership), it must forward
 * an end user who meets it; without one, no end user is looked at. On a
 * class, every operation without its own clause that is not @publicRoute.
 */
export function requireService(_cfg?: ServiceCallersConfig): ClassDecorator & MethodDecorator {
  return noopClassOrMethodDecorator;
}

/**
 * Beside the operation's user clause, which it needs: an end user who meets
 * the clause, or a listed service with no end user, which then stands in
 * for the user. On a class, every operation without its own clause that is
 * not @publicRoute.
 */
export function allowService(_cfg?: ServiceCallersConfig): ClassDecorator & MethodDecorator {
  return noopClassOrMethodDecorator;
}

export function rateLimit(_cfg: RateLimitConfig): ClassDecorator & MethodDecorator {
  return noopClassOrMethodDecorator;
}

export function bodyLimit(_cfg: BodyLimitConfig): ClassDecorator & MethodDecorator {
  return noopClassOrMethodDecorator;
}

export function timeout(_cfg: TimeoutConfig): ClassDecorator & MethodDecorator {
  return noopClassOrMethodDecorator;
}

export function source(_target: unknown): ClassDecorator {
  return noopClassDecorator;
}

/**
 * The routes @userSessions adds. `path` prefixes them, "auth" by default.
 * `login`, true by default, adds login, logout and changePassword; false
 * leaves me and capabilities, for an API whose users sign in through
 * another API of the same authDb. `register`, false by default, adds
 * register, which anyone may call, and needs the login.
 */
export type UserSessionsConfig =
  | {
      readonly path?: string;
      readonly login?: true;
      readonly register?: boolean;
    }
  | {
      readonly path?: string;
      readonly login: false;
      readonly register?: false;
    };

/** The routes @userAdministration adds. `path` prefixes them, "auth/admin" by default. */
export type UserAdministrationConfig = {
  readonly path?: string;
};

/**
 * The user model's session routes, on a class with no methods:
 * superschematic adds login, logout, me, capabilities and changePassword
 * under the path, and register with `register: true`. The identity runtime
 * serves them, so the implementation has no method for them. The API's
 * authDb must name a DB schema with a User table, and an API takes one
 * such class. The class takes no Authenticated or Encrypted base and no
 * service clause: each route's rule is the model's.
 */
export function userSessions(_cfg?: UserSessionsConfig): ClassDecorator {
  return noopClassDecorator;
}

/**
 * The user model's administration routes, on a class with no methods:
 * superschematic adds the routes that create, list, disable and enable
 * users and set a password; list, create, update and delete roles; and
 * grant and revoke them. Each needs a permission under the naming key
 * identity_permission_prefix: identity.users.read or .users.write,
 * identity.roles.read or .roles.write. The identity runtime serves them,
 * so the implementation has no method for them. The API's authDb must name
 * a DB schema with a User and a UserRole table, and an API takes one such
 * class, which takes no Authenticated or Encrypted base and no service
 * clause.
 */
export function userAdministration(_cfg?: UserAdministrationConfig): ClassDecorator {
  return noopClassDecorator;
}

/** The options of a job: when it runs, and how long and how often it tries. */
export type JobOptions = {
  /**
   * A five-field cron: minute, hour, day of the month, month and day of the
   * week (`"*\/15 * * * *"`). Without one the job runs only on demand. An
   * environment's settings change it or turn it off.
   */
  readonly schedule?: string;
  /** The IANA time zone the schedule is read in (`"Europe/Paris"`); UTC unless set. */
  readonly timeZone?: string;
  /** Bounds one run, a duration of whole seconds (`"90s"`, `"10m"`, `"1h30m"`); ten minutes unless set. */
  readonly timeout?: string;
  /** How many times a failed run is run again; none unless set. */
  readonly retries?: number;
};

/**
 * Declares a job of the API service: a run to completion that does the
 * API's background work with the API's Deps (docs/stack-model.md, section
 * 8.7). The class's name is the job's, and the class holds nothing:
 *
 * ```ts
 * @job({ schedule: "*\/15 * * * *", timeout: "10m" })
 * export abstract class ExpireCarts {}
 * ```
 */
export function job(_options?: JobOptions): ClassDecorator {
  return noopClassDecorator;
}

/** The options of a worker: the queue it handles, and how. */
export type WorkerOptions = {
  /** The `@queue` class whose messages the worker handles, imported from the API's database. */
  readonly queue: abstract new (...args: never[]) => unknown;
  /** How many messages an instance handles at a time; one unless set. An environment's settings change it. */
  readonly concurrency?: number;
  /** How long a stopping worker lets its running handlers finish before it gives their messages back, a duration of whole seconds; eight seconds unless set. */
  readonly grace?: string;
};

/**
 * Declares a worker of the API service: a deployable that claims the
 * messages of a queue of the API's database and handles each with the API's
 * Deps (docs/stack-model.md, section 8.8). The class's name is the
 * worker's, and the class holds nothing:
 *
 * ```ts
 * @worker({ queue: OrderPlaced, concurrency: 4 })
 * export abstract class FulfilOrders {}
 * ```
 */
export function worker(_options: WorkerOptions): ClassDecorator {
  return noopClassDecorator;
}

export const virtual: PropertyDecorator = noopPropertyDecorator;
export const uiHidden: PropertyDecorator = noopPropertyDecorator;
export const manualRouteRegistration: MethodDecorator = noopMethodDecorator;
