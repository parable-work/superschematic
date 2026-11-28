import type { Principal } from './auth.js';
import { GRANT_TOKEN_PREFIX, grantEnded, grantTokenHash, issueGrant, revokeGrant, type GrantRow } from './access.js';
import { requestIdOf } from './envelope.js';
import { badRequest, notFound, problemResponse, unauthorized, type HttpProblem } from './problem.js';
import { sha256 } from './sha256.js';
import { ormDialect, type PlainRepository } from './orm.js';
import { quoteIdentifier as quote } from './sql-text.js';
import { uuidFromDb } from './uuid.js';

/*
The calendar (D152): the dates of many tables' rows, merged into one read
and one iCalendar feed.

A DB table's @calendarSource declarations become its generated
repository's definition's calendar entries: a source dates each row by a
date or date-time field (to an end field when it spans), or each occurrence
of the table's Recurrence by its due time, and titles each entry by a field
of the row. readCalendar reads every source in a window, each row through
its table's repository as the caller (orm.as(principal)), so a row's
audience and the caller's grants hold as they hold on any read (D156); an
occurrence is shown only when its row is. A source a caller lacks the
permission of (CalendarOptions.permissions) is left out.

The feed is for a calendar client, which sends no Authorization header: its
URL holds a token. The token is a grant (D156) of the database's
@accessGrants table: its holder the user it reads as (holderKind user), its
role a row of the UserRole table whose permissions bound what the feed may
read, its target the feed itself (targetType `CalendarFeed;detail=...;
sources=...`, targetId the holder), its end a year on unless set. grantAuthenticator
refuses such a grant on every other route, so a leaked feed URL reads the
feed and nothing else; a revoked or expired one is refused at the next
fetch, and rotating one revokes it and issues another with the same scope.
Its detail is full (each entry's title) or busy ("Busy" and a reference
number, a short hash of the entry's source and key, which its owner looks
up), so a leaked URL shows only what its owner chose.

Nothing here imports pg or Hono: the ORM is the generated createOrm's, taken
as a value; calendarRoutes answers a fetch Request with a Response.
*/

/** The occurrences sub-table an occurrences source reads (the generated definition's). */
export interface CalendarOccurrencesDefinition {
  readonly table: string;
  readonly row: string;
  readonly key: string;
  readonly due: string;
  readonly dueAt: string;
  readonly state: string;
}

/** One @calendarSource of a table, as its generated repository's definition carries it (D152). */
export interface CalendarSourceDefinition {
  readonly name: string;
  readonly kind: string;
  readonly title: string;
  readonly color?: string;
  readonly field?: string;
  readonly end?: string;
  readonly owner?: string;
  readonly occurrences?: CalendarOccurrencesDefinition;
}

/** One entry of the calendar. */
export interface CalendarEntry {
  /** The source's name. */
  readonly source: string;
  readonly kind: string;
  readonly color?: string;
  /** The row's table type and key. */
  readonly type: string;
  readonly key: string;
  readonly title: string;
  /** When it starts: an ISO 8601 instant, or a date for an all-day entry. */
  readonly start: string;
  readonly end?: string;
  readonly allDay?: true;
  /** An occurrence's key, its wall due and its state. */
  readonly occurrence?: { readonly key: string; readonly due: string; readonly state: string };
}

/** What readCalendar reads: a window of instants or dates, and which sources. */
export interface CalendarRead {
  readonly from: Date | string | number;
  readonly to: Date | string | number;
  /** Only these sources, by name; every one when absent. */
  readonly sources?: readonly string[];
  /** all: every row the caller reads; mine: only rows whose source's owner field holds the caller. */
  readonly scope?: 'all' | 'mine';
  /** At most this many entries per source; 500 unless set. */
  readonly limit?: number;
}

/** What the calendar takes beside the ORM. */
export interface CalendarOptions {
  /** The permission each source needs, by its name: a caller without it reads none of its entries. A source not named needs none beyond its table's audience and grants. */
  readonly permissions?: Readonly<Record<string, string>>;
}

