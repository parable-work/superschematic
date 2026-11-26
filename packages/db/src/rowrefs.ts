/*
References and attachments on any row (D154): the rows a row points at, and
the files it holds in a bucket.
*/

import type { SchemaClass } from "./decorators";

/** The options of @references. */
export interface ReferencesOptions {
  /** The tables a reference may point at; any table of the database whose key is one column unless set. */
  readonly tables?: readonly SchemaClass[];
  /**
   * The @queue class a target's delete enqueues one message on for each reference it clears, so the row that held
   * it hears of it. The build fills its fields: holderTable, holderKey, targetTable, targetKey and role. Unset, a
   * delete clears the references and enqueues nothing.
   */
  readonly cleared?: SchemaClass;
  /** The most references one row holds; 1000 unless set. */
  readonly maxPerRow?: number;
}

/**
 * Lets each row of a DB table point at rows of any table of the database
 * (D154), each target named by its table and key, with a role the app gives
 * it.
 *
 * ```ts
 * @queue()
 * export abstract class ReferenceCleared {}
 *
 * @references<Ticket>({ tables: [Ticket, Note], cleared: ReferenceCleared })
 * export abstract class Ticket { ... }
 * ```
 *
 * The build adds the sub-table ticket_references beside the table, with an
 * index on each target, and on each table a reference may point at a trigger
 * that clears the references to a row it deletes. The runtime's
 * orm.references adds and removes them, lists a row's, and lists the rows
 * that point at one (referencedBy) across every table: each held to what the
 * caller may read, so a reference to a row the caller cannot see is left out,
 * and only the owner of a private row hears how many others there are.
 */
export function references<T>(_options?: ReferencesOptions): ClassDecorator {
  return () => {};
}

/** The options of @attachments. */
export interface AttachmentsOptions {
  /**
   * The @queue class a file's delete enqueues a message on, naming its object, as does an upload that was never
   * completed once its URL expires. The build fills its fields: objectKey, contentHash and reason. Its worker calls
   * orm.attachments.release, which deletes the object once no file of any table names it.
   */
  readonly released: SchemaClass;
  /** The largest file, in bytes; 100 MiB unless set, and at most 5 GiB, the largest one signed PUT uploads. */
  readonly maxFileBytes?: number;
  /** The most bytes one row's files hold together, those whose upload is under way among them; 1 GiB unless set. */
  readonly maxRowBytes?: number;
  /** The most files one row holds; 100 unless set. */
  readonly maxFiles?: number;
  /** The media types a file may have, each a type (image/png) or a range (image/*); any unless set. */
  readonly mediaTypes?: readonly string[];
}

/**
 * Lets each row of a DB table hold files in a bucket (D154): each a name, a
 * media type, a size, the SHA-256 of its bytes, a caption, who uploaded it and
 * when.
 *
 * ```ts
 * @queue()
 * export abstract class AttachmentReleased {}
 *
 * @attachments<Ticket>({ released: AttachmentReleased, maxFileBytes: 25 * 1024 * 1024 })
 * export abstract class Ticket { ... }
 * ```
 *
 * The build adds the sub-table ticket_attachments beside the table. A browser
 * uploads a file straight to the bucket through a URL the API signs
 * (orm.attachments.begin), and the API checks the bytes against what the
 * upload declared before the file joins the row (complete). A file filed onto
 * another row reuses its object, with no second upload, and the object is
 * deleted once no row names it.
 */
export function attachments<T>(_options: AttachmentsOptions): ClassDecorator {
  return () => {};
}
