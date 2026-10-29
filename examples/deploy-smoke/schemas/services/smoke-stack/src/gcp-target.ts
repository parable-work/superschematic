// The authoring types of the gcp target: its values and the settings of each
// deployable kind, read from the JSON Schemas the target and its platforms
// register, which the loader checks the same values against
// (docs/stack-model.md, section 4.3). superschematic stack init wrote it.
export {};

declare module "@superschematic/stack" {
  interface Targets {
    gcp: {
      values: { project: string; projectNumber?: string; region: string; production?: boolean; telemetry?: { endpoint?: string; sampleRatio?: number } };
      server: { minInstances?: number; maxInstances?: number; concurrency?: number; cpu?: string; cpuAlwaysAllocated?: boolean; memory?: string; telemetry?: boolean };
      database: { tier?: string; highAvailability?: boolean; version?: "POSTGRES_15" | "POSTGRES_16" | "POSTGRES_17"; deletionProtection?: boolean; diskSize?: number };
      job: { cpu?: string; memory?: string; telemetry?: boolean };
      site: {};
      bucket: { versioning?: boolean; deleteAfterDays?: number };
      worker: { cpu?: string; memory?: string; telemetry?: boolean };
      runner: { cpu?: string; memory?: string; telemetry?: boolean };
      deriver: { cpu?: string; memory?: string; telemetry?: boolean };
      memoryStore: { nodeType?: "SHARED_CORE_NANO" | "STANDARD_SMALL" | "HIGHCPU_MEDIUM" | "HIGHMEM_MEDIUM" | "HIGHMEM_XLARGE" | "STANDARD_LARGE" | "HIGHMEM_2XLARGE" | "CUSTOM_PICO" | "CUSTOM_MICRO" | "CUSTOM_MINI"; replicas?: number };
      app: { minInstances?: number; maxInstances?: number; concurrency?: number; cpu?: string; cpuAlwaysAllocated?: boolean; memory?: string; telemetry?: boolean };
    };
  }
}
