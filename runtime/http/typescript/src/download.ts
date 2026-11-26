import type { OperationDownload } from './operation.js';

/*
A FileDownload result (D81, D141). The implementation returns the file: its
body and, when it names them, the filename, the media type, an entity tag
and when it last changed. The route answers the body with a Content-Type
the schema lists and a Content-Disposition, attachment or inline, with
filename, an ASCII fallback, and filename*, the name in UTF-8 (RFC 6266,
RFC 8187). A stream body is sent as the implementation writes it.

Every file answer says X-Content-Type-Options: nosniff. A sandboxed one
adds Content-Security-Policy: sandbox, with the tokens the schema lists,
and Cross-Origin-Resource-Policy: same-origin. Cache-Control is the
schema's, no-store without one.

With an entity tag or a last-modified time, a GET or a HEAD whose
If-None-Match names the tag, or, when it sends no If-None-Match, whose
If-Modified-Since is not before the time, is answered 304 with no body, and
a stream body is cancelled unread (RFC 9110, 13.1).

The Go server answers the same headers and statuses
(runtime/http/testdata/content_disposition_parity.json,
runtime/http/testdata/file_answer_parity.json).
*/

/** What an implementation returns for a FileDownload result. */
export interface FileDownload {
  /**
   * The file: text, sent as UTF-8; bytes; a Blob; or a stream, which the
   * route sends as the implementation writes it.
   */
  readonly body: string | Uint8Array | ArrayBuffer | Blob | ReadableStream<Uint8Array>;
  /** The filename Content-Disposition offers; the schema's filename when absent. */
  readonly filename?: string;
  /**
   * The body's media type, one the schema lists in contentType or
   * contentTypes (D141); the schema's contentType when absent. One outside
   * the list is a 500.
   */
  readonly contentType?: string;
  /**
   * The file's entity tag (D141): an opaque value, which the route quotes,
   * or a tag as the header carries it, `"v3"` or `W/"v3"`.
   */
  readonly etag?: string;
  /** When the file last changed (D141), sent as Last-Modified to the second. */
  readonly lastModified?: Date;
  /**
   * Headers beside the ones the route writes, such as Vary. A Cache-Control
   * here replaces the schema's.
   */
  readonly headers?: Readonly<Record<string, string>>;
}

/** The request a file answers, as far as its validators read it. */
export interface DownloadRequest {
  readonly method: string;
  readonly headers: Headers;
}

/**
 * The name a filename offers once its directories and control characters
 * are dropped: what follows the last / or \, without C0 controls or DEL.
 */
export function downloadFilename(name: string): string {
  const base = name.slice(Math.max(name.lastIndexOf('/'), name.lastIndexOf('\\')) + 1);
  const cleaned = base.replace(/[\u0000-\u001f\u007f]/gu, '');
  return cleaned === '.' || cleaned === '..' ? '' : cleaned;
}

