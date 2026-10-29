// Type-level checks of @superschematic/stack (docs/stack-model.md, section
// 4.3). Nothing imports this file: `bun run typecheck`, which `make ts`
// runs, checks it, and fails on an @ts-expect-error that no longer errors.
import type { Default, Secret } from "@superschematic/schema";
import { SchemaKind, service } from "@superschematic/schema-config";
import { database, environment, server, stack } from "../src/index";

// A target's authoring package augments Targets; this one stands in for it.
declare module "../src/index" {
  interface Targets {
    fake: {
      values: { project: string; region: string; production?: boolean };
      server: { minInstances?: number };
      database: { tier?: string; highAvailability?: boolean };
      job: { cpu?: string };
      bucket: { versioning?: boolean };
      worker: { memory?: string };
      runner: { memory?: string };
      memoryStore: { nodeType?: string };
    };
  }
}

enum LogLevel {
  Debug = "debug",
  Info = "info",
  Warn = "warn",
}

abstract class PaymentsSecrets {
  STRIPE_KEY: Secret<string>;
}

abstract class ShopApiConfig extends PaymentsSecrets {
  LOG_LEVEL: Default<LogLevel, LogLevel.Info>;
  MAX_PAGE: Default<number, 50>;
  PREVIEW_ID?: string;
}

// What the sentinel generator writes: an API handle with its config type,
// and a DB handle with none.
const ShopApi = service<"API", ShopApiConfig>({ name: "shop-api", kind: SchemaKind.API });
const ShopOrders = service({ name: "shop-orders", kind: SchemaKind.API });
const ShopDb = service({ name: "shop-db", kind: SchemaKind.DB });
const ShopCommon = service({ name: "shop-common", kind: SchemaKind.General });
// A Bucket service's handle (D54).
const ShopMedia = service({ name: "shop-media", kind: SchemaKind.Bucket });
// A MemoryStore service's handle (D75).
const ShopCache = service({ name: "shop-cache", kind: SchemaKind.MemoryStore });
// An API with @job classes, whose names the sentinel writes as the third
// type argument.
const ShopCart = service<"API", ShopApiConfig, "ExpireCarts" | "SendDigest">({ name: "shop-cart", kind: SchemaKind.API });
// An API with @worker classes and no @job class, whose workers the
// sentinel writes as the fourth type argument, with the jobs never.
const ShopFulfilment = service<"API", ShopApiConfig, never, "FulfilOrders">({ name: "shop-fulfilment", kind: SchemaKind.API });
// An API with @deriver classes and no @job or @worker class, whose
// derivers the sentinel writes as the fifth type argument (D74).
const ShopSupport = service<"API", ShopApiConfig, never, never, "TriageTickets">({ name: "shop-support", kind: SchemaKind.API });
// An API with @app classes and no @job, @worker or @deriver class, whose
// apps the sentinel writes as the sixth type argument (D78).
const ShopStock = service<"API", ShopApiConfig, never, never, never, "Backoffice">({ name: "shop-stock", kind: SchemaKind.API });

@stack({ deploy: [ShopApi, ShopOrders], expose: [ShopApi] })
export abstract class Shop {}

@server({ serves: [ShopApi, ShopOrders] })
export abstract class Backend {}

@database({ hosts: [ShopDb] })
export abstract class Data {}

@environment({
  target: "fake",
  fake: { project: "acme-prod", region: "us-east1" },
  domain: "acme.dev",
  dns: { "fake.dns": { zone: "acme.dev" } },
  settings: [
    { of: ShopDb, tier: "large", highAvailability: true },
    { of: ShopApi, minInstances: 1, env: { LOG_LEVEL: "warn", MAX_PAGE: 100 } },
    { of: ShopApi, env: { LOG_LEVEL: LogLevel.Debug, PREVIEW_ID: { parameter: "pr" } } },
    // A handle with no config type takes any field.
    { of: ShopOrders, env: { FULFILLMENT_REGION: "us" } },
    // A declared deployable's class takes either kind's settings.
    { of: Backend, minInstances: 2, env: { ANY_FIELD: "x" } },
    { of: Data, tier: "small" },
    // A bucket takes the target's bucket settings.
    { of: ShopMedia, versioning: true },
    // A memory store takes the target's memoryStore settings.
    { of: ShopCache, nodeType: "SHARED_CORE_NANO" },
    // A platform outside the target takes its own settings, which the
    // loader checks.
    { of: ShopDb, platform: "other.sql", storageGb: 10 },
  ],
  parameters: ["pr"],
})
export abstract class Production {}

