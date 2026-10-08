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
// An API with @job classes, whose names the sentinel writes as the third
// type argument.
const ShopCart = service<"API", ShopApiConfig, "ExpireCarts" | "SendDigest">({ name: "shop-cart", kind: SchemaKind.API });

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

// @ts-expect-error only API and DB services are deployed
@stack({ deploy: [ShopCommon] })
export abstract class DeploysGeneral {}

// The core's local target needs no package: its values set the Postgres
// container, and a server's settings its port.
@environment({ target: "local" })
export abstract class Dev {}

@environment({
  target: "local",
  local: { postgresImage: "postgres:17-alpine", postgresPort: 55432 },
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
    { of: ShopCart, job: "SendDigest", enabled: false },
    // A handle with no jobs type takes any job name, which the loader checks.
    { of: ShopOrders, job: "Anything", enabled: true },
  ],
})
export abstract class JobSettings {}

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