type Values = Record<string, unknown>;

type RepositoryShape = Pick<PlainRepository, 'def' | 'findMany' | 'findOne'>;

interface OrmShape {
  as(principal: Principal | null | undefined): OrmShape;
  asSystem(): OrmShape;
  query(text: string, values?: readonly unknown[]): Promise<{ rows: Values[] }>;
}

function repositoriesOf(orm: object): RepositoryShape[] {
  return Object.values(orm).filter((value): value is RepositoryShape => {
    const repository = value as RepositoryShape | undefined;
    return typeof repository === 'object' && repository !== null && typeof repository.def === 'object' && typeof repository.findMany === 'function';
  });
}

/** Every calendar source of orm's tables, with its repository. */
export function calendarSourcesOf(orm: object): { readonly source: CalendarSourceDefinition; readonly type: string }[] {
  return repositoriesOf(orm).flatMap(repository => (repository.def?.calendar ?? []).map(source => ({ source, type: repository.def!.name ?? '' })));
}

function instant(value: Date | string | number, what: string): Date {
  const at = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(at.getTime())) throw new TypeError(`readCalendar: ${what} is no instant: ${String(value)}`);
  return at;
}

function iso(value: unknown): string | undefined {
  if (value === null || value === undefined) return undefined;
  if (value instanceof Date) return value.toISOString();
  return String(value);
}

/**
 * Reads the calendar as principal (D152): every source's entries whose
 * start lies in [from, to), each source's rows through its table's
 * repository as principal, so a row's audience and the principal's grants
 * hold; an occurrence only when its row is read. Entries come in start
 * order, ties by source and key; more says a source held more than limit.
 */
export async function readCalendar(orm: object, principal: Principal | null, read: CalendarRead, options: CalendarOptions = {}): Promise<{ entries: CalendarEntry[]; more: boolean }> {
  const from = instant(read.from, 'from');
  const to = instant(read.to, 'to');
  const limit = Math.min(Math.max(read.limit ?? 500, 1), 5000);
  const db = (orm as unknown as OrmShape).as(principal);
  const entries: CalendarEntry[] = [];
  let more = false;
  for (const repository of repositoriesOf(db)) {
    const def = repository.def!;
    for (const source of def.calendar ?? []) {
      if (read.sources && !read.sources.includes(source.name)) continue;
      const permission = options.permissions?.[source.name];
      if (permission !== undefined && !(principal?.permissions ?? []).includes(permission)) continue;
      if (read.scope === 'mine' && (source.owner === undefined || principal === null)) continue;
      const owned: Values = read.scope === 'mine' && source.owner !== undefined && principal !== null ? { [source.owner]: { eq: principal.subject } } : {};
      const read_ = source.occurrences
        ? await occurrencesOf(db, repository, source, from, to, limit, owned)
        : await rowsOf(repository, source, from, to, limit, owned);
      entries.push(...read_.entries);
      more = more || read_.more;
    }
  }
  entries.sort((a, b) => (a.start < b.start ? -1 : a.start > b.start ? 1 : a.source < b.source ? -1 : a.source > b.source ? 1 : a.key < b.key ? -1 : a.key > b.key ? 1 : 0));
  return { entries, more };
}

// rowsOf reads a field source's rows in the window through its repository.
async function rowsOf(repository: RepositoryShape, source: CalendarSourceDefinition, from: Date, to: Date, limit: number, owned: Values): Promise<{ entries: CalendarEntry[]; more: boolean }> {
  const def = repository.def!;
  const field = source.field!;
  const column = def.columns?.find(c => c.name === field);
  const timestamp = column?.timestamp === true;
  const bound = (at: Date) => (timestamp ? at : at.toISOString().slice(0, 10));
  const { rows } = await repository.findMany({ ...owned, [field]: { gte: bound(from), lt: bound(to) } }, { orderBy: [{ field }], limit: limit + 1, count: false });
  const entries = rows.slice(0, limit).flatMap(row => {
    const start = iso(row[field]);
    if (start === undefined) return [];
    const end = source.end ? iso(row[source.end]) : undefined;
    const entry: CalendarEntry = {
      source: source.name,
      kind: source.kind,
      ...(source.color ? { color: source.color } : {}),
      type: def.name ?? '',
      key: String(row[def.key ?? 'id']),
      title: String(row[source.title] ?? ''),
      start: timestamp ? start : start.slice(0, 10),
      ...(end !== undefined ? { end: timestamp ? end : end.slice(0, 10) } : {}),
      ...(timestamp ? {} : { allDay: true as const }),
    };
    return [entry];
  });
  return { entries, more: rows.length > limit };
}