// An environment that inherits its target sets settings the loader checks.
@environment({ parameters: ["pr"], settings: [{ of: ShopApi, anything: 1, env: { LOG_LEVEL: "info" } }] })
export abstract class Preview extends Production {}

@environment({
  // @ts-expect-error a target no package registers
  target: "gcp",
})
export abstract class UnknownTarget {}

@environment({
  target: "fake",
  // @ts-expect-error the values of a target sit under its own name
  other: { project: "p", region: "r" },
})
export abstract class ValuesUnderAnotherName {}

@environment({
  target: "fake",
  // @ts-expect-error region is required by the target's values
  fake: { project: "p" },
})
export abstract class MissingValue {}

@environment({
  // @ts-expect-error values need the target named
  fake: { project: "p", region: "r" },
})
export abstract class ValuesWithoutTarget {}

@environment({
  target: "fake",
  // @ts-expect-error a secret takes no literal
  settings: [{ of: ShopApi, env: { STRIPE_KEY: "sk_live" } }],
})
export abstract class SecretLiteral {}

@environment({
  target: "fake",
  // @ts-expect-error an env key that is not a field of the config
  settings: [{ of: ShopApi, env: { LOG_LEVL: "warn" } }],
})
export abstract class UnknownField {}

@environment({
  target: "fake",
  // @ts-expect-error a value the field's type refuses
  settings: [{ of: ShopApi, env: { LOG_LEVEL: "verbose" } }],
})
export abstract class WrongValue {}

@environment({
  target: "fake",
  // @ts-expect-error a server's settings on a database
  settings: [{ of: ShopDb, minInstances: 1 }],
})
export abstract class ServerSettingOnDatabase {}

@environment({
  target: "fake",
  // @ts-expect-error a database's settings on a server
  settings: [{ of: ShopApi, tier: "large" }],
})
export abstract class DatabaseSettingOnServer {}

@environment({
  target: "fake",
  // @ts-expect-error a database has no env
  settings: [{ of: ShopDb, env: { LOG_LEVEL: "warn" } }],
})
export abstract class EnvOnDatabase {}

@environment({
  target: "fake",
  // @ts-expect-error a server's settings on a bucket
  settings: [{ of: ShopMedia, minInstances: 1 }],
})
export abstract class ServerSettingOnBucket {}

@environment({
  target: "fake",
  // @ts-expect-error a bucket has no env
  settings: [{ of: ShopMedia, env: { LOG_LEVEL: "warn" } }],
})
export abstract class EnvOnBucket {}

// A memory store an environment turns off (D75).
@environment({ target: "fake", settings: [{ of: ShopCache, enabled: false }] })
export abstract class NoCache {}

@environment({
  target: "fake",
  // @ts-expect-error a bucket's settings on a memory store
  settings: [{ of: ShopCache, versioning: true }],
})
export abstract class BucketSettingOnMemoryStore {}

@environment({
  target: "fake",
  // @ts-expect-error a bucket is not turned off
  settings: [{ of: ShopMedia, enabled: false }],
})
export abstract class BucketOff {}

@environment({
  target: "fake",
  // @ts-expect-error a memory store has no env
  settings: [{ of: ShopCache, env: { LOG_LEVEL: "warn" } }],
})
export abstract class EnvOnMemoryStore {}

@environment({
  target: "fake",
  // @ts-expect-error a General service is no deployable
  settings: [{ of: ShopCommon }],
})
export abstract class GeneralDeployable {}

// @ts-expect-error a server serves API services
@server({ serves: [ShopDb] })
export abstract class ServesDb {}

// @ts-expect-error a database hosts DB services
@database({ hosts: [ShopApi] })
export abstract class HostsApi {}

// @ts-expect-error only API, DB and Site services are deployed
@stack({ deploy: [ShopCommon] })
export abstract class DeploysGeneral {}

