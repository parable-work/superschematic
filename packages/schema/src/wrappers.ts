export type ValidateConfig = {
  readonly min?: number;
  readonly max?: number;
  readonly minLength?: number;
  readonly maxLength?: number;
  readonly listMin?: number;
  readonly listMax?: number;
  readonly pattern?: string;
  readonly format?: string;
};

export type Default<T, V> = T & { readonly __default: V };
export type PlatformDefault<T> = T & { readonly __platformDefault: true };
export type Nullable<T> = T | null;
export type Validate<T, C extends ValidateConfig> = T & { readonly __validate: C };
export type Secret<T> = T & { readonly __secret: true };