/** A time a statement read: a Date (pg's), its RFC 3339 text (SQLite's) or epoch milliseconds. */
function timeOf(value: unknown): Date {
  if (value instanceof Date) return value;
  return typeof value === 'string' && !/^\d+$/u.test(value) ? new Date(value) : new Date(Number(value));
}

// occurrencesOf reads an occurrences source's occurrences in the window,
// then the rows they belong to through the repository as the caller, and
// keeps the occurrences of the rows it read.
async function occurrencesOf(db: OrmShape, repository: RepositoryShape, source: CalendarSourceDefinition, from: Date, to: Date, limit: number, owned: Values): Promise<{ entries: CalendarEntry[]; more: boolean }> {
  const def = repository.def!;
  const occ = source.occurrences!;
  // On SQLite a time is its RFC 3339 text, which compares in order (D182).
  const [p1, p2] = ormDialect(db) === 'sqlite' ? ['?1', '?2'] : ['$1', '$2'];
  const { rows } = await db.asSystem().query(
    `SELECT ${quote(occ.row)} AS row, ${quote(occ.key)} AS key, ${quote(occ.due)} AS due, ${quote(occ.dueAt)} AS due_at, ${quote(occ.state)} AS state FROM ${quote(occ.table)} WHERE ${quote(occ.dueAt)} >= ${p1} AND ${quote(occ.dueAt)} < ${p2} ORDER BY ${quote(occ.dueAt)}, ${quote(occ.key)} LIMIT ${limit + 1}`,
    [from, to]
  );
  const keyColumn = def.columns?.find(c => c.name === (def.key ?? 'id'));
  const rowKey = (value: unknown) => (keyColumn?.uuid ? uuidFromDb(String(value)) : String(value));
  const keys = [...new Set(rows.slice(0, limit).map(row => rowKey(row.row)))];
  const visible = new Map<string, Values>();
  if (keys.length > 0) {
    const { rows: parents } = await repository.findMany({ ...owned, [def.key ?? 'id']: { in: keys } }, { limit: keys.length, count: false });
    for (const parent of parents) visible.set(String(parent[def.key ?? 'id']), parent);
  }
  const entries = rows.slice(0, limit).flatMap(row => {
    const parent = visible.get(rowKey(row.row));
    if (!parent) return [];
    const entry: CalendarEntry = {
      source: source.name,
      kind: source.kind,
      ...(source.color ? { color: source.color } : {}),
      type: def.name ?? '',
      key: String(parent[def.key ?? 'id']),
      title: String(parent[source.title] ?? ''),
      start: iso(timeOf(row.due_at))!,
      occurrence: { key: String(row.key), due: String(row.due ?? ''), state: String(row.state) },
    };
    return [entry];
  });
  return { entries, more: rows.length > limit };
}

// ---- the iCalendar feed ----

/** What a feed shows of each entry: its title, or "Busy" and a reference number. */
export type CalendarDetail = 'full' | 'busy';

/** What a calendar feed's grant's targetType begins with; its scope follows, `;detail=busy;sources=a,b`. */
export const CALENDAR_FEED = 'CalendarFeed';

/** Whether a grant's target type is a calendar feed's. */
export function isCalendarFeed(targetType: string): boolean {
  return targetType === CALENDAR_FEED || targetType.startsWith(`${CALENDAR_FEED};`);
}

/** How long a feed's token lasts unless its issue says: a year. */
export const CALENDAR_FEED_DAYS = 365;

