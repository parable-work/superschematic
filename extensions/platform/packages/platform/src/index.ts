// @superschematic/platform: the authoring package for the example Platform
// kind (utils/psgen/extensions/platform). psgen reads the AST, so the
// decorator is a no-op at runtime, as in the core packages; the types exist
// so authors get completion and tsc catches a bad argument before psgen does.

export type Visibility = "public" | "internal";

export interface PlatformConfig {
  readonly description?: string;
  readonly visibility: Visibility;
  /** Member service names. */
  readonly services: readonly string[];
  /** Resource kind -> the members that share one instance of it. */
  readonly shared?: Readonly<Record<string, readonly string[]>>;
}

export function platform(_config: PlatformConfig): ClassDecorator {
  return () => {};
}
