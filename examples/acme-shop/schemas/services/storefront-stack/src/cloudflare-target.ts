// The authoring types of the cloudflare target: its values and the settings of each
// deployable kind, read from the JSON Schemas the target and its platforms
// register, which the loader checks the same values against
// (docs/stack-model.md, section 4.3). superschematic stack init wrote it.
export {};

declare module "@superschematic/stack" {
  interface Targets {
    cloudflare: {
      values: { accountId: string; workersSubdomain?: string; secretsStoreId?: string };
      server: { logs?: boolean; compatibilityDate?: string; cpuMs?: number; placement?: "smart" };
    };
  }
}
