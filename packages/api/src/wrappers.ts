export type QueryParam<T> = T & { readonly __queryParam: true };
export type EncryptedField<T> = T & { readonly __encrypted: true };