// The core's local target needs no package: its values set the Postgres
// container, and a server's settings its port.
@environment({ target: "local" })
export abstract class Dev {}

@environment({
  target: "local",
  local: { postgresImage: "postgres:17-alpine", postgresPort: 55432, storagePort: 54443, memoryStorePort: 56379 },
  settings: [{ of: ShopApi, port: 8080, env: { LOG_LEVEL: "debug" } }, { of: Backend, port: 8081 }],
})
export abstract class PinnedDev {}

@environment({
  target: "local",
  // @ts-expect-error a value the local target does not take
  local: { project: "acme" },
})
export abstract class LocalProject {}

@environment({
  target: "local",
  // @ts-expect-error a local database takes no settings
  settings: [{ of: ShopDb, tier: "large" }],
})
export abstract class LocalDatabaseSetting {}

@environment({
  target: "local",
  // @ts-expect-error a local bucket takes no settings
  settings: [{ of: ShopMedia, versioning: true }],
})
export abstract class LocalBucketSetting {}

// The local target's memory store takes no settings, and is turned off.
@environment({ target: "local", settings: [{ of: ShopCache, enabled: false }] })
export abstract class LocalNoCache {}

@environment({
  target: "local",
  // @ts-expect-error a local memory store takes no settings
  settings: [{ of: ShopCache, nodeType: "x" }],
})
export abstract class LocalMemoryStoreSetting {}

@environment({
  target: "local",
  // @ts-expect-error a port is a number
  settings: [{ of: ShopApi, port: "8080" }],
})
export abstract class LocalPortString {}

// A job's element names its API's handle and the job's class; it changes
// the schedule, takes the target's job settings, and an env of its API's
// config (D52).
@environment({
  target: "fake",
  fake: { project: "acme-staging", region: "us-east1" },
  settings: [
    { of: ShopCart, job: "ExpireCarts", schedule: "0 * * * *", timeZone: "Europe/Paris", cpu: "1", env: { LOG_LEVEL: "warn" } },
    { of: ShopCart, job: "SendDigest", enabled: false, onDeploy: true },
    // A handle with no jobs type takes any job name, which the loader checks.
    { of: ShopOrders, job: "Anything", enabled: true },
  ],
})
export abstract class JobSettings {}

@environment({
  target: "fake",
  // @ts-expect-error onDeploy is a job's setting
  settings: [{ of: ShopApi, onDeploy: true }],
})
export abstract class OnDeployOnServer {}

@environment({
  target: "fake",
  // @ts-expect-error onDeploy is a boolean
  settings: [{ of: ShopCart, job: "ExpireCarts", onDeploy: "yes" }],
})
export abstract class OnDeployString {}

@environment({
  target: "local",
  settings: [{ of: ShopCart, job: "ExpireCarts", schedule: "* * * * *" }],
})
export abstract class LocalJob {}

@environment({
  target: "fake",
  // @ts-expect-error a job the API does not declare
  settings: [{ of: ShopCart, job: "ExpireCrats" }],
})
export abstract class UnknownJob {}

@environment({
  target: "fake",
  // @ts-expect-error a schedule is a job's setting
  settings: [{ of: ShopApi, schedule: "0 * * * *" }],
})
export abstract class ScheduleOnServer {}

@environment({
  target: "fake",
  // @ts-expect-error a DB service declares no job
  settings: [{ of: ShopDb, job: "ExpireCarts" }],
})
export abstract class JobOfDatabase {}

@environment({
  target: "fake",
  // @ts-expect-error a server's settings on a job
  settings: [{ of: ShopCart, job: "ExpireCarts", minInstances: 1 }],
})
export abstract class ServerSettingOnJob {}

@environment({
  target: "fake",
  // @ts-expect-error enabled is a boolean
  settings: [{ of: ShopCart, job: "ExpireCarts", enabled: "no" }],
})
export abstract class EnabledString {}

@environment({
  target: "local",
  // @ts-expect-error a local job takes no settings
  settings: [{ of: ShopCart, job: "ExpireCarts", port: 8080 }],
})
export abstract class LocalJobPort {}

// A site (D55): a stack deploys it, may name it in expose, and gives it the
// local target's site settings, its port, and no env.
const ShopWeb = service({ name: "shop-web", kind: SchemaKind.Site });

