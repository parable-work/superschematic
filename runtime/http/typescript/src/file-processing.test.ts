import { describe, expect, test } from 'bun:test';
import { ObjectNotFoundError, type Bucket, type BucketBody, type PutOptions } from './bucket';
import { processFiles } from './file-processing';
import { createLogger } from './logger';
import { defineProvider, providersFrom, type FileProcessor } from './providers';
import { previewObjectOf, type Attachments } from './rowfeatures';

/** A bucket in memory, with the objects it holds. */
function memoryBucket(objects: Map<string, { bytes: Uint8Array; contentType?: string }>): Bucket {
  const objectOf = (name: string) => ({ name, size: objects.get(name)!.bytes.byteLength });
  return {
    name: 'mem',
    async put(name: string, body: BucketBody, options: PutOptions = {}) {
      const bytes = typeof body === 'string' ? new TextEncoder().encode(body) : body instanceof Uint8Array ? body : new Uint8Array(await new Response(body).arrayBuffer());
      objects.set(name, { bytes, ...(options.contentType ? { contentType: options.contentType } : {}) });
      return { ...objectOf(name), created: true };
    },
    async head(name: string) {
      return objects.has(name) ? objectOf(name) : undefined;
    },
    async get(name: string) {
      if (!objects.has(name)) throw new ObjectNotFoundError('mem', name);
      return { object: objectOf(name), body: new Response(objects.get(name)!.bytes).body! };
    },
    async delete(name: string) {
      objects.delete(name);
    },
    async list() {
      return { objects: [] };
    },
    async signedUrl(name: string) {
      return `https://mem.example/${name}`;
    },
  };
}

interface FileRow {
  objectKey: string;
  mediaType: string;
  extraction: string;
  extractedText?: string;
  preview: string;
  previewObject?: string;
}

/** The attachments' pending and processed over rows in memory, as the repositories run them. */
function memoryAttachments(rows: FileRow[]): Pick<Attachments, 'pending' | 'processed'> {
  return {
    async pending(kind, options = {}) {
      const seen = new Set<string>();
      const out: { objectKey: string; mediaType: string; contentHash: string }[] = [];
      for (const row of rows) {
        if ((kind === 'preview' ? row.preview : row.extraction) !== 'pending' || seen.has(row.objectKey)) continue;
        seen.add(row.objectKey);
        out.push({ objectKey: row.objectKey, mediaType: row.mediaType, contentHash: 'h' });
      }
      return out.slice(0, options.limit ?? 100);
    },
    async processed(input) {
      let n = 0;
      for (const row of rows.filter(r => r.objectKey === input.objectKey)) {
        if (input.extraction) {
          row.extraction = input.extraction.state;
          if (input.extraction.text !== undefined) row.extractedText = input.extraction.text;
        }
        if (input.preview) {
          row.preview = input.preview.state;
          if (input.preview.object !== undefined) row.previewObject = input.preview.object;
        }
        n++;
      }
      return n;
    },
  };
}

/** A processor of text alone, named name, that records what it was handed. */
function textProcessor(name: string, seen: string[]) {
  return defineProvider({
    name,
    implements: 'files',
    config: {},
    create(): FileProcessor {
      return {
        mediaTypes: ['text/*'],
        async extractText(file) {
          seen.push(`${name}:${file.mediaType}`);
          return { text: new TextDecoder().decode(file.bytes).toUpperCase() };
        },
        async preview(file) {
          if (file.mediaType === 'text/broken') throw new Error('the preview failed');
          return { mediaType: 'image/svg+xml', bytes: new TextEncoder().encode('<svg/>'), width: 1, height: 1 };
        },
      };
    },
  });
}

describe('processFiles', () => {
  test("hands each pending object to the first files slot that reads it, records its text and puts its preview beside it", async () => {
    const objects = new Map<string, { bytes: Uint8Array; contentType?: string }>([
      ['attachments/a', { bytes: new TextEncoder().encode('hello') }],
      ['attachments/b', { bytes: new Uint8Array([1, 2, 3]) }],
      ['attachments/c', { bytes: new TextEncoder().encode('x') }],
    ]);
    const rows: FileRow[] = [
      // Two files of one object, one filed onto another row: recorded on both.
      { objectKey: 'attachments/a', mediaType: 'text/plain', extraction: 'pending', preview: 'pending' },
      { objectKey: 'attachments/a', mediaType: 'text/plain', extraction: 'pending', preview: 'none' },
      // A type no slot reads.
      { objectKey: 'attachments/b', mediaType: 'image/png', extraction: 'pending', preview: 'none' },
      // A preview the processor throws on, and an object that is gone.
      { objectKey: 'attachments/c', mediaType: 'text/broken', extraction: 'done', preview: 'pending' },
      { objectKey: 'attachments/gone', mediaType: 'text/plain', extraction: 'pending', preview: 'none' },
    ];
    const seen: string[] = [];
    const lines: string[] = [];
    const providers = providersFrom({
      slots: { FILES: 'files', MORE_FILES: 'files' },
      config: { FILES: 'first', MORE_FILES: 'second' },
      definitions: [textProcessor('first', seen), textProcessor('second', seen)],
    });
    const run = await processFiles({
      orm: { attachments: memoryAttachments(rows) as Attachments },
      providers,
      bucket: memoryBucket(objects),
      log: createLogger({}, { write: line => lines.push(line) }),
    });
    expect(run).toEqual({ extracted: 1, previewed: 1, failed: 3 });
    expect(rows[0]).toMatchObject({ extraction: 'done', extractedText: 'HELLO', preview: 'done', previewObject: previewObjectOf('attachments/a') });
    expect(rows[1]).toMatchObject({ extraction: 'done', extractedText: 'HELLO' });
    expect(rows[2]).toMatchObject({ extraction: 'failed' });
    expect(rows[3]).toMatchObject({ preview: 'failed' });
    expect(rows[4]).toMatchObject({ extraction: 'failed' });
    expect(objects.get(previewObjectOf('attachments/a'))).toMatchObject({ contentType: 'image/svg+xml' });
    // The first slot that reads the type answers; the second is not asked.
    expect(seen).toEqual(['first:text/plain']);
    // Each failure is logged with its reason and never the file's text.
    const failures = lines.filter(line => line.includes('a file was not processed'));
    expect(failures).toHaveLength(3);
    expect(failures.some(line => line.includes('no files slot of the environment reads image/png'))).toBe(true);
    expect(lines.join('\n')).not.toContain('hello');
    // Nothing is pending now, so a second pass does nothing.
    expect(await processFiles({ orm: { attachments: memoryAttachments(rows) as Attachments }, providers, bucket: memoryBucket(objects) })).toEqual({ extracted: 0, previewed: 0, failed: 0 });
  });

  test('records an object past maxBytes failed without reading it whole', async () => {
    const objects = new Map([['attachments/big', { bytes: new Uint8Array(64) }]]);
    const rows: FileRow[] = [{ objectKey: 'attachments/big', mediaType: 'text/plain', extraction: 'pending', preview: 'none' }];
    const providers = providersFrom({ slots: { FILES: 'files' }, config: { FILES: 'first' }, definitions: [textProcessor('first', [])] });
    const run = await processFiles({ orm: { attachments: memoryAttachments(rows) as Attachments }, providers, bucket: memoryBucket(objects), maxBytes: 10, log: createLogger({}, { write: () => {} }) });
    expect(run).toEqual({ extracted: 0, previewed: 0, failed: 1 });
    expect(rows[0]!.extraction).toBe('failed');
  });
});
