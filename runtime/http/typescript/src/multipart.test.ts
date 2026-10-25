import { describe, expect, test } from 'bun:test';
import { MultipartError, multipartBoundary, parseMultipart } from './multipart';

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
