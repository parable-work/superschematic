/*
A multipart/form-data body (RFC 7578) read part by part, as Go's
mime/multipart reads one (D81). Request.formData() does not serve: Bun's
reads a file part's media type from its filename's extension, not from the
part's Content-Type, so the Content-Type a route checks would depend on the
runtime. The body is read whole, which the route's body cap bounds before
it is read. Each search stays within the part it reads, so the work is
linear in the body, and a body past Go's ReadForm limits is refused as Go
refuses it: MAX_MULTIPART_PARTS, MAX_MULTIPART_HEADER_LINES and
MAX_MULTIPART_HEADER_BYTES.
*/

/** One part of a multipart body. */
export interface MultipartPart {
  /** The name Content-Disposition gives it. */
  readonly name: string;
  /** Its filename, the part after the last /; undefined for a part that is no file. */
  readonly filename?: string;
  /** Its Content-Type as sent, "" when it has none. */
  readonly contentType: string;
  readonly body: Uint8Array;
}

/** A body that is no multipart/form-data, or one this reader cannot read. */
export class MultipartError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'MultipartError';
  }
}

/** The boundary a multipart/form-data Content-Type names. */
export function multipartBoundary(contentType: string): string {
  const params = parseParams(contentType.slice(contentType.indexOf(';') + 1));
  const boundary = params.get('boundary');
  if (contentType.indexOf(';') < 0 || boundary === undefined || boundary === '' || boundary.length > 70) {
    throw new MultipartError('multipart/form-data without a boundary');
  }
  return boundary;
}

/** The most parts a body may hold, as Go's ReadForm reads at most 1000. */
export const MAX_MULTIPART_PARTS = 1000;
/**
 * The most header lines a body's file parts may hold in all, as Go's
 * ReadForm counts them: a part with more lines than are left is refused,
 * and a file part's lines are taken from what is left.
 */
export const MAX_MULTIPART_HEADER_LINES = 10_000;
/** The most bytes a body's part headers may hold in all, Go's maxMIMEHeaderSize. */
export const MAX_MULTIPART_HEADER_BYTES = 10 << 20;

const encoder = new TextEncoder();
const decoder = new TextDecoder();
const BLANK_LINE = encoder.encode('\r\n\r\n');

/**
 * The first index at or after from, and before before, at which needle
 * starts in haystack, or -1. It reads no byte at or past before plus the
 * needle's length, so a search bounded to a part costs that part alone.
 */
function indexOf(haystack: Uint8Array, needle: Uint8Array, from: number, before = haystack.length): number {
  const window = haystack.subarray(0, Math.min(haystack.length, before + needle.length - 1));
  const first = needle[0]!;
  for (let i = window.indexOf(first, from); i >= 0 && i < before && i + needle.length <= window.length; i = window.indexOf(first, i + 1)) {
    let j = 1;
    while (j < needle.length && window[i + j] === needle[j]) j++;
    if (j === needle.length) return i;
  }
  return -1;
}

/**
 * Reads every part of a multipart body delimited by boundary; a
 * MultipartError when it is no multipart body or holds more than the
 * limits above.
 */