/** What a feed reads, as its grant's targetId holds it. */
export interface CalendarFeedScope {
  readonly detail: CalendarDetail;
  /** Only these sources; every one when absent. */
  readonly sources?: readonly string[];
}

function scopeText(scope: CalendarFeedScope): string {
  return `${CALENDAR_FEED};detail=${scope.detail}${scope.sources && scope.sources.length > 0 ? `;sources=${scope.sources.join(',')}` : ''}`;
}

function scopeOf(targetType: string): CalendarFeedScope {
  const parts = new Map(targetType.split(';').slice(1).map(part => part.split('=', 2) as [string, string]));
  const detail = parts.get('detail') === 'full' ? 'full' : 'busy';
  const sources = parts.get('sources')?.split(',').filter(Boolean);
  return sources && sources.length > 0 ? { detail, sources } : { detail };
}

/** What issueCalendarFeed takes. */
export interface IssueCalendarFeedInput extends CalendarFeedScope {
  /** The user the feed reads as: its principal's subject. */
  readonly holder: string;
  /** The UserRole row whose permissions bound what the feed reads. */
  readonly role: string;
  /** When it expires; CALENDAR_FEED_DAYS on unless set. */
  readonly endsAt?: Date;
}

/**
 * Issues a calendar feed (D152): a grant of orm's @accessGrants table that
 * reads the feed alone, as holder, and the token its URL holds, which only
 * the holder is given.
 */
export async function issueCalendarFeed(orm: object, input: IssueCalendarFeedInput): Promise<{ readonly id: string; readonly token: string; readonly endsAt: Date }> {
  const endsAt = input.endsAt ?? new Date(Date.now() + CALENDAR_FEED_DAYS * 86_400_000);
  const { grant, token } = await issueGrant(orm, {
    holder: input.holder,
    holderKind: 'user',
    role: input.role,
    targetType: scopeText(input),
    targetId: input.holder,
    endsAt,
  });
  return { id: String(grant.id), token, endsAt };
}

/** Reads a feed's grant by its id, as the system; null when it is none or no feed's. */
async function feedGrant(orm: object, id: string): Promise<GrantRow | null> {
  const grants = repositoriesOf((orm as OrmShape).asSystem()).find(repository => (repository.def as { access?: string } | undefined)?.access === 'grants');
  if (!grants) throw new Error('calendar: the ORM has no @accessGrants table');
  const grant = (await grants.findOne({ id: { eq: id } })) as unknown as GrantRow | null;
  return grant !== null && isCalendarFeed(grant.targetType) ? grant : null;
}

/** Revokes a feed: its URL is refused at the next fetch. Only its holder's own feed; false for none. */
export async function revokeCalendarFeed(orm: object, holder: string, id: string): Promise<boolean> {
  const grant = await feedGrant(orm, id);
  if (grant === null || grant.holder !== holder || grant.revokedAt) return false;
  await revokeGrant(orm, id);
  return true;
}

/** Rotates a feed: revokes it and issues another of the same scope, role and expiry. Only its holder's own; null for none. */
export async function rotateCalendarFeed(orm: object, holder: string, id: string): Promise<{ readonly id: string; readonly token: string; readonly endsAt: Date } | null> {
  const grant = await feedGrant(orm, id);
  if (grant === null || grant.holder !== holder || grant.revokedAt) return null;
  await revokeGrant(orm, id);
  const ends = grant.endsAt ? new Date(grant.endsAt) : undefined;
  return issueCalendarFeed(orm, { holder, role: grant.role, ...scopeOf(grant.targetType), ...(ends ? { endsAt: ends } : {}) });
}

/**
 * The principal and scope a feed's token reads as (D152), or the reason it
 * is refused: no such feed, or one that was revoked or has expired. The
 * principal is the holder, a user, with the permissions of the grant's
 * role, and no grants of its own, so the holder's audiences hold.
 */
