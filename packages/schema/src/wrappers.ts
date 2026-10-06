export type ValidateConfig = {
  readonly min?: number;
  readonly max?: number;
  /** Largest multipart upload in bytes; only on a file-upload scalar field. */
  readonly uploadMaxBytes?: number;
  readonly minLength?: number;
  readonly maxLength?: number;
  readonly listMin?: number;
  readonly listMax?: number;
  readonly pattern?: string;
  readonly format?: string;
};

/**
 * The phantom base of every wrapper below: it carries the wrapped type and
 * is never set. A wrapper is an intersection with T, from which a
 * conditional type cannot take T back; through the base it can
 * (`F extends Wrapped<infer T> ? T : F`), so a mapped type unwraps a field's
 * type, as `@superschematic/stack` types an environment's `env`.
 */
export type Wrapped<T> = { readonly __wrapped: T };

export type Default<T, V> = T & Wrapped<T> & { readonly __default: V };
export type PlatformDefault<T> = T & Wrapped<T> & { readonly __platformDefault: true };
export type Nullable<T> = T | null;
export type Validate<T, C extends ValidateConfig> = T & Wrapped<T> & { readonly __validate: C };
export type Secret<T> = T & Wrapped<T> & { readonly __secret: true };
