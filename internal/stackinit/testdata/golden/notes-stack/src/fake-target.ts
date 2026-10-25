// The authoring types of the fake target: its values and the settings of each
// deployable kind, read from the JSON Schemas the target and its platforms
// register, which the loader checks the same values against
// (docs/stack-model.md, section 4.3). superschematic stack init wrote it.
export {};

declare module "@superschematic/stack" {
  interface Targets {
    fake: {
      values: { project: string; projectNumber?: string; region: string; production?: boolean };
      server: { minInstances?: number; public?: boolean };
      database: { tier?: "small" | "large"; highAvailability?: boolean };
      job: { cpu?: string };
      site: {};
      bucket: { versioning?: boolean };
      worker: { memory?: string };
      runner: {};
      deriver: {};
      memoryStore: { nodeType?: string };
    };
  }
}