export async function calendarFeedOf(orm: object, token: string, now = new Date()): Promise<{ readonly principal: Principal; readonly scope: CalendarFeedScope } | { readonly refused: string }> {
  if (!token.startsWith(GRANT_TOKEN_PREFIX)) return { refused: 'no such feed' };
  const db = (orm as OrmShape).asSystem();
  const repositories = repositoriesOf(db);
  const grants = repositories.find(repository => (repository.def as { access?: string } | undefined)?.access === 'grants');
  const roles = repositories.find(repository => (repository.def as { access?: string } | undefined)?.access === 'roles');
  if (!grants || !roles) throw new Error('calendar: the ORM has no @accessGrants table or no UserRole table');
  const grant = (await grants.findOne({ tokenHash: { eq: grantTokenHash(token) } })) as unknown as GrantRow | null;
  if (grant === null || !isCalendarFeed(grant.targetType)) return { refused: 'no such feed' };
  const ended = await grantEnded(db, grant, now);
  if (ended !== undefined) return { refused: `the feed has ${ended === 'not started' ? 'not started' : 'ended'}` };
  const role = await roles.findOne({ name: { eq: grant.role } });
  const permissions = (role?.permissions as readonly string[] | null | undefined) ?? [];
  return {
    principal: { subject: grant.holder, kind: 'user', permissions: [...permissions], credential: { kind: 'grant', id: grant.id } },
    scope: scopeOf(grant.targetType),
  };
}

/** A short reference to an entry, for a busy feed: eight characters of base32 of the SHA-256 of its source, key and occurrence. */
export function calendarReference(entry: CalendarEntry): string {
  const digest = sha256(new TextEncoder().encode(`${entry.source}\u0000${entry.key}\u0000${entry.occurrence?.key ?? ''}`));
  const alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789';
  let out = '';
  for (let i = 0; i < 8; i++) out += alphabet[digest[i]! % 32];
  return out;
}

function icsText(value: string): string {
  return value.replaceAll('\\', '\\\\').replaceAll(';', '\\;').replaceAll(',', '\\,').replaceAll(/\r?\n/gu, '\\n');
}

// fold folds a content line at 75 octets, as RFC 5545 (3.1) has it.
function fold(line: string): string {
  const bytes = new TextEncoder().encode(line);
  if (bytes.length <= 75) return line;
  const out: string[] = [];
  let current = '';
  let size = 0;
  for (const char of line) {
    const width = new TextEncoder().encode(char).length;
    if (size + width > (out.length === 0 ? 75 : 74)) {
      out.push(current);
      current = '';
      size = 0;
    }
    current += char;
    size += width;
  }
  out.push(current);
  return out.join('\r\n ');
}

function icsInstant(value: string): string {
  return new Date(value).toISOString().replaceAll('-', '').replaceAll(':', '').replace(/\.\d{3}Z$/u, 'Z');
}

/**
 * The entries as an iCalendar document (RFC 5545): one VEVENT each, an
 * all-day entry a date, its summary its title, or "Busy" and its reference
 * for a busy feed, which carries nothing else of the row.
 */
export function calendarIcs(entries: readonly CalendarEntry[], detail: CalendarDetail, now = new Date(), name = 'Calendar'): string {
  const lines = ['BEGIN:VCALENDAR', 'VERSION:2.0', 'PRODID:-//superschematic//calendar//EN', 'CALSCALE:GREGORIAN', `X-WR-CALNAME:${icsText(name)}`];
  const stamp = icsInstant(now.toISOString());
  for (const entry of entries) {
    const reference = calendarReference(entry);
    lines.push('BEGIN:VEVENT');
    lines.push(`UID:${reference}-${icsText(entry.occurrence?.key ?? entry.start)}@superschematic`);
    lines.push(`DTSTAMP:${stamp}`);
    if (entry.allDay) {
      lines.push(`DTSTART;VALUE=DATE:${entry.start.replaceAll('-', '')}`);
      if (entry.end) lines.push(`DTEND;VALUE=DATE:${entry.end.replaceAll('-', '')}`);
    } else {
      lines.push(`DTSTART:${icsInstant(entry.start)}`);
      if (entry.end) lines.push(`DTEND:${icsInstant(entry.end)}`);
    }
    if (detail === 'full') {
      lines.push(`SUMMARY:${icsText(entry.title)}`);
      lines.push(`CATEGORIES:${icsText(entry.kind)}`);
      if (entry.occurrence) lines.push(`STATUS:${entry.occurrence.state === 'skipped' ? 'CANCELLED' : 'CONFIRMED'}`);
    } else {
      lines.push(`SUMMARY:Busy (${reference})`);
      lines.push('TRANSP:OPAQUE');
    }
    lines.push('END:VEVENT');
  }
  lines.push('END:VCALENDAR');
  return lines.map(fold).join('\r\n') + '\r\n';
}

