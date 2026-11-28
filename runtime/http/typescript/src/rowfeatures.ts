import { ObjectNotFoundError, type Bucket } from './bucket.js';
import type { AccessCaller } from './caller.js';
import { sha256Stream } from './sha256.js';
import { sqlitePlaceholders } from './sql-text.js';
import { uuidFromDb, uuidToDb, uuidValue } from './uuid.js';

/*
References and attachments on any row (D154), the ORM's references and
attachments members, which an ORM and a transaction hold when their
database declares @references or @attachments (or a table's Comments, whose
ref blocks are references too).

A reference is a row of a @references table's sub-table: the row that holds
it, the target's table and key, and a role. Every read keeps to what its
caller may read, through the target's and the holder's own repositories,
which apply the caller's audience and grants as any read of theirs does
(D156): a reference to a row the caller cannot read is left out entirely,
and referencedBy answers only the owner of a private row how many rows it
cannot see point at it. A target's delete clears the references to it, a
trigger's work, whatever deletes it.

An attachment is a row of an @attachments table's sub-table: a file in a
bucket, its name, media type, size, SHA-256, caption, uploader and time, and
the object that holds its bytes. A browser uploads it straight to the bucket
through a URL begin signs, so its bytes never pass through the API; complete
reads the object back, hashing it as it streams, and holds it to the size
and hash begin declared before the file joins the row. file copies or moves
a file onto another row, the same object, with no second upload. A file's
delete enqueues its object's release, and release, which the released
queue's worker calls, deletes the object once no file of any table names
it. Extraction and previews are a file processor's (D159): complete asks
extraction, a file's first view asks its preview, and processed records
what the processor made.

On SQLite and D1 (D182) the members run the same reads and writes in
SQLite's words (sqlOf): a parameter ?n, a cast CAST(... AS TEXT), a ref
block matched through json_each, and no advisory lock, since a transaction
on SQLite holds the database's one write lock from its BEGIN IMMEDIATE, and
D1 runs each statement alone. A row's time reads back as its text there,
which the members read as a Date.
*/

/** A row of a table, by its type's name and its key in its wire form (a UUID in base62). */
export interface RowRef {
  readonly table: string;
  readonly key: string;
}

/** A sub-table of a row, as orm.ts describes it: the holder's repository member, the sub-table, its column of the row's key, and whether that key is a UUID. */
export interface RowTableDefinition {
  readonly member: string;
  readonly table: string;
  readonly row: string;
  readonly uuid?: boolean;
}

/** A @references table's sub-table, the tables it may point at (any when absent), and the most references a row holds. */
export interface ReferencesHolderDefinition extends RowTableDefinition {
  readonly tables?: readonly string[];
  readonly maxPerRow: number;
}

/** A table a reference may point at: its repository member, table and key column, whether its key is a UUID, and an @audience table's owner and resolved audience columns. */
export interface ReferenceTargetDefinition {
  readonly member: string;
  readonly table: string;
  readonly key: string;
  readonly uuid?: boolean;
  readonly owner?: string;
  readonly restricted?: string;
}

/** A table's Comments messages sub-table, whose ref blocks are the row's references too: its blocks, state and message id columns. */
export interface CommentsSourceDefinition extends RowTableDefinition {
  readonly blocks: string;
  readonly state: string;
  readonly id: string;
}

/** The references of a database, by holder type, target type and conversation (D154). */
export interface RowReferencesDefinition {
  readonly holders: Readonly<Record<string, ReferencesHolderDefinition>>;
  readonly targets: Readonly<Record<string, ReferenceTargetDefinition>>;
  readonly comments?: Readonly<Record<string, CommentsSourceDefinition>>;
}

/** An @attachments table's sub-table, its bounds, and the released queue's table. */
export interface AttachmentsHolderDefinition extends RowTableDefinition {
  readonly maxFileBytes: number;
  readonly maxRowBytes: number;
  readonly maxFiles: number;
  readonly mediaTypes?: readonly string[];
  readonly released: string;
}

/** The attachments of a database, by holder type (D154). */
export interface AttachmentsDefinition {
  readonly holders: Readonly<Record<string, AttachmentsHolderDefinition>>;
}

/** A reference a row holds. */
export interface Reference {
  readonly id: string;
  readonly from: RowRef;
  readonly to: RowRef;
  /** Its role, "" for none. */
  readonly role: string;
  readonly addedBy?: string;
  readonly addedAt: Date;
}

/** A row that points at another: through a reference it holds, with its role, or through a ref block of a message of its conversation. */
export interface Backlink extends RowRef {
  readonly via: 'reference' | 'comment';
  readonly role?: string;
  /** The message, for a ref block. */
  readonly message?: number;
}

/** The rows that point at a row, as its reader may see them. */
export interface Backlinks {
  readonly items: readonly Backlink[];
  /**
   * How many rows the reader cannot see point at the row: present only for
   * the owner of a row whose audience is not everyone (D156). Anyone else
   * hears of the rows they see, and of no others.
   */
  readonly hidden?: number;
}

/** A file of a row. */
export interface Attachment {
  readonly id: string;
  /** The row that holds it. */
  readonly table: string;
  readonly key: string;
  readonly name: string;
  readonly mediaType: string;
  readonly size: number;
  /** The SHA-256 of its bytes, 64 lowercase hex digits. */
  readonly contentHash: string;
  /** The bucket's object that holds its bytes, which a file filed elsewhere shares. */
  readonly objectKey: string;
  readonly caption?: string;
  /** pending while its upload is under way, ready once its bytes were checked. */
  readonly state: 'pending' | 'ready';
  readonly uploadedBy?: string;
  readonly uploadedAt: Date;
  readonly expiresAt?: Date;
  /** The file it was filed from, as table:key:id. */
  readonly filedFrom?: string;
  readonly extraction: ProcessingState;
  readonly extractedText?: string;
  readonly preview: ProcessingState;
  readonly previewObject?: string;
}

