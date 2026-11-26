/*
Search across tables (D155): the fields of a table the database's search
index holds.
*/

import type { SchemaClass } from "./decorators";

/** The options of @searchable. */
export interface SearchableOptions<T> {
  /** The text field that titles each row: a match in it ranks highest, and a hit shows it. */
  readonly title: keyof T & string;
  /** The text fields that hold the row's body, in order; the title alone unless set. */
  readonly body?: (keyof T & string) | readonly (keyof T & string)[];
  /**
   * The field a search's scope keeps to, a text or a key (a relation's among them), as a workspace or a project.
   * Unset, a row is in no scope, and a search with a scope leaves it out.
   */
  readonly scope?: keyof T & string;
  /**
   * The @queue class a write that changes the indexed text enqueues a message on, naming the row. The build fills
   * its fields: sourceTable and sourceKey. Its worker calls indexSearch, which rebuilds the row's index row.
   */
  readonly queue: SchemaClass;
  /** false leaves the row's conversation (Comments on the table) out of its index row; indexed unless set. */
  readonly comments?: boolean;
  /** false leaves its files' extracted text (@attachments) out of its index row; indexed unless set. */
  readonly attachments?: boolean;
}

/**
 * Puts each row of a DB table in the database's search index (D155): its
 * title, its body, the text of its conversation and its files' extracted
 * text, ranked across every @searchable table in one query.
 *
 * ```ts
 * @queue()
 * export abstract class SearchIndexing {}
 *
 * @searchable<Ticket>({ title: "title", body: ["summary", "details"], scope: "project", queue: SearchIndexing })
 * export abstract class Ticket { ... }
 * ```
 *
 * The build adds the table search_index to the database, with a row per
 * record of every @searchable table, and a trigger on each that copies the
 * row's audience into its index row and deletes the index row with the row.
 * A write that changes the indexed text enqueues a message, whose worker
 * calls indexSearch. A search ranks with Postgres full text, keeps to what
 * its caller may read inside the query, and may re-rank by embeddings. An
 * Encrypted<T> field is never indexed: the build refuses one here.
 */
export function searchable<T>(_options: SearchableOptions<T>): ClassDecorator {
  return () => {};
}
