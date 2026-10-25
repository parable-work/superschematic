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
 * port of the environment's Postgres container and of its storage emulator
 * (D54), a server's, an app's (D78) and a site's settings its port, and a
 * database, a job, a worker, a bucket, an engine's runner (D58) and a
 * deriver (D74) take no settings. A port left out is derived from the
 * stack, the environment and the deployable.
 */
export interface LocalTarget {
  values: { postgresImage?: string; postgresPort?: number; storageImage?: string; storagePort?: number };
  server: { port?: number };
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  database: {};
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  job: {};
  site: { port?: number };
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  bucket: {};
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  worker: {};
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  runner: {};
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  deriver: {};
  app: { port?: number };
}

/** A target's name: a key of `Targets`. */
export type TargetName = Extract<keyof Targets, string>;

/** A deployable kind: what a target gives a settings type for. */
export type DeployableKind = "server" | "database" | "job" | "site" | "bucket" | "worker" | "runner" | "deriver" | "app";

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
    : Of extends ServiceHandle<"Site">
      ? SettingsOf<T, "site">
      : Of extends ServiceHandle<"Bucket">
        ? SettingsOf<T, "bucket">
        : Of extends DeployableClass
          ? { readonly env?: EnvOf<unknown> } & (SettingsOf<T, "server"> | SettingsOf<T, "database">)
          : never);

/**
 * When a job runs in an environment (D52): its schedule, a five-field cron
 * read in an IANA time zone, whether the schedule runs, and whether the job
 * runs on every deploy. A member of a parameterized environment runs no
 * schedule unless `enabled` turns it on.
 */
export type JobSchedule = {
  /** Replaces the `@job` schedule: minute, hour, day of the month, month and day of the week. */
  readonly schedule?: string;
  /** The IANA time zone the schedule is read in. */
  readonly timeZone?: string;
  /** Turns the schedule on or off; off, the job runs only on demand. */
  readonly enabled?: boolean;
  /**
   * Runs the job once on every deploy, after the last rollout wave and
   * before the contract phase, and fails the deploy when its last try
   * fails; `stack dev` runs it once the environment is up. Off unless set;
   * `false` turns off what an environment this one extends turned on.
   */
  readonly onDeploy?: boolean;
};

/**
 * What a job's settings element is: its API's handle in `of` and the job's
 * class name in `job`, which the handle's jobs type, its schedule, the
 * target's job settings, and an env of the API's config, over what the
 * API's server is given.
 */
type JobElementOf<T, Of> =
  Of extends ServiceHandle<"API", infer C, infer J>
    ? { readonly of: Of; readonly job: J; readonly platform?: string; readonly env?: EnvOf<C> } & JobSchedule &
        SettingsOf<T, "job">
    : never;

/**
 * How a worker runs in an environment (D53): how many instances, each
 * handling how many messages at a time, and whether it runs at all. A
 * worker that is off runs no instance, and its queue's messages wait.
 */
export type WorkerRun = {
  /** How many instances run; one unless set, and the fewest when maxInstances is set. */
  readonly instances?: number;
  /**
   * The most instances a target may run as the queue's depth grows, a
   * range from `instances` (D53, amended). No target scales a worker yet:
   * gcp refuses a range, and local runs one process.
   */
  readonly maxInstances?: number;
  /** How many messages an instance handles at a time; the `@worker` concurrency unless set. */
  readonly concurrency?: number;
  /** Turns the worker on or off. */
  readonly enabled?: boolean;
};

/**
 * What a worker's settings element is: its API's handle in `of` and the
 * worker's class name in `worker`, which the handle's workers type, how it
 * runs, the target's worker settings, and an env of the API's config, over
 * what the API's server is given.
 */
type WorkerElementOf<T, Of> =
  Of extends ServiceHandle<"API", infer C, string, infer W>
    ? { readonly of: Of; readonly worker: W; readonly platform?: string; readonly env?: EnvOf<C> } & WorkerRun &
        SettingsOf<T, "worker">
    : never;

/**
 * Whether an engine's runner runs in an environment (D58). A runner that is
 * off runs no instance: no reaction, schedule, Timers deadline or event-log
 * retention runs until it is on again, when each reaction reads on from its
 * cursor, a schedule that came due runs once and every overdue deadline
 * fires.
 */
export type RunnerRun = {
  /** Turns the runner on or off. */
  readonly enabled?: boolean;
};

/**
 * What an engine's runner's settings element is: its database's handle in
 * `of` and `runner: true`, whether it runs, and the target's runner
 * settings. A runner reads no config but its database's connection, so it
 * takes no env.
 */
type RunnerElementOf<T, Of> =
  Of extends ServiceHandle<"DB">
    ? { readonly of: Of; readonly runner: true; readonly platform?: string } & RunnerRun & SettingsOf<T, "runner">
    : never;

/**
 * How a deriver runs in an environment (D74): whether its one instance
 * runs, and how many answers it runs at a time. A deriver that is off runs
 * no instance, and its fields stay stale until it is on again.
 */
export type DeriverRun = {
  /** How many instances run: one unless set, or zero. */
  readonly instances?: 0 | 1;
  /** How many answers an instance runs at a time; the `@deriver` concurrency unless set. */
  readonly concurrency?: number;
  /** Turns the deriver on or off. */
  readonly enabled?: boolean;
};