/** A file processor's state of an attachment's text or preview (D159): none asked, asked, made, or failed. */
export type ProcessingState = 'none' | 'pending' | 'done' | 'failed';

/** What a browser sends its upload with: a PUT of the file's bytes to url, with headers, before expiresAt. */
export interface Upload {
  readonly url: string;
  readonly method: 'PUT';
  readonly headers: Readonly<Record<string, string>>;
  readonly expiresAt: Date;
}

/** A Comments attachment block that names a file of the row's Attachments by its id. */
export interface AttachmentBlock {
  readonly kind: 'attachment';
  readonly uri: string;
  readonly name: string;
  readonly mediaType: string;
  readonly bytes: number;
  readonly sha256: string;
}

/** The reference operations of an ORM whose database declares @references or Comments (orm.references). */
export interface References {
  /** Makes from point at to with role ("" unless given), once per target and role; answers the reference, the one there when it was there. */
  add(input: { readonly from: RowRef; readonly to: RowRef; readonly role?: string }): Promise<Reference>;
  /** Stops from pointing at to with role, or with any role when role is absent; answers how many it removed. */
  remove(input: { readonly from: RowRef; readonly to: RowRef; readonly role?: string }): Promise<number>;
  /** The references row holds, oldest first, each to a row the reader can read. */
  of(row: RowRef): Promise<Reference[]>;
  /** The rows that point at row, across every table, each one the reader can read, most recent first. */
  referencedBy(row: RowRef, options?: { readonly limit?: number }): Promise<Backlinks>;
}

/** The attachment operations of an ORM whose database declares @attachments (orm.attachments). */
export interface Attachments {
  /** Whether table is an @attachments table, whose rows hold files; no other is asked for a file. */
  holds(table: string): boolean;
  /**
   * Starts an upload of a file to row: a pending file, held to the
   * declaration's bounds with the row's other files and uploads, and a URL
   * the browser PUTs the bytes to, which expires after expiresInSeconds
   * (an hour unless set).
   */
  begin(
    input: { readonly to: RowRef; readonly name: string; readonly mediaType: string; readonly size: number; readonly sha256: string; readonly caption?: string },
    options: { readonly bucket: Bucket; readonly expiresInSeconds?: number }
  ): Promise<{ readonly attachment: Attachment; readonly upload: Upload }>;
  /** Checks an upload's bytes against the size and SHA-256 its begin declared, and makes the file ready; a file whose bytes differ is removed. */
  complete(file: FileRef, options: { readonly bucket: Bucket }): Promise<Attachment>;
  /** The ready files of row, oldest first. */
  list(row: RowRef): Promise<Attachment[]>;
  /** One file of a table's row. */
  get(file: FileRef): Promise<Attachment>;
  /** A URL that downloads the file's bytes until it expires (an hour unless set), and the file, whose preview this first view asks. */
  download(file: FileRef, options: { readonly bucket: Bucket; readonly expiresInSeconds?: number }): Promise<{ readonly url: string; readonly expiresAt: Date; readonly attachment: Attachment }>;
  /** Sets the file's caption, or clears it with null. */
  caption(file: FileRef & { readonly caption: string | null }): Promise<Attachment>;
  /** Removes the file from its row; its object is released once no file names it. */
  detach(file: FileRef): Promise<void>;
  /**
   * Files the file onto another row, which may be of another table, with no
   * second upload: a copy that names the same object, or with move the file
   * itself, leaving its row. When the file's table keeps references that may
   * point at to's table, its row then points at to, with role (filed unless
   * given), so to lists it in its backlinks.
   */
  file(input: FileRef & { readonly to: RowRef; readonly move?: boolean; readonly role?: string }): Promise<Attachment>;
  /** A released message's work, which the released queue's worker calls: deletes the object, and its preview's, once no file names it; uploads never completed are removed first. */
  release(message: { readonly objectKey: string }, options: { readonly bucket: Bucket }): Promise<{ readonly deleted: boolean }>;
  /** The objects whose extraction or preview a processor has yet to make, each once, oldest first. */
  pending(kind: 'extraction' | 'preview', options?: { readonly limit?: number }): Promise<{ objectKey: string; mediaType: string; contentHash: string }[]>;
  /** Records what a file processor made of an object, on every file that names it; answers how many. */
  processed(input: {
    readonly objectKey: string;
    readonly extraction?: { readonly state: 'done' | 'failed'; readonly text?: string };
    readonly preview?: { readonly state: 'done' | 'failed'; readonly object?: string };
  }): Promise<number>;
}

/** A file, by the table of its row and its id. */
export interface FileRef {
  readonly table: string;
  readonly id: string;
}

/** The ORM's references member, on a database that declares them. */
export interface ReferencesMembers {
  readonly references: References;
}

/** The ORM's attachments member, on a database that declares them. */
export interface AttachmentsMembers {
  readonly attachments: Attachments;
}

/**
 * A references or attachments operation the declaration's rules refuse:
 * code names which, details what.
 */
export class RowFeatureError extends Error {
  constructor(
    readonly code:
      | 'not_a_holder'
      | 'not_a_target'
      | 'table_not_allowed'
      | 'too_many'
      | 'invalid'
      | 'media_type'
      | 'too_large'
      | 'row_full'
      | 'not_pending'
      | 'expired'
      | 'not_uploaded'
      | 'mismatch'
      | 'not_ready',
    message: string,
    readonly details: Readonly<Record<string, unknown>> = {}
  ) {
    super(message);
    this.name = 'RowFeatureError';
  }
}

/** The Comments attachment block of a ready file: its uri attachment:<id>. */
export function attachmentBlock(file: Attachment): AttachmentBlock {
  return { kind: 'attachment', uri: `attachment:${file.id}`, name: file.name, mediaType: file.mediaType, bytes: file.size, sha256: file.contentHash };
}

/** The name of the object that holds an object's preview, which a file processor writes and a release deletes with it. */
export function previewObjectOf(objectKey: string): string {
  return `previews/${objectKey}`;
}

