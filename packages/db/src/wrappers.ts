// FK ON DELETE actions expressible on a relation. SET NULL clears the
// reference instead of deleting the row, so it needs a nullable foreign key
// column: the loader accepts it only on Nullable<Relation<T, ...>> (D83).
export type OnDeleteAction = "CASCADE" | "RESTRICT" | "NO ACTION" | "SET NULL";

// Options carried by a relation's second type argument. onDelete controls the
// generated FK's ON DELETE action; omitting it keeps the default (CASCADE).
export type RelationOptions = { onDelete?: OnDeleteAction };

// Opts is a constrained phantom: the `extends RelationOptions` bound gives the
// author typecheck and autocomplete on the option value, while the superschematic Go
// loader reads the actual value from the second type-argument node (mirroring
// Validate<T, C>). No embed is added because nothing reads a `__relationOpts`.
export type Relation<T, Opts extends RelationOptions = {}> = T & { readonly __relation: true };
// The rows of T whose relation points at this row.
export type HasMany<T> = T[] & { readonly __hasMany: true };
export type ManyToMany<T> = T[] & { readonly __manyToMany: true };
// The database generates the value on insert.
export type AutoGenerate<T> = T & { readonly __autogen: true };
// The field is stored as JSONB.
export type JsonField<T> = T & { readonly __jsonField: true };
