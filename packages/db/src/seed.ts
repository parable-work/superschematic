/*
Seed rows (#625, #667): the rows a DB table holds from its first migration
on, policy an app keeps as data in its schema (ground rule 1).
*/

/** A value a seeded field holds: text, a number, a boolean, null, a list's values, or a JSON field's value. */
export type SeedValue = string | number | boolean | null | readonly SeedValue[] | { readonly [key: string]: SeedValue };

/**
 * A seeded row: its fields' values by name. A field the row leaves out takes its column's default. The fields the
 * build fills on a table (a notifier's part, a channel identity) are fields a row may name, so the row is not
 * typed by the class.
 */
export type SeedRow = { readonly [field: string]: SeedValue };

/** The options of @seedRows. */
export interface SeedRowsOptions {
  /**
   * The fields whose values identify a seeded row. Unset, the table's key when every row gives it, else the first
   * unique field, then the first unique index, every row gives; the build refuses a table with none.
   */
  readonly key?: readonly string[];
}

/**
 * The rows a DB table holds from its first migration on:
 *
 * ```ts
 * @seedRows<ChannelTrust>([
 *   { channelKind: "sms", level: "low", permissions: [] },
 *   { channelKind: "email", level: "standard", permissions: null },
 * ])
 * @notifier("channelTrust")
 * export abstract class ChannelTrust {}
 * ```
 *
 * `create.sql` inserts each row, on Postgres and SQLite. A migration plan inserts a row the schema adds, by its key,
 * and never updates or deletes a seeded row: once it is in the table it is the app's to edit, and a changed seed
 * reaches only a database that does not hold its key. Each insert leaves a row of the same key alone, so it may run
 * again.
 */
export function seedRows<_T = unknown>(_rows: readonly SeedRow[], _options?: SeedRowsOptions): ClassDecorator {
  return () => {};
}