/** A repository's read of rows by key, as the references read targets and holders through. */
interface KeyReader {
  getManyByIds(ids: readonly unknown[]): Promise<Map<unknown, unknown>>;
}

/** What the members run through: the ORM or the transaction that holds them. */
export interface RowFeatureHost {
  /** The dialect of the database the host's statements run on (D182); Postgres unless sqlite. */
  readonly dialect?: 'postgres' | 'sqlite';
  readonly references?: RowReferencesDefinition;
  readonly attachments?: AttachmentsDefinition;
  /** Runs a statement as written, reading every row. */
  query(text: string, values: readonly unknown[]): Promise<{ rows: Record<string, unknown>[]; rowCount: number }>;
  /** The repository of a table, by its member, which reads as the ORM's reader. */
  repository(member: string): KeyReader | undefined;
  /** Runs fn in a transaction, the host's own, or a savepoint of it. */
  transaction<T>(fn: (host: RowFeatureHost) => Promise<T>): Promise<T>;
  /** The user the writes record. */
  actingUser(): string | undefined;
  /** Whom the reads keep to: every row, or a caller. */
  sees(): 'all' | AccessCaller;
  notFound(message: string): Error;
}

const DEFAULT_LIMIT = 100;
const MAX_LIMIT = 1000;
/** The most holders referencedBy reads of a row before it reads which the reader sees. */
const MAX_SCAN = 10_000;
/** The most keys one read of a repository asks for. */
const BATCH = 500;
/** How long an upload's URL lives unless begin is told, and the longest it may. */
const DEFAULT_UPLOAD_SECONDS = 60 * 60;
const MAX_UPLOAD_SECONDS = 7 * 24 * 60 * 60;
/** How long after an upload's URL expires its never-completed file is released. */
const RELEASE_GRACE_SECONDS = 60 * 60;
const SHA256_HEX = /^[0-9a-f]{64}$/u;
/** A media type a file may have (D154): a lowercase type/subtype without parameters. */
export const MEDIA_TYPE = /^[a-z0-9][a-z0-9!#$&^_.+-]{0,126}\/[a-z0-9][a-z0-9!#$&^_.+-]{0,126}$/u;

/** The ORM's references and attachments members over host, as the database declares them. */
export function rowFeatureMembers(host: RowFeatureHost): Partial<ReferencesMembers & AttachmentsMembers> {
  const out: Partial<ReferencesMembers & AttachmentsMembers> = {};
  const dialect = inDialect(host);
  if (host.references) Object.assign(out, { references: referencesOf(dialect) });
  if (host.attachments) Object.assign(out, { attachments: attachmentsOf(dialect) });
  return out;
}

// ---- the dialect ----

/** The words of a statement that differ between Postgres and SQLite (D182). */
interface Sql {
  /** text with each $n a parameter of the dialect. */
  readonly statement: (text: string) => string;
  /** expr as text, as a key or an id compares. */
  readonly text: (expr: string) => string;
  /** expr as an integer, as a count reads. */
  readonly int: (expr: string) => string;
  /** The database's clock. */
  readonly now: string;
  /** A share lock of the row a statement reads, or none. */
  readonly forShare: string;
  /** Whether a statement may take an advisory lock. */
  readonly locks: boolean;
}

const POSTGRES_SQL: Sql = {
  statement: text => text,
  text: expr => `${expr}::text`,
  int: expr => `${expr}::int`,
  now: 'now()',
  forShare: ' FOR SHARE',
  locks: true,
};

/** SQLite's, which holds a time as its RFC 3339 text to the millisecond, as its schema's defaults write it. */
const SQLITE_SQL: Sql = {
  statement: sqlitePlaceholders,
  text: expr => `CAST(${expr} AS TEXT)`,
  int: expr => `CAST(${expr} AS INTEGER)`,
  now: "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')",
  forShare: '',
  locks: false,
};

function sqlOf(host: RowFeatureHost): Sql {
  return host.dialect === 'sqlite' ? SQLITE_SQL : POSTGRES_SQL;
}

/** A time a row read holds: a Date on Postgres, its text on SQLite. */
function timeOf(value: unknown): Date {
  return value instanceof Date ? value : new Date(String(value));
}

/** A boolean a row read holds: true on Postgres, 1 on SQLite. */
function truthOf(value: unknown): boolean {
  return value === true || value === 1 || value === 1n;
}

/** host's statements written in its dialect's words, $n among them. */
function inDialect(host: RowFeatureHost): RowFeatureHost {
  const sql = sqlOf(host);
  if (sql === POSTGRES_SQL) return host;
  return {
    ...host,
    query: (text, values) => host.query(sql.statement(text), values),
    transaction: fn => host.transaction(h => fn(inDialect(h))),
  };
}

// ---- keys ----

function dbKey(uuid: boolean | undefined, key: string): string {
  if (typeof key !== 'string' || key === '') throw new RowFeatureError('invalid', `a row's key is a non-empty string, not ${JSON.stringify(key)}`);
  return uuid ? uuidToDb(key) : key;
}

function wireKey(uuid: boolean | undefined, text: string): string {
  return uuid ? uuidFromDb(text) : text;
}

function sameSubject(a: string | undefined, b: string | undefined): boolean {
  if (a === undefined || b === undefined) return false;
  const x = uuidValue(a);
  const y = uuidValue(b);
  return x !== undefined ? x === y : a === b;
}

/** The keys of keys the reader of member sees, in their wire form. */
async function visibleKeys(host: RowFeatureHost, member: string, keys: readonly string[]): Promise<Set<string>> {
  const repository = host.repository(member);
  if (!repository) throw new Error(`orm: no repository ${member}`);
  const seen = new Set<string>();
  for (let i = 0; i < keys.length; i += BATCH) {
    const rows = await repository.getManyByIds(keys.slice(i, i + BATCH));
    for (const key of rows.keys()) seen.add(String(key));
  }
  return seen;
}

/**
 * Holds name's advisory lock to the end of the transaction on Postgres. On
 * SQLite the transaction holds the database's write lock, and D1 runs each
 * statement alone, so there is none to take.
 */
async function lock(host: RowFeatureHost, name: string): Promise<void> {
  if (!sqlOf(host).locks) return;
  await host.query('SELECT pg_advisory_xact_lock(hashtextextended($1, 0))', [name]);
}

function limitOf(limit: number | undefined): number {
  if (limit === undefined) return DEFAULT_LIMIT;
  if (!Number.isInteger(limit) || limit < 1 || limit > MAX_LIMIT) throw new RowFeatureError('invalid', `a limit is a whole number from 1 to ${MAX_LIMIT}, not ${limit}`);
  return limit;
}

// ---- references ----

function referencesOf(host: RowFeatureHost): References {
  const definition = host.references!;
  const sql = sqlOf(host);
  const holderOf = (table: string): ReferencesHolderDefinition => {
    const holder = definition.holders[table];
    if (!holder) throw new RowFeatureError('not_a_holder', `${table} keeps no references: it is no @references table`, { table });
    return holder;
  };
  const targetOf = (table: string): ReferenceTargetDefinition => {
    const target = definition.targets[table];
    if (!target) throw new RowFeatureError('not_a_target', `${table} is no table a reference may point at`, { table });
    return target;
  };
  const reference = (from: RowRef, holder: ReferencesHolderDefinition, row: Record<string, unknown>): Reference => {
    const to = definition.targets[String(row.target_table)];
    return {
      id: uuidFromDb(row.id),
      from,
      to: { table: String(row.target_table), key: wireKey(to?.uuid, String(row.target_key)) },
      role: String(row.role ?? ''),
      ...(row.added_by ? { addedBy: uuidFromDb(row.added_by) } : {}),
      addedAt: timeOf(row.added_at),
    };
  };
  const columns = (_holder: ReferencesHolderDefinition) => `${sql.text('id')} AS id, "target_table", "target_key", "role", ${sql.text('added_by')} AS added_by, "added_at"`;
  /** Throws not found unless the reader sees the row. */
  const mustSee = async (h: RowFeatureHost, row: RowRef, member: string): Promise<void> => {
    if (!(await visibleKeys(h, member, [row.key])).has(row.key)) throw h.notFound(`${row.table} ${row.key} is not there`);
  };

  return {
    async add({ from, to, role = '' }) {
      const holder = holderOf(from.table);
      const target = targetOf(to.table);
      if (holder.tables && !holder.tables.includes(to.table)) {
        throw new RowFeatureError('table_not_allowed', `${from.table} points only at ${holder.tables.join(', ')}, and not at ${to.table}`, { table: to.table, allowed: holder.tables });
      }
      if (typeof role !== 'string' || role.length > 128) throw new RowFeatureError('invalid', 'a reference\'s role is a string of at most 128 characters');
      const fromKey = dbKey(holder.uuid, from.key);
      const toKey = dbKey(target.uuid, to.key);
      return host.transaction(async h => {
        // A reader who sees neither row is told the same of each: it is not
        // there, so a reference never shows whether a hidden row exists.
        await mustSee(h, from, holder.member);
        await mustSee(h, to, target.member);
        await lock(h, `references:${holder.table}:${fromKey}`);
        const keyText = (await h.query(`SELECT ${sql.text(target.key)} AS key FROM ${target.table} WHERE ${target.key} = $1`, [toKey])).rows[0]?.key;
        if (keyText === undefined) throw h.notFound(`${to.table} ${to.key} is not there`);
        const held = await h.query(
          `SELECT ${columns(holder)} FROM ${holder.table} WHERE ${holder.row} = $1 AND "target_table" = $2 AND "target_key" = $3 AND "role" = $4`,
          [fromKey, to.table, keyText, role]
        );
        if (held.rows[0]) return reference(from, holder, held.rows[0]);
        const count = Number((await h.query(`SELECT ${sql.int('count(*)')} AS n FROM ${holder.table} WHERE ${holder.row} = $1`, [fromKey])).rows[0]?.n ?? 0);
        if (count >= holder.maxPerRow) {
          throw new RowFeatureError('too_many', `${from.table} ${from.key} holds ${count} references, the most it may`, { max: holder.maxPerRow });
        }
        const user = h.actingUser();
        const inserted = await h.query(
          `INSERT INTO ${holder.table} (${holder.row}, "target_table", "target_key", "role", "added_by") VALUES ($1, $2, $3, $4, $5) RETURNING ${columns(holder)}`,
          [fromKey, to.table, keyText, role, user === undefined ? null : uuidToDb(user)]
        );
        return reference(from, holder, inserted.rows[0]!);
      });
    },

    async remove({ from, to, role }) {
      const holder = holderOf(from.table);
      const target = targetOf(to.table);
      const fromKey = dbKey(holder.uuid, from.key);
      const toKey = dbKey(target.uuid, to.key);
      return host.transaction(async h => {
        await mustSee(h, from, holder.member);
        const values: unknown[] = [fromKey, to.table, toKey];
        let roleCondition = '';
        if (role !== undefined) {
          values.push(role);
          roleCondition = ' AND "role" = $4';
        }
        const removed = await h.query(`DELETE FROM ${holder.table} WHERE ${holder.row} = $1 AND "target_table" = $2 AND "target_key" = ${sql.text('$3')}${roleCondition}`, values);
        return removed.rowCount;
      });
    },

    async of(row) {
      const holder = holderOf(row.table);
      const fromKey = dbKey(holder.uuid, row.key);
      await mustSee(host, row, holder.member);
      const rows = (await host.query(`SELECT ${columns(holder)} FROM ${holder.table} WHERE ${holder.row} = $1 ORDER BY "added_at", id`, [fromKey])).rows;
      const references = rows.map(r => reference(row, holder, r));
      // Each target the reader cannot read is left out, so its existence
      // does not show.
      const byTable = new Map<string, string[]>();
      for (const r of references) byTable.set(r.to.table, [...(byTable.get(r.to.table) ?? []), r.to.key]);
      const seen = new Map<string, Set<string>>();
      for (const [table, keys] of byTable) {
        const target = definition.targets[table];
        seen.set(table, target ? await visibleKeys(host, target.member, keys) : new Set());
      }
      return references.filter(r => seen.get(r.to.table)?.has(r.to.key));
    },

    async referencedBy(row, options = {}) {
      const limit = limitOf(options.limit);
      const target = targetOf(row.table);
      const key = dbKey(target.uuid, row.key);
      await mustSee(host, row, target.member);
      const keyText = (await host.query(`SELECT ${sql.text(target.key)} AS key FROM ${target.table} WHERE ${target.key} = $1`, [key])).rows[0]?.key;
      if (keyText === undefined) throw host.notFound(`${row.table} ${row.key} is not there`);
      // Every row that points at the row, as the system reads them: the
      // references held to it, then the ref blocks of messages that name it.
      interface Found {
        readonly table: string;
        readonly key: string;
        readonly via: 'reference' | 'comment';
        readonly role?: string;
        readonly message?: number;
        readonly at: number;
      }
      const found: Found[] = [];
      for (const [table, holder] of Object.entries(definition.holders)) {
        if (holder.tables && !holder.tables.includes(row.table)) continue;
        const rows = (
          await host.query(
            `SELECT ${sql.text(holder.row)} AS holder, "role", "added_at" FROM ${holder.table} WHERE "target_table" = $1 AND "target_key" = $2 ORDER BY "added_at" DESC LIMIT ${MAX_SCAN}`,
            [row.table, keyText]
          )
        ).rows;
        for (const r of rows) found.push({ table, key: wireKey(holder.uuid, String(r.holder)), via: 'reference', role: String(r.role ?? ''), at: timeOf(r.added_at).getTime() });
      }
      const ids = target.uuid ? [uuidFromDb(keyText), keyText] : [keyText];
      for (const [table, source] of Object.entries(definition.comments ?? {})) {
        // A ref block of a message that names the row: JSONB containment on
        // Postgres, and on SQLite, whose blocks are JSON text, a block
        // json_each finds.
        const probes = host.dialect === 'sqlite' ? [row.table, ...ids] : ids.map(id => JSON.stringify([{ kind: 'ref', schema: row.table, id }]));
        const conditions =
          host.dialect === 'sqlite'
            ? `EXISTS (SELECT 1 FROM json_each(${source.blocks}) AS block WHERE json_extract(block.value, '$.kind') = 'ref' AND json_extract(block.value, '$.schema') = $1 AND json_extract(block.value, '$.id') IN (${ids.map((_, i) => `$${i + 2}`).join(', ')}))`
            : probes.map((_, i) => `${source.blocks} @> $${i + 1}::jsonb`).join(' OR ');
        const rows = (
          await host.query(
            `SELECT ${sql.text(source.row)} AS holder, ${source.id} AS message FROM ${source.table} WHERE (${conditions}) AND ${source.state} IN ('posted', 'interrupted') ORDER BY ${source.id} DESC LIMIT ${MAX_SCAN}`,
            probes
          )
        ).rows;
        for (const r of rows) found.push({ table, key: wireKey(source.uuid, String(r.holder)), via: 'comment', message: Number(r.message), at: Number(r.message) });
      }
      // Which of them the reader sees, through each holder's repository.
      const byTable = new Map<string, string[]>();
      for (const f of found) byTable.set(f.table, [...new Set([...(byTable.get(f.table) ?? []), f.key])]);
      const seen = new Map<string, Set<string>>();
      let holders = 0;
      let visible = 0;
      for (const [table, keys] of byTable) {
        const member = definition.holders[table]?.member ?? definition.comments?.[table]?.member ?? definition.targets[table]?.member;
        const keep = member ? await visibleKeys(host, member, keys) : new Set<string>();
        seen.set(table, keep);
        holders += keys.length;
        visible += keys.filter(k => keep.has(k)).length;
      }
      const items: Backlink[] = [];
      for (const f of found.sort((a, b) => b.at - a.at)) {
        if (items.length >= limit) break;
        if (!seen.get(f.table)?.has(f.key)) continue;
        items.push({ table: f.table, key: f.key, via: f.via, ...(f.role !== undefined ? { role: f.role } : {}), ...(f.message !== undefined ? { message: f.message } : {}) });
      }
      // Only the owner of a row whose audience is not everyone hears how many
      // rows they cannot see point at it (D156).
      const sees = host.sees();
      if (sees !== 'all' && target.owner && target.restricted && holders > visible) {
        const own = (await host.query(`SELECT ${sql.text(target.owner)} AS owner, ${target.restricted} IS NOT NULL AS restricted FROM ${target.table} WHERE ${target.key} = $1`, [key])).rows[0];
        if (truthOf(own?.restricted) && sameSubject(String(own?.owner ?? ''), sees.subject)) return { items, hidden: holders - visible };
      }
      return { items };
    },
  };
}

// ---- attachments ----

/** The columns of a file, each named as attachment reads it, those read as text cast in sql's words. */
function attachmentColumnsOf(holder: AttachmentsHolderDefinition, sql: Sql): string {
  return [
    `${sql.text('id')} AS id`,
    `${sql.text(holder.row)} AS row_key`,
    '"name"',
    '"media_type"',
    `${sql.text('"size"')} AS size`,
    '"content_hash"',
    '"object_key"',
    '"caption"',
    '"state"',
    `${sql.text('"uploaded_by"')} AS uploaded_by`,
    '"uploaded_at"',
    '"expires_at"',
    '"filed_from"',
    '"extraction"',
    '"extracted_text"',
    '"preview"',
    '"preview_object"',
  ].join(', ');
}

/** Whether a declaration's mediaTypes admit mediaType: each is a type/subtype, a family's type/* or any type's; none admits any. */
export function mediaTypeAllowed(allowed: readonly string[] | undefined, mediaType: string): boolean {
  if (!allowed || allowed.length === 0) return true;
  const family = mediaType.split('/')[0];
  return allowed.some(one => one === mediaType || one === '*/*' || (one.endsWith('/*') && one.slice(0, -2) === family));
}

/**
 * Whether name is a file's name (D154): 1 to 255 Unicode code points, as
 * every length counts them (D14, amended) and the Go runtime does, not
 * UTF-16 units; none of them a slash, a backslash or a control character
 * (U+0000 to U+001F, U+007F); and not white space alone, Unicode's
 * White_Space as Go's strings.TrimSpace reads it.
 * runtime/http/testdata/attachments_parity.json holds the vectors both run.
 */
export function isFileName(name: unknown): boolean {
  if (typeof name !== 'string') return false;
  const length = [...name].length;
  // biome-ignore lint/suspicious/noControlCharactersInRegex: a file's name holds no control character
  return length >= 1 && length <= 255 && !/[/\\\u0000-\u001f\u007f]/u.test(name) && !/^\p{White_Space}*$/u.test(name);
}

function checkName(name: string): void {
  if (!isFileName(name)) {
    throw new RowFeatureError('invalid', `a file's name is 1 to 255 characters with no /, \\ or control character, not ${JSON.stringify(name)}`);
  }
}

function attachmentsOf(host: RowFeatureHost): Attachments {
  const definition = host.attachments!;
  const sql = sqlOf(host);
  const attachmentColumns = (holder: AttachmentsHolderDefinition): string => attachmentColumnsOf(holder, sql);
  const holderOf = (table: string): AttachmentsHolderDefinition => {
    const holder = definition.holders[table];
    if (!holder) throw new RowFeatureError('not_a_holder', `${table} holds no files: it is no @attachments table`, { table });
    return holder;
  };
  const attachment = (table: string, holder: AttachmentsHolderDefinition, row: Record<string, unknown>): Attachment => ({
    id: uuidFromDb(row.id),
    table,
    key: wireKey(holder.uuid, String(row.row_key)),
    name: String(row.name),
    mediaType: String(row.media_type),
    size: Number(row.size),
    contentHash: String(row.content_hash),
    objectKey: String(row.object_key),
    ...(row.caption != null ? { caption: String(row.caption) } : {}),
    state: row.state === 'ready' ? 'ready' : 'pending',
    ...(row.uploaded_by ? { uploadedBy: uuidFromDb(row.uploaded_by) } : {}),
    uploadedAt: timeOf(row.uploaded_at),
    ...(row.expires_at ? { expiresAt: timeOf(row.expires_at) } : {}),
    ...(row.filed_from != null ? { filedFrom: String(row.filed_from) } : {}),
    extraction: String(row.extraction) as ProcessingState,
    ...(row.extracted_text != null ? { extractedText: String(row.extracted_text) } : {}),
    preview: String(row.preview) as ProcessingState,
    ...(row.preview_object != null ? { previewObject: String(row.preview_object) } : {}),
  });
  const mustSee = async (h: RowFeatureHost, holder: AttachmentsHolderDefinition, row: RowRef): Promise<void> => {
    if (!(await visibleKeys(h, holder.member, [row.key])).has(row.key)) throw h.notFound(`${row.table} ${row.key} is not there`);
  };
  /** The file, read as the system, once the reader sees its row; not found for either. */
  const read = async (h: RowFeatureHost, file: FileRef, lockRow = ''): Promise<{ holder: AttachmentsHolderDefinition; file: Attachment }> => {
    const holder = holderOf(file.table);
    const id = uuidValue(file.id) === undefined ? undefined : uuidToDb(file.id);
    const row = id === undefined ? undefined : (await h.query(`SELECT ${attachmentColumns(holder)} FROM ${holder.table} WHERE id = $1${lockRow}`, [id])).rows[0];
    if (!row) throw h.notFound(`${file.table} holds no file ${file.id}`);
    const found = attachment(file.table, holder, row);
    if (!(await visibleKeys(h, holder.member, [found.key])).has(found.key)) throw h.notFound(`${file.table} holds no file ${file.id}`);
    return { holder, file: found };
  };
  /** Holds one more file of size to the row's bounds, under the row's lock: the declaration's media types and bounds. */
  const admit = async (h: RowFeatureHost, holder: AttachmentsHolderDefinition, row: RowRef, mediaType: string, size: number): Promise<void> => {
    if (!mediaTypeAllowed(holder.mediaTypes, mediaType)) {
      throw new RowFeatureError('media_type', `${row.table} holds files of ${holder.mediaTypes!.join(', ')}, and not ${mediaType}`, { mediaType, allowed: holder.mediaTypes });
    }
    if (size > holder.maxFileBytes) {
      throw new RowFeatureError('too_large', `a file of ${row.table} is at most ${holder.maxFileBytes} bytes, and this one is ${size}`, { size, max: holder.maxFileBytes });
    }
    const key = dbKey(holder.uuid, row.key);
    await lock(h, `attachments:${holder.table}:${key}`);
    const held = (
      await h.query(
        `SELECT ${sql.int('count(*)')} AS files, ${sql.text('COALESCE(sum("size"), 0)')} AS bytes FROM ${holder.table} WHERE ${holder.row} = $1 AND ("state" = 'ready' OR "expires_at" > ${sql.now})`,
        [key]
      )
    ).rows[0]!;
    if (Number(held.files) + 1 > holder.maxFiles) {
      throw new RowFeatureError('row_full', `${row.table} ${row.key} holds ${held.files} files, the most a row may`, { files: Number(held.files), max: holder.maxFiles });
    }
    if (Number(held.bytes) + size > holder.maxRowBytes) {
      throw new RowFeatureError('row_full', `${row.table} ${row.key}'s files hold ${held.bytes} bytes, and ${size} more would pass its ${holder.maxRowBytes}`, { bytes: Number(held.bytes), size, max: holder.maxRowBytes });
    }
  };
  const user = (h: RowFeatureHost): string | null => {
    const id = h.actingUser();
    return id === undefined ? null : uuidToDb(id);
  };

  return {
    holds(table) {
      return Object.hasOwn(definition.holders, table);
    },

    async begin(input, options) {
      const holder = holderOf(input.to.table);
      checkName(input.name);
      if (typeof input.mediaType !== 'string' || !MEDIA_TYPE.test(input.mediaType)) {
        throw new RowFeatureError('invalid', `a file's media type is type/subtype in lower case with no parameters, not ${JSON.stringify(input.mediaType)}`);
      }
      if (!Number.isSafeInteger(input.size) || input.size < 0) throw new RowFeatureError('invalid', `a file's size is a whole number of bytes, not ${input.size}`);
      if (typeof input.sha256 !== 'string' || !SHA256_HEX.test(input.sha256)) {
        throw new RowFeatureError('invalid', 'a file\'s sha256 is the SHA-256 of its bytes, 64 lowercase hex digits');
      }
      const seconds = options.expiresInSeconds ?? DEFAULT_UPLOAD_SECONDS;
      if (!Number.isInteger(seconds) || seconds < 1 || seconds > MAX_UPLOAD_SECONDS) {
        throw new RowFeatureError('invalid', `an upload's URL lives 1 to ${MAX_UPLOAD_SECONDS} seconds, not ${seconds}`);
      }
      const objectKey = `attachments/${crypto.randomUUID()}`;
      const expiresAt = new Date(Date.now() + seconds * 1000);
      const created = await host.transaction(async h => {
        await mustSee(h, holder, input.to);
        await admit(h, holder, input.to, input.mediaType, input.size);
        const row = (
          await h.query(
            `INSERT INTO ${holder.table} (${holder.row}, "name", "media_type", "size", "content_hash", "object_key", "caption", "state", "uploaded_by", "expires_at")
             VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $9) RETURNING ${attachmentColumns(holder)}`,
            [dbKey(holder.uuid, input.to.key), input.name, input.mediaType, String(input.size), input.sha256, objectKey, input.caption ?? null, user(h), expiresAt]
          )
        ).rows[0]!;
        // An upload never completed is released once its URL has expired.
        await h.query(`INSERT INTO ${holder.released} ("object_key", "content_hash", "reason", "due_at") VALUES ($1, $2, 'expired', $3)`, [
          objectKey,
          input.sha256,
          new Date(expiresAt.getTime() + RELEASE_GRACE_SECONDS * 1000),
        ]);
        return attachment(input.to.table, holder, row);
      });
      const url = await options.bucket.signedUrl(objectKey, { method: 'PUT', expiresInSeconds: seconds, contentType: input.mediaType });
      return { attachment: created, upload: { url, method: 'PUT', headers: { 'content-type': input.mediaType }, expiresAt } };
    },

    async complete(ref, options) {
      const { holder, file } = await read(host, ref);
      if (file.state !== 'pending') throw new RowFeatureError('not_pending', `file ${ref.id} is ${file.state}, not an upload under way`, { state: file.state });
      if (!file.expiresAt || file.expiresAt.getTime() <= Date.now()) throw new RowFeatureError('expired', `the upload of file ${ref.id} expired at ${file.expiresAt?.toISOString()}`);
      let got;
      try {
        got = await options.bucket.get(file.objectKey);
      } catch (error) {
        if (error instanceof ObjectNotFoundError) throw new RowFeatureError('not_uploaded', `the bucket holds no bytes of file ${ref.id} yet`);
        throw error;
      }
      const { hash, size } = await sha256Stream(got.body, file.size);
      if (size !== file.size || hash !== file.contentHash) {
        // The bytes are not the file begin declared: the file goes, and its
        // object with it, once the released message's worker runs.
        await host.query(`DELETE FROM ${holder.table} WHERE id = $1 AND "state" = 'pending'`, [uuidToDb(file.id)]);
        throw new RowFeatureError('mismatch', `the bytes uploaded for file ${ref.id} are not the ones its upload declared`, {
          size: { declared: file.size, uploaded: size },
          sha256: { declared: file.contentHash, uploaded: size > file.size ? undefined : hash },
        });
      }
      const row = (
        await host.query(
          `UPDATE ${holder.table} SET "state" = 'ready', "expires_at" = NULL, "uploaded_at" = ${sql.now}, "extraction" = 'pending' WHERE id = $1 AND "state" = 'pending' RETURNING ${attachmentColumns(holder)}`,
          [uuidToDb(file.id)]
        )
      ).rows[0];
      if (!row) throw new RowFeatureError('not_pending', `file ${ref.id} is no longer an upload under way`);
      return attachment(ref.table, holder, row);
    },

    async list(row) {
      const holder = holderOf(row.table);
      await mustSee(host, holder, row);
      const rows = (
        await host.query(`SELECT ${attachmentColumns(holder)} FROM ${holder.table} WHERE ${holder.row} = $1 AND "state" = 'ready' ORDER BY "uploaded_at", id`, [
          dbKey(holder.uuid, row.key),
        ])
      ).rows;
      return rows.map(r => attachment(row.table, holder, r));
    },

    async get(ref) {
      return (await read(host, ref)).file;
    },

    async download(ref, options) {
      const { holder, file } = await read(host, ref);
      if (file.state !== 'ready') throw new RowFeatureError('not_ready', `file ${ref.id} is still being uploaded`);
      const seconds = options.expiresInSeconds ?? DEFAULT_UPLOAD_SECONDS;
      const url = await options.bucket.signedUrl(file.objectKey, { method: 'GET', expiresInSeconds: seconds });
      // A file's first view asks its preview (D154): most files are never
      // viewed, so none is made at upload.
      let viewed = file;
      if (file.preview === 'none') {
        const row = (
          await host.query(`UPDATE ${holder.table} SET "preview" = 'pending' WHERE id = $1 AND "preview" = 'none' RETURNING ${attachmentColumns(holder)}`, [uuidToDb(file.id)])
        ).rows[0];
        if (row) viewed = attachment(ref.table, holder, row);
      }
      return { url, expiresAt: new Date(Date.now() + seconds * 1000), attachment: viewed };
    },

    async caption(ref) {
      if (ref.caption !== null && (typeof ref.caption !== 'string' || ref.caption.length > 2000)) {
        throw new RowFeatureError('invalid', 'a caption is a string of at most 2000 characters, or null');
      }
      const { holder, file } = await read(host, ref);
      const row = (await host.query(`UPDATE ${holder.table} SET "caption" = $2 WHERE id = $1 RETURNING ${attachmentColumns(holder)}`, [uuidToDb(file.id), ref.caption])).rows[0]!;
      return attachment(ref.table, holder, row);
    },

    async detach(ref) {
      const { holder, file } = await read(host, ref);
      await host.query(`DELETE FROM ${holder.table} WHERE id = $1`, [uuidToDb(file.id)]);
    },

    async file(input) {
      const to = holderOf(input.to.table);
      return host.transaction(async h => {
        const { holder: from, file } = await read(h, input, sql.forShare);
        if (file.state !== 'ready') throw new RowFeatureError('not_ready', `file ${input.id} is still being uploaded`);
        await mustSee(h, to, input.to);
        await admit(h, to, input.to, file.mediaType, file.size);
        await lock(h, `attachment-object:${file.objectKey}`);
        const filedFrom = `${input.table}:${file.key}:${file.id}`;
        const row = (
          await h.query(
            `INSERT INTO ${to.table} (${to.row}, "name", "media_type", "size", "content_hash", "object_key", "caption", "state", "uploaded_by", "filed_from", "extraction", "extracted_text", "preview", "preview_object")
             SELECT $1, "name", "media_type", "size", "content_hash", "object_key", "caption", 'ready', $2, $3, "extraction", "extracted_text", "preview", "preview_object" FROM ${from.table} WHERE id = $4
             RETURNING ${attachmentColumns(to)}`,
            [dbKey(to.uuid, input.to.key), user(h), filedFrom, uuidToDb(file.id)]
          )
        ).rows[0]!;
        if (input.move) await h.query(`DELETE FROM ${from.table} WHERE id = $1`, [uuidToDb(file.id)]);
        // The row the file was posted on points at the row it went to, so
        // that row lists it in its backlinks.
        const references = h.references?.holders[input.table];
        const target = h.references?.targets[input.to.table];
        if (references && target && (!references.tables || references.tables.includes(input.to.table))) {
          const toKey = (await h.query(`SELECT ${sql.text(target.key)} AS key FROM ${target.table} WHERE ${target.key} = $1`, [dbKey(target.uuid, input.to.key)])).rows[0]?.key;
          await h.query(
            `INSERT INTO ${references.table} (${references.row}, "target_table", "target_key", "role", "added_by") VALUES ($1, $2, $3, $4, $5)
             ON CONFLICT (${references.row}, "target_table", "target_key", "role") DO NOTHING`,
            [dbKey(references.uuid, file.key), input.to.table, toKey, input.role ?? 'filed', user(h)]
          );
        }
        return attachment(input.to.table, to, row);
      });
    },

    async release(message, options) {
      const objectKey = message.objectKey;
      if (typeof objectKey !== 'string' || objectKey === '') throw new RowFeatureError('invalid', 'a released message names its object');
      return host.transaction(async h => {
        await lock(h, `attachment-object:${objectKey}`);
        let held = 0;
        for (const holder of Object.values(definition.holders)) {
          // An upload never completed goes once its URL has expired.
          await h.query(`DELETE FROM ${holder.table} WHERE "object_key" = $1 AND "state" = 'pending' AND "expires_at" <= ${sql.now}`, [objectKey]);
          held += Number((await h.query(`SELECT ${sql.int('count(*)')} AS n FROM ${holder.table} WHERE "object_key" = $1`, [objectKey])).rows[0]?.n ?? 0);
        }
        if (held > 0) return { deleted: false };
        for (const name of [objectKey, previewObjectOf(objectKey)]) {
          try {
            await options.bucket.delete(name);
          } catch (error) {
            if (!(error instanceof ObjectNotFoundError)) throw error;
          }
        }
        return { deleted: true };
      });
    },

    async pending(kind, options = {}) {
      const limit = limitOf(options.limit);
      const column = kind === 'preview' ? '"preview"' : '"extraction"';
      const unions = Object.values(definition.holders).map(
        holder => `SELECT "object_key", "media_type", "content_hash", "uploaded_at" FROM ${holder.table} WHERE ${column} = 'pending' AND "state" = 'ready'`
      );
      const rows = (
        await host.query(
          `SELECT "object_key", min("media_type") AS media_type, min("content_hash") AS content_hash FROM (${unions.join(' UNION ALL ')}) AS files GROUP BY "object_key" ORDER BY min("uploaded_at") LIMIT ${limit}`,
          []
        )
      ).rows;
      return rows.map(r => ({ objectKey: String(r.object_key), mediaType: String(r.media_type), contentHash: String(r.content_hash) }));
    },

    async processed(input) {
      const sets: string[] = [];
      const values: unknown[] = [input.objectKey];
      if (input.extraction) {
        values.push(input.extraction.state, input.extraction.text ?? null);
        sets.push(`"extraction" = $${values.length - 1}`, `"extracted_text" = $${values.length}`);
      }
      if (input.preview) {
        values.push(input.preview.state, input.preview.object ?? null);
        sets.push(`"preview" = $${values.length - 1}`, `"preview_object" = $${values.length}`);
      }
      if (sets.length === 0) return 0;
      let n = 0;
      for (const holder of Object.values(definition.holders)) {
        n += (await host.query(`UPDATE ${holder.table} SET ${sets.join(', ')} WHERE "object_key" = $1`, values)).rowCount;
      }
      return n;
    },
  };
}
