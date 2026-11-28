import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { ObjectNotFoundError, type Bucket, type BucketBody, type PutOptions } from './bucket';
import { processFiles, type FileProcessingRun } from './file-processing';
import { createLogger } from './logger';
import { defineProvider, providersFrom, readsMediaType, type FileProcessor, type RuntimeInterface } from './providers';
import type { Attachments } from './rowfeatures';

/*
The file processor's pass (D154, D159, #603) over the scenarios the Go
runtime writes (`go test ./fileprocessing -run TestFileProcessingParity
-update` in runtime/http/go), which the Go and Rust passes run too: the
same files, objects and processors give the same counts, rows, previews,
calls and logged reasons in every runtime. The file's comment states the
harness.
*/

interface Row {
  objectKey: string;
  mediaType: string;
  extraction: string;
  extractedText?: string;
  preview: string;
  previewObject?: string;
}

interface Processor {
  readonly slot: string;
  readonly name: string;
  readonly mediaTypes: string[];
  readonly extract: 'upper' | 'none' | 'fail';
  readonly preview: 'svg' | 'none' | 'fail';
  readonly failsOn?: string[];
}

interface Scenario {
  readonly name: string;
  readonly processors: Processor[];
  readonly files: Row[];
  readonly objects: { name: string; text?: string; size?: number }[];
  readonly maxBytes: number;
  readonly want: {
    readonly runs: FileProcessingRun[];
    readonly files: Row[];
    readonly previews: { name: string; contentType: string; text: string }[];
    readonly asked: string[];
    readonly reasons: string[];
  };
}

const scenarios = (JSON.parse(readFileSync(fileURLToPath(new URL('../../testdata/file_processing_parity.json', import.meta.url)), 'utf8')) as { scenarios: Scenario[] }).scenarios;

async function run(scenario: Scenario) {
  const asked: string[] = [];
  const objects = new Map<string, { bytes: Uint8Array; contentType?: string }>();
  for (const o of scenario.objects) objects.set(o.name, { bytes: o.size ? new Uint8Array(o.size) : new TextEncoder().encode(o.text ?? '') });
  const bucket = {
    name: 'mem',
    async put(name: string, body: BucketBody, options: PutOptions = {}) {
      const bytes = body instanceof Uint8Array ? body : new TextEncoder().encode(String(body));
      objects.set(name, { bytes, ...(options.contentType ? { contentType: options.contentType } : {}) });
      return { name, size: bytes.byteLength, created: true };
    },
    async get(name: string) {
      const o = objects.get(name);
      if (!o) throw new ObjectNotFoundError('mem', name);
      return { object: { name, size: o.bytes.byteLength }, body: new Response(o.bytes).body! };
    },
  } as unknown as Bucket;
  const rows = scenario.files.map(row => ({ ...row }));
  const attachments: Pick<Attachments, 'pending' | 'processed'> = {
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
  const processors = scenario.processors;
  const slots: Record<string, RuntimeInterface> = {};
  const config: Record<string, string> = {};
  for (const p of processors) {
    slots[p.slot] = 'files';
    config[p.slot] = p.name;
  }
  const definitions = processors.map(p =>
    defineProvider({
      name: p.name,
      implements: 'files',
      config: {},
      create(): FileProcessor {
        const fails = (mediaType: string) => (p.failsOn ?? []).some(t => readsMediaType([t], mediaType));
        return {
          mediaTypes: p.mediaTypes,
          async extractText(file) {
            asked.push(`${p.name}:extractText:${file.mediaType}`);
            if (fails(file.mediaType) || p.extract === 'fail') throw new Error('the extraction failed');
            return p.extract === 'upper' ? { text: new TextDecoder().decode(file.bytes).toUpperCase() } : null;
          },
          async preview(file) {
            asked.push(`${p.name}:preview:${file.mediaType}`);
            if (fails(file.mediaType) || p.preview === 'fail') throw new Error('the preview failed');
            return p.preview === 'svg' ? { mediaType: 'image/svg+xml', bytes: new TextEncoder().encode('<svg/>'), width: 1, height: 1 } : null;
          },
        };
      },
    })
  );
  const providers = providersFrom({ slots, config, definitions });
  const lines: string[] = [];
  const log = createLogger({}, { write: line => lines.push(line) });
  const runs: FileProcessingRun[] = [];
  for (let i = 0; i < 2; i++) {
    runs.push(await processFiles({ orm: { attachments: attachments as Attachments }, providers, bucket, log, ...(scenario.maxBytes ? { maxBytes: scenario.maxBytes } : {}) }));
  }
  const previews = [...objects.entries()]
    .filter(([, o]) => o.contentType)
    .map(([name, o]) => ({ name, contentType: o.contentType!, text: new TextDecoder().decode(o.bytes) }))
    .sort((a, b) => (a.name < b.name ? -1 : 1));
  const logged = lines.map(line => JSON.parse(line) as { msg: string; reason: string });
  return { runs, files: rows, previews, asked, reasons: logged.map(l => l.reason), messages: logged.map(l => l.msg), lines };
}

describe('file processing parity', () => {
  test.each(scenarios.map(s => [s.name, s] as const))('%s', async (_, scenario) => {
    const got = await run(scenario);
    expect({ runs: got.runs, files: got.files, previews: got.previews, asked: got.asked, reasons: got.reasons }).toEqual(scenario.want);
    expect(got.messages.every(m => m === 'a file was not processed')).toBe(true);
    for (const o of scenario.objects) if (o.text) expect(got.lines.join('\n')).not.toContain(o.text);
  });
});