/** RFC 8187's attr-char: what filename* carries without percent-encoding. */
const ATTR_CHAR = /^[A-Za-z0-9!#$&+\-.^_`|~]$/u;

/**
 * The Content-Disposition of a file named name: `attachment`, or with a
 * name, `attachment; filename="<ASCII>"; filename*=UTF-8''<encoded>`, where
 * the ASCII fallback has each character outside printable ASCII, and each
 * of `"`, `\` and `%`, replaced by `_`. disposition `inline` writes that
 * word in attachment's place (D141).
 */
export function contentDisposition(name: string | undefined, disposition: 'attachment' | 'inline' = 'attachment'): string {
  const filename = downloadFilename(name ?? '');
  if (filename === '') return disposition;
  const fallback = [...filename].map(char => (/^[ -~]$/u.test(char) && !'"\\%'.includes(char) ? char : '_')).join('');
  let encoded = '';
  for (const char of filename) {
    if (ATTR_CHAR.test(char)) {
      encoded += char;
      continue;
    }
    for (const byte of new TextEncoder().encode(char)) encoded += '%' + byte.toString(16).toUpperCase().padStart(2, '0');
  }
  return `${disposition}; filename="${fallback}"; filename*=UTF-8''${encoded}`;
}

/** An entity tag as the header carries it, weak or strong. */
const ENTITY_TAG = /^(?:W\/)?"[!#-~]*"$/u;
/** An opaque value the route may quote into a tag. */
const OPAQUE_TAG = /^[!#-~]+$/u;

/**
 * The ETag header of a tag an implementation names (D141): the tag itself
 * when it is written as the header carries one, `"v3"` or `W/"v3"`, and
 * the value in quotes otherwise. A value that holds a quote, a space or a
 * character outside printable ASCII is refused.
 */
export function entityTag(tag: string): string {
  if (ENTITY_TAG.test(tag)) return tag;
  if (OPAQUE_TAG.test(tag)) return `"${tag}"`;
  throw new TypeError(`a FileDownload etag is printable ASCII with no space and no quote, or a quoted tag, not ${JSON.stringify(tag)}`);
}

/**
 * Whether If-None-Match names etag, by the weak comparison (RFC 9110,
 * 8.8.3.2): `*` names every tag, and a list names the tags it holds, a
 * W/ prefix on either side ignored. A list that does not parse names
 * nothing past the point it stops parsing.
 */
export function ifNoneMatchNames(header: string, etag: string): boolean {
  const opaque = etag.startsWith('W/') ? etag.slice(2) : etag;
  let rest = header.trim();
  if (rest === '*') return true;
  while (rest !== '') {
    if (rest.startsWith('W/')) rest = rest.slice(2);
    if (!rest.startsWith('"')) return false;
    const end = rest.indexOf('"', 1);
    if (end < 0) return false;
    if (rest.slice(0, end + 1) === opaque) return true;
    rest = rest.slice(end + 1).replace(/^[ \t]*/u, '');
    if (rest === '') return false;
    if (!rest.startsWith(',')) return false;
    rest = rest.slice(1).replace(/^[ \t,]*/u, '');
  }
  return false;
}

const MONTHS = ['jan', 'feb', 'mar', 'apr', 'may', 'jun', 'jul', 'aug', 'sep', 'oct', 'nov', 'dec'];
const DAY = '(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)';
const LONG_DAY = '(?:Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday)';
const MONTH = '(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)';
const CLOCK = '(\\d{2}):(\\d{2}):(\\d{2})';
/** Sun, 06 Nov 1994 08:49:37 GMT */
const IMF_FIXDATE = new RegExp(`^${DAY}, (\\d{2}) ${MONTH} (\\d{4}) ${CLOCK} GMT$`, 'u');
/** Sunday, 06-Nov-94 08:49:37 GMT */
const RFC_850 = new RegExp(`^${LONG_DAY}, (\\d{2})-${MONTH}-(\\d{2}) ${CLOCK} GMT$`, 'u');
/** Sun Nov  6 08:49:37 1994 */
const ASCTIME = new RegExp(`^${DAY} ${MONTH} ([ \\d]\\d) ${CLOCK} (\\d{4})$`, 'u');

function utcSeconds(year: number, month: string, day: number, hour: number, minute: number, second: number): number | undefined {
  const monthIndex = MONTHS.indexOf(month.toLowerCase());
  if (hour > 23 || minute > 59 || second > 59 || day < 1) return undefined;
  // Date.UTC reads a year under 100 as 19xx, so the year is set apart.
  const date = new Date(0);
  date.setUTCFullYear(year, monthIndex, day);
  date.setUTCHours(hour, minute, second, 0);
  // A day the month does not hold rolls into the next one.
  if (date.getUTCMonth() !== monthIndex || date.getUTCDate() !== day) return undefined;
  return Math.floor(date.getTime() / 1000);
}

/**
 * The time an HTTP date names, in seconds since the epoch: the fixed
 * format, `Sun, 06 Nov 1994 08:49:37 GMT`, and the two obsolete ones a
 * recipient still reads, `Sunday, 06-Nov-94 08:49:37 GMT`, whose year is
 * 1969 to 2068, and `Sun Nov  6 08:49:37 1994` (RFC 9110, 5.6.7). The day
 * of the week is not checked against the date. undefined for anything
 * else.
 */
export function parseHttpDate(value: string): number | undefined {
  let match = IMF_FIXDATE.exec(value);
  if (match) return utcSeconds(Number(match[3]), match[2]!, Number(match[1]), Number(match[4]), Number(match[5]), Number(match[6]));
  match = RFC_850.exec(value);
  if (match) {
    const short = Number(match[3]);
    return utcSeconds(short >= 69 ? 1900 + short : 2000 + short, match[2]!, Number(match[1]), Number(match[4]), Number(match[5]), Number(match[6]));
  }
  match = ASCTIME.exec(value);
  if (match) return utcSeconds(Number(match[6]), match[1]!, Number(match[2]!.trim()), Number(match[3]), Number(match[4]), Number(match[5]));
  return undefined;
}

/**
 * Whether a request's validators say its copy of the file is current
 * (D141): its If-None-Match names etag, or, when it sends no
 * If-None-Match, its If-Modified-Since is at or after lastModified, in
 * seconds since the epoch. Only a GET or a HEAD is conditional.
 */
export function notModified(request: DownloadRequest, etag: string | undefined, lastModified: number | undefined): boolean {
  if (request.method !== 'GET' && request.method !== 'HEAD') return false;
  const ifNoneMatch = request.headers.get('if-none-match');
  if (ifNoneMatch !== null) return etag !== undefined && ifNoneMatchNames(ifNoneMatch, etag);
  const ifModifiedSince = request.headers.get('if-modified-since');
  if (ifModifiedSince === null || lastModified === undefined) return false;
  const since = parseHttpDate(ifModifiedSince);
  return since !== undefined && lastModified <= since;
}

/** The Content-Security-Policy of a sandboxed file answer; undefined for none. */
export function sandboxPolicy(sandbox: OperationDownload['sandbox']): string | undefined {
  if (sandbox === undefined || sandbox === false) return undefined;
  return sandbox === true ? 'sandbox' : ['sandbox', ...sandbox].join(' ');
}

/**
 * The response of a FileDownload result: the file, its media type, its
 * name and its validators, or a 304 when request's validators match them.
 * Without a request no answer is conditional.
 */
export function downloadResponse(result: unknown, download: OperationDownload, requestId: string, request?: DownloadRequest): Response {
  if (typeof result !== 'object' || result === null || !('body' in result)) {
    throw new TypeError(`a FileDownload result is { body, filename? }, not ${result === null ? 'null' : typeof result}`);
  }
  const file = result as FileDownload;
  try {
    return fileResponse(file, download, requestId, request);
  } catch (error) {
    cancelBody(file.body);
    throw error;
  }
}

function cancelBody(body: FileDownload['body']): void {
  if (body instanceof ReadableStream) void body.cancel().catch(() => undefined);
}

function fileResponse(file: FileDownload, download: OperationDownload, requestId: string, request: DownloadRequest | undefined): Response {
  let contentType = download.contentType;
  if (file.contentType !== undefined && file.contentType !== '') {
    if (file.contentType !== download.contentType && !(download.contentTypes ?? []).includes(file.contentType)) {
      throw new TypeError(`a FileDownload result named the media type ${JSON.stringify(file.contentType)}, which its schema does not list`);
    }
    contentType = file.contentType;
  }
  const etag = file.etag === undefined || file.etag === '' ? undefined : entityTag(file.etag);
  let lastModified: number | undefined;
  if (file.lastModified !== undefined) {
    const time = file.lastModified instanceof Date ? file.lastModified.getTime() : Number.NaN;
    if (Number.isNaN(time)) throw new TypeError('a FileDownload lastModified is a Date that holds a time');
    lastModified = Math.floor(time / 1000);
  }
  const headers = new Headers();
  headers.set('cache-control', download.cacheControl ?? 'no-store');
  for (const [name, value] of Object.entries(file.headers ?? {})) headers.set(name, value);
  headers.set('x-content-type-options', 'nosniff');
  const policy = sandboxPolicy(download.sandbox);
  if (policy !== undefined) {
    headers.set('content-security-policy', policy);
    headers.set('cross-origin-resource-policy', 'same-origin');
  }
  if (etag !== undefined) headers.set('etag', etag);
  if (lastModified !== undefined) headers.set('last-modified', new Date(lastModified * 1000).toUTCString());
  headers.set('x-request-id', requestId);
  if (request && notModified(request, etag, lastModified)) {
    cancelBody(file.body);
    headers.delete('content-type');
    headers.delete('content-length');
    headers.delete('content-disposition');
    return new Response(null, { status: 304, headers });
  }
  headers.set('content-type', contentType);
  headers.set('content-disposition', contentDisposition(file.filename ?? download.filename, download.disposition ?? 'attachment'));
  return new Response(file.body as BodyInit, { status: 200, headers });
}