/**
 * What a deriver's settings element is: its API's handle in `of` and the
 * deriver's class name in `deriver`, which the handle's derivers type, how
 * it runs, the target's deriver settings, and an env of the API's config,
 * over what the API's server is given.
 */
type DeriverElementOf<T, Of> =
  Of extends ServiceHandle<"API", infer C, string, string, infer D>
    ? { readonly of: Of; readonly deriver: D; readonly platform?: string; readonly env?: EnvOf<C> } & DeriverRun &
        SettingsOf<T, "deriver">
    : never;

/**
 * Whether an app runs in an environment (D78). An app that is off is in
 * the environment, with its edges, and its target runs nothing for it.
 */
export type AppRun = {
  /** Turns the app on or off. */
  readonly enabled?: boolean;
};

/**
 * What an app's settings element is: its API's handle in `of` and the
 * app's class name in `app`, which the handle's apps type, whether it
 * runs, the target's app settings, and an env of the API's config, over
 * what the API's server is given.
 */
type AppElementOf<T, Of> =
  Of extends ServiceHandle<"API", infer C, string, string, string, infer A>
    ? { readonly of: Of; readonly app: A; readonly platform?: string; readonly env?: EnvOf<C> } & AppRun &
        SettingsOf<T, "app">
    : never;

/**
 * An app the stack exposes (D78): its API's handle in `of` and the app's
 * class name in `app`, which the handle's apps type.
 */
export type ExposedApp<H> = H extends ServiceHandle<"API", unknown, string, string, string, infer A>
  ? { readonly of: H; readonly app: A }
  : never;

/** Every key of W, or of any member when W is a union. */
type KeysOf<W> = W extends unknown ? keyof W : never;

/** W with every key of E that W lacks refused. */
type Exact<W, E> = W & { readonly [K in Exclude<keyof E, KeysOf<W>>]: never };

/** The env of a settings element whose `of` is Of. */
type EnvFor<Of> = Of extends ServiceHandle<"API", infer C> ? EnvOf<C> : EnvOf<unknown>;

/**
 * One settings element checked: its `of` picks the settings type, a `job`,
 * a `worker`, a `deriver` or an `app` beside an API's handle makes it the
 * element of that job, worker, deriver or app, and `runner: true` beside a
 * DB's handle that of the engine's
 * runner of the database; its env is the config's fields, and a key none
 * takes is refused. An element that names a platform, which may be another
 * target's, takes any settings key; the loader checks it against that
 * platform.
 */
export type SettingsElement<T, E> = E extends { readonly of: infer Of }
  ? Exact<
      E extends { readonly job: unknown }
        ? JobElementOf<E extends { readonly platform: string } ? undefined : T, Of>
        : E extends { readonly worker: unknown }
          ? WorkerElementOf<E extends { readonly platform: string } ? undefined : T, Of>
          : E extends { readonly deriver: unknown }
            ? DeriverElementOf<E extends { readonly platform: string } ? undefined : T, Of>
            : E extends { readonly app: unknown }
              ? AppElementOf<E extends { readonly platform: string } ? undefined : T, Of>
              : E extends { readonly runner: unknown }
                ? RunnerElementOf<E extends { readonly platform: string } ? undefined : T, Of>
                : ElementOf<E extends { readonly platform: string } ? undefined : T, Of>,
      E
    > &
      (E extends { readonly env: infer V } ? { readonly env: Exact<EnvFor<Of>, V> } : {})
  : { readonly of: ServiceHandle<"API" | "DB" | "Site" | "Bucket"> | DeployableClass };

/** The argument of `@environment`. */
export type EnvironmentOptions<T extends TargetName | undefined, S extends readonly unknown[]> = {
  /** The target that places every deployable no setting places elsewhere. */
  readonly target?: T;
  /** Where exposed servers are reached. */
  readonly domain?: string;
  /** The DNS platform that holds the domain's records, with its values: `{ cloudflare: { zone, zoneId } }`. */
  readonly dns?: { readonly [platform: string]: { readonly [value: string]: unknown } };
  /** Settings per deployable; `of` is a service handle, a Bucket's among them (D54), or a declared deployable's class, with `job`, `worker`, `deriver`, `app` or `runner: true` beside a handle for a job, a worker, a deriver, an app or an engine's runner. */
  readonly settings?: S & { readonly [I in keyof S]: SettingsElement<T, S[I]> };
  /** Makes the environment a family, one member per value. */
  readonly parameters?: readonly string[];
} & { readonly [K in NoInfer<T> & string]?: ValuesOf<K> };

const noop: ClassDecorator = () => {};

/**
 * Declares the stack: its entry points and what is reachable from outside.
 * A site (a Site service's handle) is always exposed, named in `expose` or
 * not, and each API it calls must be exposed (D55). An app is exposed
 * beside its API's handle, `{ of: ShopStock, app: "Backoffice" }` (D78).
 */
export function stack<const X extends readonly unknown[] = readonly []>(_options: {
  readonly deploy?: readonly ServiceHandle<"API" | "DB" | "Site">[];
  readonly expose?: X & {
    readonly [I in keyof X]: X[I] extends { readonly app: unknown; readonly of: infer H }
      ? ExposedApp<H>
      : ServiceHandle<"API" | "Site"> | DeployableClass;
  };
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