export function parseMultipart(body: Uint8Array, boundary: string): MultipartPart[] {
  const delimiter = encoder.encode(`--${boundary}`);
  const partEnd = encoder.encode(`\r\n--${boundary}`);
  const parts: MultipartPart[] = [];
  let count = 0;
  let headerLinesLeft = MAX_MULTIPART_HEADER_LINES;
  let headerBytesLeft = MAX_MULTIPART_HEADER_BYTES;
  // The first delimiter starts the body or follows the preamble's CRLF.
  let at = indexOf(body, delimiter, 0);
  if (at < 0 || (at > 0 && (body[at - 2] !== 13 || body[at - 1] !== 10))) throw new MultipartError('multipart body without its boundary');
  for (;;) {
    let cursor = at + delimiter.length;
    if (body[cursor] === 45 && body[cursor + 1] === 45) return parts; // the closing delimiter, --boundary--
    // Linear white space may follow a delimiter, then its CRLF.
    while (body[cursor] === 32 || body[cursor] === 9) cursor++;
    if (body[cursor] !== 13 || body[cursor + 1] !== 10) throw new MultipartError('multipart delimiter without its line end');
    cursor += 2;
    if (++count > MAX_MULTIPART_PARTS) throw new MultipartError(`multipart body of more than ${MAX_MULTIPART_PARTS} parts`);
    const next = indexOf(body, partEnd, cursor);
    if (next < 0) throw new MultipartError('multipart body without its closing boundary');
    let lines: string[] = [];
    let content: number;
    if (body[cursor] === 13 && body[cursor + 1] === 10) {
      content = cursor + 2; // a part with no header
    } else {
      // The blank line that ends the headers, looked for within the part
      // and the header bytes left.
      const headersEnd = indexOf(body, BLANK_LINE, cursor, Math.min(next, cursor + headerBytesLeft + 1));
      if (headersEnd < 0) {
        if (next - cursor > headerBytesLeft) throw new MultipartError(`multipart headers of more than ${MAX_MULTIPART_HEADER_BYTES} bytes`);
        throw new MultipartError('multipart part without the end of its headers');
      }
      headerBytesLeft -= headersEnd - cursor;
      lines = decoder.decode(body.subarray(cursor, headersEnd)).split('\r\n');
      content = headersEnd + 4;
    }
    if (lines.length > headerLinesLeft) throw new MultipartError(`multipart headers of more than ${MAX_MULTIPART_HEADER_LINES} lines`);
    const headers = new Map<string, string>();
    for (const line of lines) {
      const colon = line.indexOf(':');
      if (colon > 0) headers.set(line.slice(0, colon).trim().toLowerCase(), line.slice(colon + 1).trim());
    }
    const disposition = headers.get('content-disposition') ?? '';
    const params = parseParams(disposition.slice(disposition.indexOf(';') + 1));
    if (disposition.split(';', 1)[0]!.trim().toLowerCase() === 'form-data' && params.has('name')) {
      const filename = fileName(params.get('filename*') ?? params.get('filename'));
      if (filename !== undefined) headerLinesLeft -= lines.length;
      parts.push({
        name: params.get('name')!,
        ...(filename !== undefined ? { filename } : {}),
        contentType: headers.get('content-type') ?? '',
        body: body.subarray(content, next),
      });
    }
    at = next + 2;
  }
}

/** A part's filename without its directory, as Go's Part.FileName reads it; undefined for none. */
function fileName(raw: string | undefined): string | undefined {
  if (raw === undefined || raw === '') return undefined;
  const base = raw.replace(/\/+$/u, '');
  const name = base.slice(base.lastIndexOf('/') + 1);
  return name === '' || name === '.' ? undefined : name;
}

/**
 * Header parameters, `; key=value` or `; key="quoted \"value\""`, keys
 * lowercased; a key*=charset''value one (RFC 8187) is decoded under key*.
 */
function parseParams(text: string): Map<string, string> {
  const params = new Map<string, string>();
  const pattern = /\s*([^\s=;]+)\s*=\s*(?:"((?:[^"\\]|\\.)*)"|([^;]*))\s*(?:;|$)/gsu;
  for (const match of text.matchAll(pattern)) {
    const key = match[1]!.toLowerCase();
    let value = match[2] !== undefined ? match[2].replace(/\\(.)/gsu, '$1') : match[3]!.trim();
    if (key.endsWith('*')) {
      const encoded = /^([^']*)'[^']*'(.*)$/su.exec(value);
      if (!encoded || encoded[1]!.toLowerCase() !== 'utf-8') continue;
      try {
        value = decodeURIComponent(encoded[2]!);
      } catch {
        continue;
      }
    }
    if (!params.has(key)) params.set(key, value);
  }
  return params;
}
