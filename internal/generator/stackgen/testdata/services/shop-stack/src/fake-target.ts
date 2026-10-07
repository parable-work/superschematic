// The authoring types of stacktest's fake target, as a target's package
// ships them (docs/stack-model.md, section 4.3).
export {};

declare module "@superschematic/stack" {
  interface Targets {
    fake: {
      values: { project: string; projectNumber?: string; region: string; production?: boolean };
      server: { minInstances?: number; public?: boolean };
      database: { tier?: "small" | "large"; highAvailability?: boolean };
    };
  }
}
