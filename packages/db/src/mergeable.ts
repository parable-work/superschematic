/**
 * Marks a DB table whose rows can be folded into one another, the core
 * Mergeable trait (D147): `implements Mergeable`. Two rows that turn out to
 * be one thing, two labels, two customers, are merged, and everything that
 * pointed at one points at the other. The table's repository gains
 * `mergeInto({ from, into })`, which in one transaction repoints every
 * relation of the schema that references `from` to `into`, deletes a
 * referencing row that a unique index of its table would then refuse, and
 * deletes `from`, softly when the table deletes softly. It answers how many
 * rows of each table it repointed and deleted.
 *
 * ```ts
 * export abstract class Label implements Mergeable {
 *   @key
 *   id: AutoGenerate<Identity.UUID>;
 *   name: string;
 * }
 * ```
 *
 * Like UserRole, it resolves to an empty object type and takes no type
 * argument, so the compiler imposes no members.
 */
export type Mergeable = NonNullable<unknown>;