@stack({ deploy: [ShopApi, ShopWeb], expose: [ShopApi, ShopWeb] })
export abstract class SiteShop {}

@environment({ target: "local", settings: [{ of: ShopWeb, port: 8090 }] })
export abstract class SiteDev {}

@environment({
  target: "local",
  // @ts-expect-error a site reads no env
  settings: [{ of: ShopWeb, env: { API_URL: "x" } }],
})
export abstract class SiteEnv {}

@environment({
  target: "local",
  // @ts-expect-error a site takes a site's settings, not a database's
  settings: [{ of: ShopWeb, tier: "large" }],
})
export abstract class DatabaseSettingOnSite {}

// An origin (D91): a site and the APIs it serves on its own origin, or
// every API it calls with apis left out.
@stack({ deploy: [ShopApi, ShopWeb], origins: [{ site: ShopWeb, apis: [ShopApi] }] })
export abstract class OriginShop {}

@stack({ deploy: [ShopApi, ShopWeb], origins: [{ site: ShopWeb }] })
export abstract class OriginOfEveryCall {}

// @ts-expect-error an origin's site is a Site service's handle
@stack({ deploy: [ShopApi, ShopWeb], origins: [{ site: ShopApi }] })
export abstract class OriginOfAnAPI {}

// @ts-expect-error an origin serves APIs
@stack({ deploy: [ShopApi, ShopWeb], origins: [{ site: ShopWeb, apis: [ShopWeb] }] })
export abstract class OriginServingASite {}

// A worker's element names its API's handle and the worker's class; it
// sets how many instances run and how many messages each handles, takes
// the target's worker settings, and an env of its API's config (D53).
@environment({
  target: "fake",
  fake: { project: "acme-staging", region: "us-east1" },
  settings: [
    { of: ShopFulfilment, worker: "FulfilOrders", instances: 3, concurrency: 8, memory: "1Gi", env: { LOG_LEVEL: "warn" } },
    // A handle with no workers type takes any worker name, which the
    // loader checks.
    { of: ShopOrders, worker: "Anything", enabled: false },
  ],
})
export abstract class WorkerSettings {}

@environment({
  target: "local",
  settings: [{ of: ShopFulfilment, worker: "FulfilOrders", concurrency: 2 }],
})
export abstract class LocalWorker {}

// A deriver's element names its API's handle and the deriver's class; it
// turns the deriver on or off, runs one instance or none, sets how many
// answers it runs at a time, and takes an env of its API's config (D74).
@environment({
  target: "fake",
  fake: { project: "acme-staging", region: "us-east1" },
  settings: [
    { of: ShopSupport, deriver: "TriageTickets", instances: 1, concurrency: 8, env: { LOG_LEVEL: "warn" } },
    { of: ShopOrders, deriver: "Anything", enabled: false },
  ],
})
export abstract class DeriverSettings {}

@environment({
  target: "fake",
  // @ts-expect-error a deriver the API does not declare
  settings: [{ of: ShopSupport, deriver: "TriageTicket" }],
})
export abstract class UnknownDeriver {}

@environment({
  target: "fake",
  // @ts-expect-error a deriver runs one instance or none
  settings: [{ of: ShopSupport, deriver: "TriageTickets", instances: 2 }],
})
export abstract class TwoDerivers {}

@environment({
  target: "fake",
  // @ts-expect-error a schedule is a job's setting, not a deriver's
  settings: [{ of: ShopSupport, deriver: "TriageTickets", schedule: "0 * * * *" }],
})
export abstract class ScheduleOnDeriver {}

@environment({
  target: "fake",
  // @ts-expect-error a worker the API does not declare
  settings: [{ of: ShopFulfilment, worker: "FulfilOrder" }],
})
export abstract class UnknownWorker {}

@environment({
  target: "fake",
  // @ts-expect-error an API with workers and no jobs names no job
  settings: [{ of: ShopFulfilment, job: "FulfilOrders" }],
})
export abstract class WorkerAsJob {}

@environment({
  target: "fake",
  // @ts-expect-error instances are a worker's setting
  settings: [{ of: ShopApi, instances: 2 }],
})
export abstract class InstancesOnServer {}

