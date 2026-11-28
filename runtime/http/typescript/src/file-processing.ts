import { ObjectNotFoundError, type Bucket } from './bucket.js';
import { createLogger, type Logger } from './logger.js';
import { readsMediaType, type FileProcessor, type PreviewOptions } from './providers/files.js';
import type { Providers } from './providers/registry.js';
import { previewObjectOf, type Attachments } from './rowfeatures.js';

/*
The file processor's wiring (D154, D159, #702): attachments' pending and
processed are its seam. An upload's complete asks its text (extraction
pending), and a file's first view asks its preview (preview pending).
processFiles takes what is pending, oldest first, reads each object's bytes
from the bucket, and hands them to the file processors the environment's
files slots name, in slot order, the first that reads the media type and
answers: extractText's text is recorded as the file's extractedText on
every file that names the object, which search's triggers hear and index
with its row (D155); preview's image is put beside the object, at
previewObjectOf, and recorded as the file's preview. A file no processor
reads, one past maxBytes, one whose object is gone, and one a processor
throws on is recorded failed, so it is not asked again, and the reason is
logged without the file's bytes or text.

It runs within its caller's invocation: a job's or a worker's, or an
operation's after a write. Each slot's provider runs as its own principal
(D159), and the ORM it is given writes as its caller.
*/

/** What processFiles reads and writes. */
export interface FileProcessingOptions {
  /** An ORM, or a transaction, whose database declares @attachments. */
  readonly orm: { readonly attachments: Attachments };
  /** The providers the environment configures: every files slot it fills is asked, in slot order. */
  readonly providers: Providers;
  /** The bucket the attachments' objects are in, which a preview is put in too. */
  readonly bucket: Bucket;
  readonly log?: Logger;
  /** How large a preview may be: 320 by 160 unless set. */
  readonly preview?: PreviewOptions;
  /** The largest object read, in bytes: 25 MiB unless set. A larger one is recorded failed. */
  readonly maxBytes?: number;
}

/** What one pass of processFiles did. */
export interface FileProcessingRun {
  /** Objects whose text was recorded. */
  readonly extracted: number;
  /** Objects whose preview was put and recorded. */
  readonly previewed: number;
  /** Objects recorded failed, of either kind. */
  readonly failed: number;
}

/** The largest object processFiles reads unless set: 25 MiB. */
export const DEFAULT_PROCESS_MAX_BYTES = 25 * 1024 * 1024;

/** How many objects of each kind one pass takes unless set. */
export const DEFAULT_PROCESS_LIMIT = 20;

/** Why an object was not processed, for the log and the failed record. */
class Unprocessable extends Error {}

/**
 * One pass over what is pending: up to limit objects whose text has yet to
 * be extracted, then up to limit whose preview has yet to be made.
 */
export async function processFiles(options: FileProcessingOptions, run: { readonly limit?: number } = {}): Promise<FileProcessingRun> {
  const log = options.log ?? createLogger({ component: 'files' });
  const limit = run.limit ?? DEFAULT_PROCESS_LIMIT;
  const maxBytes = options.maxBytes ?? DEFAULT_PROCESS_MAX_BYTES;
  const slots = options.providers.configured('files').map(c => c.slot);
  let extracted = 0;
  let previewed = 0;
  let failed = 0;
  const processors = async (mediaType: string): Promise<FileProcessor[]> => {
    const out: FileProcessor[] = [];
    for (const slot of slots) {
      const processor = await options.providers.files(slot);
      if (readsMediaType(processor.mediaTypes, mediaType)) out.push(processor);
    }
    return out;
  };
  for (const kind of ['extraction', 'preview'] as const) {
    for (const file of await options.orm.attachments.pending(kind, { limit })) {
      const at = { objectKey: file.objectKey, mediaType: file.mediaType, kind };
      try {
        const readers = await processors(file.mediaType);
        if (readers.length === 0) throw new Unprocessable(`no files slot of the environment reads ${file.mediaType}`);
        const bytes = await readObject(options.bucket, file.objectKey, maxBytes);
        const input = { mediaType: file.mediaType, bytes };
        if (kind === 'extraction') {
          let text: string | undefined;
          for (const processor of readers) {
            const answer = await processor.extractText(input);
            if (answer) {
              text = answer.text;
              break;
            }
          }
          if (text === undefined) throw new Unprocessable(`no file processor extracts the text of ${file.mediaType}`);
          await options.orm.attachments.processed({ objectKey: file.objectKey, extraction: { state: 'done', text } });
          extracted++;
        } else {
          let made: { mediaType: string; bytes: Uint8Array } | undefined;
          for (const processor of readers) {
            const answer = await processor.preview(input, options.preview ?? { maxWidth: 320, maxHeight: 160 });
            if (answer) {
              made = answer;
              break;
            }
          }
          if (made === undefined) throw new Unprocessable(`no file processor previews ${file.mediaType}`);
          const object = previewObjectOf(file.objectKey);
          await options.bucket.put(object, made.bytes, { contentType: made.mediaType });
          await options.orm.attachments.processed({ objectKey: file.objectKey, preview: { state: 'done', object } });
          previewed++;
        }
      } catch (error) {
        const reason = error instanceof Unprocessable ? error.message : error instanceof Error ? error.message : String(error);
        log.warn('a file was not processed', { ...at, reason });
        await options.orm.attachments.processed(kind === 'extraction' ? { objectKey: file.objectKey, extraction: { state: 'failed' } } : { objectKey: file.objectKey, preview: { state: 'failed' } });
        failed++;
      }
    }
  }
  return { extracted, previewed, failed };
}

/** The bytes of the object name, refused past maxBytes or when it is gone. */
async function readObject(bucket: Bucket, name: string, maxBytes: number): Promise<Uint8Array> {
  let read;
  try {
    read = await bucket.get(name);
  } catch (error) {
    if (error instanceof ObjectNotFoundError) throw new Unprocessable(`object ${name} is gone`);
    throw error;
  }
  if (read.object.size > maxBytes) {
    await read.body.cancel().catch(() => undefined);
    throw new Unprocessable(`object ${name} holds ${read.object.size} bytes, more than the ${maxBytes} a pass reads`);
  }
  const chunks: Uint8Array[] = [];
  let size = 0;
  const reader = read.body.getReader();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > maxBytes) {
      await reader.cancel().catch(() => undefined);
      throw new Unprocessable(`object ${name} holds more than the ${maxBytes} bytes a pass reads`);
    }
    chunks.push(value);
  }
  const out = new Uint8Array(size);
  let at = 0;
  for (const chunk of chunks) {
    out.set(chunk, at);
    at += chunk.byteLength;
  }
  return out;
}
