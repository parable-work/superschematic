// FK ON DELETE actions expressible on a relation. SET NULL is intentionally
// omitted: it needs a nullable FK column, and relations are non-null.
export type OnDeleteAction = "CASCADE" | "RESTRICT" | "NO ACTION";

// Options carried by a relation's second type argument. onDelete controls the
// generated FK's ON DELETE action; omitting it keeps the default (CASCADE).
export type RelationOptions = { onDelete?: OnDeleteAction };

// Opts is a constrained phantom: the `extends RelationOptions` bound gives the
// author typecheck and autocomplete on the option value, while the superschematic Go
// loader reads the actual value from the second type-argument node (mirroring
// Validate<T, C>). No embed is added because nothing reads a `__relationOpts`.
export type Relation<T, Opts extends RelationOptions = {}> = T & { readonly __relation: true };
export type HasMany<T> = T[] & { readonly __hasMany: true };
export type ManyToMany<T> = T[] & { readonly __manyToMany: true };
export type AutoGenerate<T> = T & { readonly __autogen: true };
export type JsonField<T> = T & { readonly __jsonField: true };
