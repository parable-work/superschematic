import type { BehaviorConfigs } from "@superschematic/schema";

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
// The column holds the value encrypted with the DB service's field keys
// (outputs.sql.encryption, D85): AES-256-GCM, bound to the table, the column
// and the row's key. The ORMs encrypt it on write and decrypt it on read, and
// a read holds it as a SecretValue. It has no filter, index or default.
export type Encrypted<T extends string> = T & { readonly __encrypted: true };

// Workflow's config, as @behavior("Workflow", ...) takes it: the BehaviorConfigs
// entry of @superschematic/schema itself, so a field's config and a
// decorator's cannot drift.
export type WorkflowConfig = BehaviorConfigs["Workflow"];
// A state machine on the field (D103):
// `status: Workflow<{ states: ["draft", "paid"]; transitions: [{ from: "draft"; to: "paid" }] }>`.
// The config is a type argument of literal types, or `typeof` a constant
// declared `as const`. The type stays a table: the field's column holds the
// state, and each of Workflow's other fields takes a column named for the
// field (status_entered_at). Only Workflow's operations (status.transition)
// change them; the generated repository reads them and leaves them out of its
// inputs. The field's type is the union of the config's states. A table
// carries Workflow on any number of fields, and never beside @behavior.
export type Workflow<C extends WorkflowConfig> = C["states"][number];
