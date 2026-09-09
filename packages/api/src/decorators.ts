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

const noopClassDecorator: ClassDecorator = () => {};
const noopMethodDecorator: MethodDecorator = () => {};
const noopPropertyDecorator: PropertyDecorator = () => {};
const noopClassOrMethodDecorator: ClassDecorator & MethodDecorator = () => {};

export function rest(_method: HttpMethod, _path?: string): MethodDecorator {
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
