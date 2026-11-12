// The authoring types of the embedded target: its values and the settings of each
// deployable kind, read from the JSON Schemas the target and its platforms
// register, which the loader checks the same values against
// (docs/stack-model.md, section 4.3). superschematic stack init wrote it.
export {};

declare module "@superschematic/stack" {
  interface Targets {
    embedded: {
      values: { port?: number };
      server: {};
      database: { adoption?: { model: string; check: string } };
      site: {};
      worker: {};
    };
  }
}
