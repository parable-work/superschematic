// The Stack kind's decorators (docs/stack-model.md, section 4): a stack
// service declares what runs where over the services it references. Each
// decorator does nothing at run time; superschematic reads the arguments
// statically, and the types here check in the editor what the loader checks
// again (section 4.3).
import type { Wrapped } from "@superschematic/schema";
import type { ServiceHandle } from "@superschematic/schema-config";

/**
 * The targets an environment may name: the core's `local`, and each target
 * whose authoring package augments it with the target's environment values
 * and a settings type per deployable kind, which `@environment` checks
 * against.
 *
 * ```ts
 * declare module "@superschematic/stack" {
 *   interface Targets {
 *     gcp: { values: GcpValues; server: CloudRunSettings; database: CloudSqlSettings };
 *   }
 * }
 * ```
 */
export interface Targets {
  /** The core's target: every server a process, every database on one Postgres container. */
  local: LocalTarget;
}

/**
 * The core's `local` target, which `superschematic stack dev` runs
 * (docs/stack-model.md, section 8.3). Its values set the image and the host
 * port of the environment's Postgres container, a server's settings its
 * port, and a database takes no settings. A port left out is derived from
 * the stack, the environment and the server.
 */
export interface LocalTarget {
  values: { postgresImage?: string; postgresPort?: number };
  server: { port?: number };
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  database: {};
}

/** A target's name: a key of `Targets`. */
export type TargetName = Extract<keyof Targets, string>;

/** A deployable kind: what a target gives a settings type for. */
export type DeployableKind = "server" | "database";

/**
 * A declared deployable, an `@server` or `@database` class, named as a value
 * (`of: Backend`).
 */
export type DeployableClass = abstract new (...args: never[]) => unknown;

/**
 * An env value a parameter of the environment gives at deploy time
 * (`{ parameter: "pr" }`). It is never written into a generated file.
 */
export type Parameter = { readonly parameter: string };

/** A literal env value. */
export type EnvLiteral = string | number | boolean;

/** The type a field's wrappers (`Default`, `Secret`, ...) wrap. */
export type Unwrap<F> = F extends Wrapped<infer T> ? Unwrap<T> : F;

/** True for a `Secret<T>` field's type. */
type IsSecret<F> = F extends { readonly __secret: true } ? true : false;

/** The fields of config type C an environment sets: all but its secrets. */
type SettableField<C> = {
  [K in keyof C]-?: IsSecret<NonNullable<C[K]>> extends true ? never : K;
}[keyof C];

/** What one field takes: its unwrapped type, a string enum's values included. */
type EnvValueOf<F> = (F extends string ? F | `${F}` : F) | Parameter;

/**
 * The `env` of a server whose config, its `@envVars` class, is C: each
 * field but a `Secret<T>` one, with `Default<T, V>` unwrapped to T. A handle
 * with no config type (C unknown) takes any field.
 */
export type EnvOf<C> = unknown extends C
  ? { readonly [field: string]: EnvLiteral | Parameter }
  : { readonly [K in SettableField<C>]?: EnvValueOf<Unwrap<NonNullable<C[K]>>> };

/**
 * The settings a target gives a deployable kind. With no target named, as
 * in an environment that inherits its target, any key goes and the loader
 * checks it against the platform the deployable lands on.
 */
export type SettingsOf<T, K extends DeployableKind> = [T] extends [TargetName]
  ? Targets[T] extends { readonly [P in K]: infer S }
    ? S
    : {}
  : { readonly [setting: string]: unknown };

/** The values a target takes, under its name in `@environment`. */
export type ValuesOf<T> = [T] extends [TargetName]
  ? Targets[T] extends { readonly values: infer V }
    ? V
    : {}
  : never;

/** What a settings element is, by its `of`, before excess keys are refused. */
type ElementOf<T, Of> = { readonly of: Of; readonly platform?: string } & (Of extends ServiceHandle<"API", infer C>
  ? { readonly env?: EnvOf<C> } & SettingsOf<T, "server">
  : Of extends ServiceHandle<"DB">
    ? SettingsOf<T, "database">
    : Of extends DeployableClass
      ? { readonly env?: EnvOf<unknown> } & (SettingsOf<T, "server"> | SettingsOf<T, "database">)
      : never);

/** Every key of W, or of any member when W is a union. */
type KeysOf<W> = W extends unknown ? keyof W : never;

/** W with every key of E that W lacks refused. */
type Exact<W, E> = W & { readonly [K in Exclude<keyof E, KeysOf<W>>]: never };

/** The env of a settings element whose `of` is Of. */
type EnvFor<Of> = Of extends ServiceHandle<"API", infer C> ? EnvOf<C> : EnvOf<unknown>;

/**
 * One settings element checked: its `of` picks the settings type, its env
 * is the config's fields, and a key neither takes is refused. An element
 * that names a platform, which may be another target's, takes any settings
 * key; the loader checks it against that platform.
 */
export type SettingsElement<T, E> = E extends { readonly of: infer Of }
  ? Exact<ElementOf<E extends { readonly platform: string } ? undefined : T, Of>, E> &
      (E extends { readonly env: infer V } ? { readonly env: Exact<EnvFor<Of>, V> } : {})
  : { readonly of: ServiceHandle<"API" | "DB"> | DeployableClass };

/** The argument of `@environment`. */
export type EnvironmentOptions<T extends TargetName | undefined, S extends readonly unknown[]> = {
  /** The target that places every deployable no setting places elsewhere. */
  readonly target?: T;
  /** Where exposed servers are reached. */
  readonly domain?: string;
  /** The DNS platform that holds the domain's records, with its values: `{ cloudflare: { zone } }`. */
  readonly dns?: { readonly [platform: string]: { readonly [value: string]: unknown } };
  /** Settings per deployable; `of` is a service handle or a declared deployable's class. */
  readonly settings?: S & { readonly [I in keyof S]: SettingsElement<T, S[I]> };
  /** Makes the environment a family, one member per value. */
  readonly parameters?: readonly string[];
} & { readonly [K in NoInfer<T> & string]?: ValuesOf<K> };

const noop: ClassDecorator = () => {};

/** Declares the stack: its entry points and what is reachable from outside. */
export function stack(_options: {
  readonly deploy?: readonly ServiceHandle<"API" | "DB">[];
  readonly expose?: readonly (ServiceHandle<"API"> | DeployableClass)[];
}): ClassDecorator {
  return noop;
}

/** Declares a server that serves several APIs in one process. */
export function server(_options: { readonly serves: readonly ServiceHandle<"API">[] }): ClassDecorator {
  return noop;
}

/** Declares a database that hosts several DB schemas. */
export function database(_options: { readonly hosts: readonly ServiceHandle<"DB">[] }): ClassDecorator {
  return noop;
}

/**
 * Declares an environment: a target and the values only a person decides.
 * The target's values sit under its name (`gcp: { project, region }`), and
 * each settings element is typed by its `of`. A class that extends another
 * `@environment` class inherits its values.
 */
export function environment<
  const T extends TargetName | undefined = undefined,
  const S extends readonly unknown[] = readonly [],
>(_options: EnvironmentOptions<T, S>): ClassDecorator {
  return noop;
}
