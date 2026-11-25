import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { Hono } from 'hono';
import {
  HttpProblem,
  contentDisposition,
  downloadFilename,
  downloadResponse,
  entityTag,
  ifNoneMatchNames,
  parseHttpDate,
  sandboxPolicy,
  type FileDownload,
  type OperationDownload,
  type OperationSpec,
} from './index';
import { mountOperation } from './hono';

/*
A FileDownload result (D81) through the Hono adapter: the file, not the
envelope, with the schema's Content-Type and a Content-Disposition naming
the implementation's filename, or the schema's, and a refusal still the
problem envelope. contentDisposition answers every vector of
runtime/http/testdata/content_disposition_parity.json, as Go's
response.ContentDisposition does.
*/

const exportStatement: OperationSpec = {
  name: 'exportStatement',
  namespace: 'statements',
  method: 'GET',
  path: '/api/statements/{accountId}/export',
  pathParams: [{ name: 'accountId', kind: 'string', required: true }],
  queryParams: [],
  bodyParams: [],
  download: { contentType: 'text/csv; charset=utf-8', filename: 'statement.csv' },
  auth: { public: true, required: false, permissions: [] },
  manual: false,
};

function serve(result: (accountId: string) => Promise<FileDownload>, spec: OperationSpec = exportStatement) {
  const app = new Hono();
  mountOperation(app, spec, async (_ctx, request) => result(request.path.accountId as string));
  return app;
}

describe('a FileDownload result', () => {
  test('answers the file with its media type and the schema filename', async () => {
    const app = serve(async accountId => ({ body: `date,amount\n2026-03-01,${accountId}\n` }));
    const response = await app.request('/api/statements/a1/export', { headers: { 'x-request-id': 'req-1' } });
    expect(response.status).toBe(200);
    expect(response.headers.get('content-type')).toBe('text/csv; charset=utf-8');
    expect(response.headers.get('content-disposition')).toBe(`attachment; filename="statement.csv"; filename*=UTF-8''statement.csv`);
    expect(response.headers.get('cache-control')).toBe('no-store');
    expect(response.headers.get('x-request-id')).toBe('req-1');
    expect(await response.text()).toBe('date,amount\n2026-03-01,a1\n');
  });

  test('names the file as the implementation does, and sends a stream as it is written', async () => {
    const app = serve(async accountId => ({
      filename: `M\u00e4rz ${accountId}.csv`,
      headers: { 'last-modified': 'Sun, 01 Mar 2026 00:00:00 GMT', 'content-type': 'text/html' },
      body: new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(new TextEncoder().encode('a,b\n'));
          controller.enqueue(new TextEncoder().encode('1,2\n'));
          controller.close();
        },
      }),
    }));
    const response = await app.request('/api/statements/a1/export');
    expect(response.headers.get('content-disposition')).toBe(`attachment; filename="M_rz a1.csv"; filename*=UTF-8''M%C3%A4rz%20a1.csv`);
    // The schema's media type wins over the implementation's headers.
    expect(response.headers.get('content-type')).toBe('text/csv; charset=utf-8');
    expect(response.headers.get('last-modified')).toBe('Sun, 01 Mar 2026 00:00:00 GMT');
    expect(await response.text()).toBe('a,b\n1,2\n');
  });

  test('offers no filename when neither the implementation nor the schema names one', async () => {
    const bytes = new Uint8Array([0, 1, 2, 255]);
    const app = serve(async () => ({ body: bytes }), { ...exportStatement, download: { contentType: 'application/octet-stream' } });
    const response = await app.request('/api/statements/a1/export');
    expect(response.headers.get('content-disposition')).toBe('attachment');
    expect(new Uint8Array(await response.arrayBuffer())).toEqual(bytes);
  });

  test('answers a refusal as the problem envelope', async () => {
    const app = serve(async () => {
      throw new HttpProblem(404, 'No such account', { code: 'not_found' });
    });
    const response = await app.request('/api/statements/a1/export');
    expect(response.status).toBe(404);
    expect(response.headers.get('content-type')).toBe('application/problem+json');
    expect((await response.json()).code).toBe('not_found');
  });

  test('answers 500 for a result that is no file', async () => {
    const app = serve(async () => 'not a file' as unknown as FileDownload);
    const response = await app.request('/api/statements/a1/export');
    expect(response.status).toBe(500);
  });
});

