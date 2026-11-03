// The authoring types of the cloudflare target: its values and the settings of each
// deployable kind, read from the JSON Schemas the target and its platforms
// register, which the loader checks the same values against
// (docs/stack-model.md, section 4.3). superschematic stack init wrote it.
export {};

declare module "@superschematic/stack" {
  interface Targets {
    cloudflare: {
      values: { accountId: string; workersSubdomain?: string; secretsStoreId?: string; telemetry?: { endpoint: string; sampleRatio?: number } };
      server: { logs?: boolean; compatibilityDate?: string; cpuMs?: number; placement?: "smart"; telemetry?: boolean };
      database: { locationHint?: "wnam" | "enam" | "weur" | "eeur" | "apac" | "oc"; jurisdiction?: "eu" | "fedramp" | "us" } | { hyperdrive?: string; caching?: boolean };
      job: { logs?: boolean; cpuMs?: number; telemetry?: boolean };
      site: {};
      bucket: { jurisdiction?: "eu" | "fedramp"; locationHint?: "wnam" | "enam" | "weur" | "eeur" | "apac" | "oc"; storageClass?: "Standard" | "InfrequentAccess"; deleteAfterDays?: number };
      worker: { logs?: boolean; cpuMs?: number; telemetry?: boolean; drainSeconds?: number };
    };
  }
}
