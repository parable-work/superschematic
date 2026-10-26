import { describe, expect, test } from 'bun:test';
import { MAX_MULTIPART_HEADER_BYTES, MAX_MULTIPART_HEADER_LINES, MAX_MULTIPART_PARTS, MultipartError, multipartBoundary, parseMultipart } from './multipart';

/*
The multipart reader (D81) reads each part's name, filename and
Content-Type as Go's mime/multipart does: a file part keeps the media type
it was sent with, whatever its filename's extension says.
*/

const encode = (text: string) => new TextEncoder().encode(text);
const text = (bytes: Uint8Array) => new TextDecoder().decode(bytes);

describe('parseMultipart', () => {
  test('reads each part, a file part with the Content-Type it was sent with', () => {
    const body = encode(
      'a preamble\r\n' +
        '--B\r\nContent-Disposition: form-data; name="data"\r\n\r\n{"a":1}\r\n' +
        '--B  \r\ncontent-disposition: form-data; name="statement"; filename="exports/march.csv"\r\nContent-Type: text/plain\r\n\r\nline 1\r\nline 2\r\n' +
        '--B\r\nContent-Disposition: form-data; name="say \\"hi\\""; filename*=UTF-8\'\'%C3%9Cbersicht.ofx\r\n\r\n\r\n' +
        '--B\r\nContent-Disposition: attachment; name="ignored"\r\n\r\nx\r\n' +
        '--B\r\nContent-Disposition: form-data; name="empty"; filename=""\r\n\r\n\r\n' +
        '--B--\r\nan epilogue'
    );
    const parts = parseMultipart(body, 'B');
    expect(parts.map(part => [part.name, part.filename, part.contentType, text(part.body)])).toEqual([
      ['data', undefined, '', '{"a":1}'],
      ['statement', 'march.csv', 'text/plain', 'line 1\r\nline 2'],
      ['say "hi"', '\u00dcbersicht.ofx', '', ''],
      ['empty', undefined, '', ''],
    ]);
  });

  test('keeps a part body byte for byte', () => {
    const bytes = new Uint8Array([0, 13, 10, 45, 45, 255]);
    const head = encode('--x\r\nContent-Disposition: form-data; name="f"; filename="a.bin"\r\n\r\n');
    const tail = encode('\r\n--x--');
    const body = new Uint8Array([...head, ...bytes, ...tail]);
    expect(parseMultipart(body, 'x')[0]!.body).toEqual(bytes);
  });

  test('refuses a body without its boundaries', () => {
    expect(() => parseMultipart(encode('no boundary here'), 'B')).toThrow(MultipartError);
    expect(() => parseMultipart(encode('--B\r\nContent-Disposition: form-data; name="a"\r\n\r\nx'), 'B')).toThrow(MultipartError);
    expect(() => parseMultipart(encode('--Bx\r\n'), 'B')).toThrow(MultipartError);
  });
});

describe('multipartBoundary', () => {
  test('reads the boundary, quoted or not', () => {
    expect(multipartBoundary('multipart/form-data; boundary=abc')).toBe('abc');
    expect(multipartBoundary('multipart/form-data; charset=utf-8; boundary="a b:c"')).toBe('a b:c');
    expect(() => multipartBoundary('multipart/form-data')).toThrow(MultipartError);
    expect(() => multipartBoundary(`multipart/form-data; boundary=${'b'.repeat(71)}`)).toThrow(MultipartError);
  });
});