// ---- routes and the MCP tool ----

/** What calendarRoutes takes. */
export interface CalendarRoutesOptions extends CalendarOptions {
  readonly orm: object;
  /** Establishes the caller of a read and of a feed's issue, rotation and revocation, from the request. */
  readonly authenticate: (request: Request) => Promise<Principal | null>;
  /** The path the routes are under; /api/calendar unless set. */
  readonly base?: string;
  /** The window a feed serves, in days before and after now; 31 and 400 unless set. */
  readonly feedDaysBefore?: number;
  readonly feedDaysAfter?: number;
  /** The clock; now unless set. */
  readonly now?: () => Date;
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

/** The refusal of a request, through the shared problem envelope, which carries its requestId and code. */
function problem(request: Request, refusal: HttpProblem): Response {
  return problemResponse(refusal, requestIdOf(request));
}

/**
 * The calendar's routes (D152), as a fetch handler the app mounts under
 * base (GET answers null for a request it does not serve):
 *
 * - GET {base}?from&to&sources&scope: the caller's calendar, as JSON.
 * - GET {base}/feed/{token}.ics: the feed a token reads, as text/calendar;
 *   401 for a revoked, expired or unknown token.
 * - POST {base}/feeds { role, detail, sources?, days? }: issues the caller a
 *   feed; the answer holds its URL's token once.
 * - POST {base}/feeds/{id}/rotate, DELETE {base}/feeds/{id}: the caller's own feed.
 */
export function calendarRoutes(options: CalendarRoutesOptions): (request: Request) => Promise<Response | null> {
  const base = (options.base ?? '/api/calendar').replace(/\/$/u, '');
  const now = options.now ?? (() => new Date());
  return async (request: Request) => {
    const url = new URL(request.url);
    if (url.pathname !== base && !url.pathname.startsWith(`${base}/`)) return null;
    const rest = url.pathname.slice(base.length);
    const feed = /^\/feed\/([^/]+?)(?:\.ics)?$/u.exec(rest);
    if (request.method === 'GET' && feed) {
      const resolved = await calendarFeedOf(options.orm, decodeURIComponent(feed[1]!), now());
      if ('refused' in resolved) return problem(request, unauthorized(resolved.refused));
      const at = now();
      const { entries } = await readCalendar(
        options.orm,
        resolved.principal,
        {
          from: new Date(at.getTime() - (options.feedDaysBefore ?? 31) * 86_400_000),
          to: new Date(at.getTime() + (options.feedDaysAfter ?? 400) * 86_400_000),
          ...(resolved.scope.sources ? { sources: resolved.scope.sources } : {}),
        },
        options
      );
      return new Response(calendarIcs(entries, resolved.scope.detail, at), { status: 200, headers: { 'content-type': 'text/calendar; charset=utf-8', 'cache-control': 'private, max-age=300' } });
    }
    const principal = await options.authenticate(request);
    if (!principal) return problem(request, unauthorized());
    if (request.method === 'GET' && rest === '') {
      const from = url.searchParams.get('from');
      const to = url.searchParams.get('to');
      if (!from || !to) return problem(request, badRequest('from and to are required'));
      const sources = url.searchParams.getAll('sources').flatMap(s => s.split(',')).filter(Boolean);
      const scope = url.searchParams.get('scope') === 'mine' ? 'mine' : 'all';
      try {
        const read = await readCalendar(options.orm, principal, { from, to, scope, ...(sources.length > 0 ? { sources } : {}) }, options);
        return json(200, { data: read });
      } catch (error) {
        if (error instanceof TypeError) return problem(request, badRequest(error.message));
        throw error;
      }
    }
    if (request.method === 'POST' && rest === '/feeds') {
      const body = (await request.json().catch(() => ({}))) as { role?: unknown; detail?: unknown; sources?: unknown; days?: unknown };
      if (typeof body.role !== 'string' || (body.detail !== 'full' && body.detail !== 'busy')) return problem(request, badRequest('a feed takes role, a UserRole, and detail, full or busy'));
      const days = typeof body.days === 'number' && body.days > 0 && body.days <= 3660 ? body.days : CALENDAR_FEED_DAYS;
      const sources = Array.isArray(body.sources) ? body.sources.filter((s): s is string => typeof s === 'string') : undefined;
      const issued = await issueCalendarFeed((options.orm as OrmShape).as(principal), {
        holder: principal.subject,
        role: body.role,
        detail: body.detail,
        ...(sources && sources.length > 0 ? { sources } : {}),
        endsAt: new Date(now().getTime() + days * 86_400_000),
      });
      return json(201, { data: { id: issued.id, token: issued.token, path: `${base}/feed/${issued.token}.ics`, endsAt: issued.endsAt.toISOString() } });
    }
    const one = /^\/feeds\/([^/]+?)(\/rotate)?$/u.exec(rest);
    if (one && request.method === 'POST' && one[2]) {
      const rotated = await rotateCalendarFeed((options.orm as OrmShape).as(principal), principal.subject, decodeURIComponent(one[1]!));
      return rotated ? json(200, { data: { id: rotated.id, token: rotated.token, path: `${base}/feed/${rotated.token}.ics`, endsAt: rotated.endsAt.toISOString() } }) : problem(request, notFound('no such feed of yours'));
    }
    if (one && request.method === 'DELETE' && !one[2]) {
      return (await revokeCalendarFeed(options.orm, principal.subject, decodeURIComponent(one[1]!))) ? new Response(null, { status: 204 }) : problem(request, notFound('no such feed of yours'));
    }
    return problem(request, notFound('no such calendar route'));
  };
}

/** The calendar as an MCP tool (D152): what tools/list answers of it. */
export const calendarMcpTool = {
  name: 'calendar',
  title: 'Calendar',
  description: 'Reads the dates of every calendar source, rows and recurrence occurrences, in a window, as the caller reads them.',
  inputSchema: {
    type: 'object',
    additionalProperties: false,
    required: ['from', 'to'],
    properties: {
      from: { type: 'string', description: 'The earliest start, an ISO 8601 date or instant.' },
      to: { type: 'string', description: 'The start every entry falls before.' },
      sources: { type: 'array', items: { type: 'string' }, description: 'Only these sources, by name.' },
      scope: { enum: ['all', 'mine'], description: 'mine: only rows the caller owns.' },
    },
  },
  annotations: { readOnlyHint: true, idempotentHint: true },
} as const;

/** Runs a call of calendarMcpTool as principal: its result, or its refusal. */
export async function callCalendarTool(orm: object, principal: Principal | null, args: Readonly<Record<string, unknown>>, options: CalendarOptions = {}): Promise<{ readonly result: unknown } | { readonly problem: { readonly status: number; readonly title: string; readonly detail: string } }> {
  if (typeof args.from !== 'string' || typeof args.to !== 'string') return { problem: { status: 400, title: 'Bad Request', detail: 'from and to are required' } };
  const sources = Array.isArray(args.sources) ? args.sources.filter((s): s is string => typeof s === 'string') : undefined;
  try {
    return { result: await readCalendar(orm, principal, { from: args.from, to: args.to, scope: args.scope === 'mine' ? 'mine' : 'all', ...(sources ? { sources } : {}) }, options) };
  } catch (error) {
    if (error instanceof TypeError) return { problem: { status: 400, title: 'Bad Request', detail: error.message } };
    throw error;
  }
}