@environment({
  target: "fake",
  // @ts-expect-error a schedule is a job's setting, not a worker's
  settings: [{ of: ShopFulfilment, worker: "FulfilOrders", schedule: "0 * * * *" }],
})
export abstract class ScheduleOnWorker {}

@environment({
  target: "fake",
  // @ts-expect-error onDeploy is a job's setting, not a worker's
  settings: [{ of: ShopFulfilment, worker: "FulfilOrders", onDeploy: true }],
})
export abstract class OnDeployOnWorker {}

@environment({
  target: "fake",
  // @ts-expect-error a DB service declares no worker
  settings: [{ of: ShopDb, worker: "FulfilOrders" }],
})
export abstract class WorkerOfDatabase {}

@environment({
  target: "local",
  // @ts-expect-error a local worker takes no settings
  settings: [{ of: ShopFulfilment, worker: "FulfilOrders", memory: "1Gi" }],
})
export abstract class LocalWorkerMemory {}

// An engine's runner's element names its database's handle and
// `runner: true`; it turns the runner on or off and takes the target's
// runner settings (D58).
@environment({
  target: "fake",
  fake: { project: "acme-staging", region: "us-east1" },
  settings: [{ of: ShopDb, runner: true, enabled: false, memory: "1Gi" }],
})
export abstract class RunnerSettings {}

@environment({
  target: "local",
  settings: [{ of: ShopDb, runner: true, enabled: true }],
})
export abstract class LocalRunner {}

@environment({
  target: "fake",
  // A platform outside the target takes its own settings.
  settings: [{ of: ShopDb, runner: true, platform: "other.runner", cpu: "2" }],
})
export abstract class RunnerElsewhere {}

@environment({
  target: "fake",
  // @ts-expect-error an API's handle has no engine's runner
  settings: [{ of: ShopApi, runner: true }],
})
export abstract class RunnerOfApi {}

@environment({
  target: "fake",
  // @ts-expect-error runner names the runner with true
  settings: [{ of: ShopDb, runner: false }],
})
export abstract class RunnerFalse {}

@environment({
  target: "fake",
  // @ts-expect-error instances are a worker's setting
  settings: [{ of: ShopDb, runner: true, instances: 2 }],
})
export abstract class InstancesOnRunner {}

@environment({
  target: "fake",
  // @ts-expect-error a runner reads no env
  settings: [{ of: ShopDb, runner: true, env: { LOG_LEVEL: "warn" } }],
})
export abstract class EnvOnRunner {}

@environment({
  target: "local",
  // @ts-expect-error a local runner takes no settings
  settings: [{ of: ShopDb, runner: true, memory: "1Gi" }],
})
export abstract class LocalRunnerMemory {}

// An app is exposed beside its API's handle, which types its name (D78).
@stack({ deploy: [ShopStock], expose: [ShopApi, { of: ShopStock, app: "Backoffice" }] })
export abstract class ShopWithApp {}

// @ts-expect-error an app the API does not declare
@stack({ deploy: [ShopStock], expose: [{ of: ShopStock, app: "Kiosk" }] })
export abstract class UnknownExposedApp {}

// An app's element names its API's handle and the app's class; it turns
// the app on or off, takes the target's app settings and an env of its
// API's config (D78).
@environment({
  target: "local",
  settings: [
    { of: ShopStock, app: "Backoffice", enabled: false, port: 3000, env: { LOG_LEVEL: "warn" } },
    { of: ShopOrders, app: "Anything" },
  ],
})
export abstract class AppSettings {}

@environment({
  target: "fake",
  // @ts-expect-error an app the API does not declare
  settings: [{ of: ShopStock, app: "Kiosk" }],
})
export abstract class UnknownApp {}

@environment({
  target: "fake",
  // @ts-expect-error instances are a worker's or a deriver's setting, not an app's
  settings: [{ of: ShopStock, app: "Backoffice", instances: 2 }],
})
export abstract class InstancesOnApp {}

@environment({
  target: "fake",
  // @ts-expect-error an app belongs to an API, not a database
  settings: [{ of: ShopDb, app: "Backoffice" }],
})
export abstract class AppOfDb {}