/*
The reader's limits (D81) are Go's ReadForm's, so a body one router refuses
the other refuses: 1000 parts, 10000 header lines of file parts, and 10 MiB
of headers. Each search stays within its part, so a body costs its length:
the bounds below are far above what the reads take (a few milliseconds),
so that a loaded machine does not fail them.
*/
describe('parseMultipart limits', () => {
  test('reads a body of many parts in time linear in its length', () => {
    const lines = 'a\r\n'.repeat(360);
    const headerless = encode(`--B\r\n\r\n${lines}`.repeat(MAX_MULTIPART_PARTS - 1) + '--B--');
    const named = encode(`--B\r\nContent-Disposition: form-data; name="a"\r\n\r\n${lines}\r\n`.repeat(MAX_MULTIPART_PARTS - 1) + '--B--');
    for (const body of [headerless, named]) {
      expect(body.length).toBeGreaterThan(1 << 20);
      const started = performance.now();
      parseMultipart(body, 'B');
      expect(performance.now() - started).toBeLessThan(500);
    }
    expect(parseMultipart(named, 'B')).toHaveLength(MAX_MULTIPART_PARTS - 1);
  });

  test('reads 1000 parts and refuses 1001, as Go does, without reading past the limit', () => {
    const part = '--B\r\nContent-Disposition: form-data; name="a"\r\n\r\nx\r\n';
    expect(parseMultipart(encode(part.repeat(MAX_MULTIPART_PARTS) + '--B--'), 'B')).toHaveLength(MAX_MULTIPART_PARTS);
    expect(() => parseMultipart(encode(part.repeat(MAX_MULTIPART_PARTS + 1) + '--B--'), 'B')).toThrow(
      new MultipartError(`multipart body of more than ${MAX_MULTIPART_PARTS} parts`)
    );
    const flood = encode('--B\r\n\r\nx\r\n'.repeat(100_000) + '--B--');
    const started = performance.now();
    expect(() => parseMultipart(flood, 'B')).toThrow(MultipartError);
    expect(performance.now() - started).toBeLessThan(500);
  });

  test('counts the header lines of file parts against 10000, and refuses a part with more than are left', () => {
    const extra = Array.from({ length: 10 }, (_, i) => `X-${i}: ${i}\r\n`).join('');
    const filePart = `--B\r\nContent-Disposition: form-data; name="f"; filename="a.txt"\r\n${extra}\r\nx\r\n`;
    const textPart = `--B\r\nContent-Disposition: form-data; name="a"\r\n${extra}\r\nx\r\n`;
    // 11 lines each: 909 file parts hold 9999, and 910 more than 10000.
    expect(parseMultipart(encode(filePart.repeat(909) + '--B--'), 'B')).toHaveLength(909);
    expect(() => parseMultipart(encode(filePart.repeat(910) + '--B--'), 'B')).toThrow(
      new MultipartError(`multipart headers of more than ${MAX_MULTIPART_HEADER_LINES} lines`)
    );
    // A text part's lines are not taken from what is left, as in Go.
    expect(parseMultipart(encode(textPart.repeat(1000) + '--B--'), 'B')).toHaveLength(1000);
    const long = `--B\r\nContent-Disposition: form-data; name="a"\r\n${'X: 1\r\n'.repeat(MAX_MULTIPART_HEADER_LINES)}\r\nx\r\n--B--`;
    expect(() => parseMultipart(encode(long), 'B')).toThrow(new MultipartError(`multipart headers of more than ${MAX_MULTIPART_HEADER_LINES} lines`));
  });

  test('refuses more than 10 MiB of headers, in all, without looking past them', () => {
    const half = `X-Big: ${'h'.repeat(MAX_MULTIPART_HEADER_BYTES / 2)}`;
    const part = `--B\r\nContent-Disposition: form-data; name="a"\r\n${half}\r\n\r\nx\r\n`;
    expect(() => parseMultipart(encode(part.repeat(2) + '--B--'), 'B')).toThrow(
      new MultipartError(`multipart headers of more than ${MAX_MULTIPART_HEADER_BYTES} bytes`)
    );
    expect(parseMultipart(encode(part + '--B--'), 'B')).toHaveLength(1);
    // Headers that never end, followed by a long part body.
    const endless = encode(`--B\r\nX: ${'h'.repeat(MAX_MULTIPART_HEADER_BYTES)}\r\n${'y'.repeat(4 << 20)}\r\n--B--`);
    const started = performance.now();
    expect(() => parseMultipart(endless, 'B')).toThrow(new MultipartError(`multipart headers of more than ${MAX_MULTIPART_HEADER_BYTES} bytes`));
    expect(performance.now() - started).toBeLessThan(1000);
  });
});