describe('contentDisposition', () => {
  const parity = JSON.parse(readFileSync(new URL('../../testdata/content_disposition_parity.json', import.meta.url), 'utf8')) as {
    vectors: { name: string; filename: string; header: string }[];
  };

  for (const vector of parity.vectors) {
    test(vector.name, () => {
      expect(contentDisposition(vector.filename)).toBe(vector.header);
    });
  }

  test('drops directories and control characters from a name', () => {
    expect(downloadFilename('a/b\\c.csv')).toBe('c.csv');
    expect(downloadFilename('\u0000.csv')).toBe('.csv');
    expect(contentDisposition(undefined)).toBe('attachment');
  });
});

/*
What D141 adds to a file answer: the disposition, the media types an
implementation may name, the Cache-Control, the sandbox and the
validators, each vector of runtime/http/testdata/file_answer_parity.json
answered as Go's response.ServeFile, EntityTag, ParseHTTPDate and
IfNoneMatchNames answer it.
*/

interface FileAnswerParity {
  etags: { name: string; tag: string; header?: string; refused?: boolean }[];
  dates: { name: string; value: string; seconds: number | null }[];
  matches: { name: string; header: string; etag: string; names: boolean }[];
  answers: {
    name: string;
    download: OperationDownload;
    file: { filename?: string; contentType?: string; etag?: string; lastModified?: string; headers?: Record<string, string> };
    method: 'GET' | 'POST';
    request: Record<string, string>;
    status: number;
    body?: string;
    headers: Record<string, string | null>;
  }[];
}

const fileAnswers = JSON.parse(readFileSync(new URL('../../testdata/file_answer_parity.json', import.meta.url), 'utf8')) as FileAnswerParity;

describe('a file answer (D141)', () => {
  for (const vector of fileAnswers.etags) {
    test(`entityTag: ${vector.name}`, () => {
      if (vector.refused) expect(() => entityTag(vector.tag)).toThrow(TypeError);
      else expect(entityTag(vector.tag)).toBe(vector.header!);
    });
  }

  for (const vector of fileAnswers.dates) {
    test(`parseHttpDate: ${vector.name}`, () => {
      expect(parseHttpDate(vector.value) ?? null).toBe(vector.seconds);
    });
  }

  for (const vector of fileAnswers.matches) {
    test(`ifNoneMatchNames: ${vector.name}`, () => {
      expect(ifNoneMatchNames(vector.header, vector.etag)).toBe(vector.names);
    });
  }

  for (const vector of fileAnswers.answers) {
    test(`answers: ${vector.name}`, async () => {
      let cancelled = false;
      const body = new ReadableStream<Uint8Array>({
        pull(controller) {
          controller.enqueue(new TextEncoder().encode('the file'));
          controller.close();
        },
        cancel() {
          cancelled = true;
        },
      });
      const { lastModified, ...file } = vector.file;
      const spec: OperationSpec = { ...exportStatement, method: vector.method, download: vector.download };
      const app = serve(async () => ({ ...file, ...(lastModified !== undefined ? { lastModified: new Date(lastModified) } : {}), body }), spec);
      const response = await app.request('/api/statements/a1/export', { method: vector.method, headers: vector.request });
      expect(response.status).toBe(vector.status);
      for (const [name, want] of Object.entries(vector.headers)) expect([name, response.headers.get(name)]).toEqual([name, want]);
      const text = await response.text();
      if (vector.body !== undefined) expect(text).toBe(vector.body);
      else if (vector.status === 200) expect(text).toBe('the file');
      // A body the route does not send is cancelled unread.
      if (vector.status !== 200) expect(cancelled).toBe(true);
    });
  }

  test('sandboxPolicy writes the policy of each form', () => {
    expect(sandboxPolicy(undefined)).toBeUndefined();
    expect(sandboxPolicy(false)).toBeUndefined();
    expect(sandboxPolicy(true)).toBe('sandbox');
    expect(sandboxPolicy(['allow-scripts', 'allow-forms'])).toBe('sandbox allow-scripts allow-forms');
  });

  test('refuses a lastModified that holds no time', async () => {
    const app = serve(async () => ({ body: 'x', lastModified: new Date(Number.NaN) }));
    expect((await app.request('/api/statements/a1/export')).status).toBe(500);
  });

  test('a response with no request is never conditional', () => {
    const response = downloadResponse({ body: 'x', etag: 'v3' }, { contentType: 'text/plain' }, 'req-1');
    expect(response.status).toBe(200);
    expect(response.headers.get('etag')).toBe('"v3"');
  });
});
