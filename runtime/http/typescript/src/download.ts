import type { OperationDownload } from './operation.js';

/*
A FileDownload result (D81). The implementation returns the file: its body
and, when it names one, the filename. The route answers the body with the
schema's Content-Type and Content-Disposition: attachment, with filename,
an ASCII fallback, and filename*, the name in UTF-8 (RFC 6266, RFC 8187).
A stream body is sent as the implementation writes it. The Go server
writes the same header for the same name
(runtime/http/testdata/content_disposition_parity.json).
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
  /** Headers beside Content-Type and Content-Disposition, such as Last-Modified. */
  readonly headers?: Readonly<Record<string, string>>;
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
 * The Content-Disposition of an attachment named name: `attachment`, or
 * with a name, `attachment; filename="<ASCII>"; filename*=UTF-8''<encoded>`,
 * where the ASCII fallback has each character outside printable ASCII, and
 * each of `"`, `\` and `%`, replaced by `_`.
 */
export function contentDisposition(name: string | undefined): string {
  const filename = downloadFilename(name ?? '');
  if (filename === '') return 'attachment';
  const fallback = [...filename].map(char => (/^[ -~]$/u.test(char) && !'"\\%'.includes(char) ? char : '_')).join('');
  let encoded = '';
  for (const char of filename) {
    if (ATTR_CHAR.test(char)) {
      encoded += char;
      continue;
    }
    for (const byte of new TextEncoder().encode(char)) encoded += '%' + byte.toString(16).toUpperCase().padStart(2, '0');
  }
  return `attachment; filename="${fallback}"; filename*=UTF-8''${encoded}`;
}

/** The response of a FileDownload result: the file, its media type and its name. */
export function downloadResponse(result: unknown, download: OperationDownload, requestId: string): Response {
  if (typeof result !== 'object' || result === null || !('body' in result)) {
    throw new TypeError(`a FileDownload result is { body, filename? }, not ${result === null ? 'null' : typeof result}`);
  }
  const file = result as FileDownload;
  return new Response(file.body as BodyInit, {
    status: 200,
    headers: {
      'cache-control': 'no-store',
      ...(file.headers ?? {}),
      'content-type': download.contentType,
      'content-disposition': contentDisposition(file.filename ?? download.filename),
      'x-request-id': requestId,
    },
  });
}
