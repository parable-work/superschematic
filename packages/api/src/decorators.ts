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

export type DocsLifecycle = "draft" | "experimental" | "active" | "deprecated" | "retired";

export type DocsVisibility = "public" | "internal" | "preview";

export type DocsMappingStatus = "mapped" | "uncertain";

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

export const virtual: PropertyDecorator = noopPropertyDecorator;
export const uiHidden: PropertyDecorator = noopPropertyDecorator;
export const manualRouteRegistration: MethodDecorator = noopMethodDecorator;
