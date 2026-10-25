/*
A multipart/form-data body (RFC 7578) read part by part, as Go's
mime/multipart reads one (D81). Request.formData() does not serve: Bun's
reads a file part's media type from its filename's extension, not from the
part's Content-Type, so the Content-Type a route checks would depend on the
runtime. The body is read whole, which the route's body cap bounds before
it is read.
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

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/** The index of needle in haystack at or after from, or -1. */
function indexOf(haystack: Uint8Array, needle: Uint8Array, from: number): number {
  const first = needle[0]!;
  for (let i = haystack.indexOf(first, from); i >= 0 && i + needle.length <= haystack.length; i = haystack.indexOf(first, i + 1)) {
    let j = 1;
    while (j < needle.length && haystack[i + j] === needle[j]) j++;
    if (j === needle.length) return i;
  }
  return -1;
}

/** Reads every part of a multipart body delimited by boundary. */
export function parseMultipart(body: Uint8Array, boundary: string): MultipartPart[] {
  const delimiter = encoder.encode(`--${boundary}`);
  const parts: MultipartPart[] = [];
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
    const headersEnd = indexOf(body, encoder.encode('\r\n\r\n'), cursor);
    const next = indexOf(body, encoder.encode(`\r\n--${boundary}`), cursor);
    if (next < 0) throw new MultipartError('multipart body without its closing boundary');
    let headerText = '';
    let content = next;
    if (body[cursor] === 13 && body[cursor + 1] === 10) {
      content = cursor + 2; // a part with no header
    } else if (headersEnd >= 0 && headersEnd < next) {
      headerText = decoder.decode(body.subarray(cursor, headersEnd));
      content = headersEnd + 4;
    } else {
      throw new MultipartError('multipart part without the end of its headers');
    }
    const headers = new Map<string, string>();
    for (const line of headerText.split('\r\n')) {
      const colon = line.indexOf(':');
      if (colon > 0) headers.set(line.slice(0, colon).trim().toLowerCase(), line.slice(colon + 1).trim());
    }
    const disposition = headers.get('content-disposition') ?? '';
    const params = parseParams(disposition.slice(disposition.indexOf(';') + 1));
    if (disposition.split(';', 1)[0]!.trim().toLowerCase() === 'form-data' && params.has('name')) {
      const filename = fileName(params.get('filename*') ?? params.get('filename'));
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
