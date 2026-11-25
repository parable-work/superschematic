/*
Files end to end (D81), run by TestGeneratedFilesEndToEnd with API_DIR set
to the materialized fixture-files-api package and SDK_DIR to its generated
TypeScript SDK. The generated router serves files_implementation.ts, and
every call but the raw refusals goes through the SDK, whose fetch hands
each request to the app: a statement imported as a multipart body, its
lines exported as CSV under the schema's filename, the statement itself
under the name it was sent with, each file the statement field does not
admit refused at its field as every server refuses it, the @bodyLimit
cap, and a preview that answers inline with its validators, sandboxed, and
304 to a request whose validators match (D141).
*/
import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { parseMinClientVersions } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
if (!apiDir || !sdkDir) throw new Error('API_DIR and SDK_DIR are required');
const { buildRouter, operationSpecs } = await import(`${apiDir}/router.ts`);
const { imported, previews, statements } = await import(`${apiDir}/files_implementation.ts`);
const { FixtureFilesApiSDK, ApiError, ClientTooOldError } = await import(`${sdkDir}/index.ts`);

const BASE = 'http://api.example.com';
const ACCOUNT = '1VgEh72lXvTXkH';

function serve(options: Record<string, unknown> = {}) {
  const app = new Hono();
  app.route('/', buildRouter({ statements }, options));
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
  return app;
}

const app = serve();
const sdk = new FixtureFilesApiSDK({
  baseUrl: BASE,
  fetch: (input: RequestInfo | URL, init?: RequestInit) => app.request(String(input), init),
});

const statementFile = (text: string, name = 'march.csv', type = 'text/csv') => new File([text], name, { type });

/** The status, problem and field errors a call is refused with. */
async function refusal(call: Promise<unknown>): Promise<{ status: number; body: Record<string, unknown>; errors: unknown }> {
  try {
    await call;
  } catch (error) {
    expect(error).toBeInstanceOf(ApiError);
    const apiError = error as { statusCode: number; response: Record<string, unknown>; validationErrors?: unknown };
    return { status: apiError.statusCode, body: apiError.response, errors: apiError.validationErrors };
  }
  throw new Error('the call was not refused');
}

describe('generated fixture-files-api router and SDK', () => {
  test('the operation table carries the file parts and the downloads, and no route is manual', () => {
    expect(operationSpecs.importStatement.uploads.map((upload: { name: string }) => upload.name)).toEqual(['statement', 'receipt']);
    expect(operationSpecs.importStatement.bodyLimitBytes).toBe(1024 * 1024);
    expect(operationSpecs.exportStatement.download).toEqual({ contentType: 'text/csv; charset=utf-8', filename: 'statement.csv' });
    expect(operationSpecs.previewStatement.download).toEqual({
      contentType: 'text/plain; charset=utf-8',
      contentTypes: ['text/html; charset=utf-8'],
      filename: 'statement.txt',
      disposition: 'inline',
      cacheControl: 'private, no-cache',
      sandbox: ['allow-popups'],
    });
    for (const spec of Object.values(operationSpecs) as { manual: boolean }[]) expect(spec.manual).toBe(false);
  });

  test('previews a statement inline, sandboxed, with its validators, and answers 304 to a request that holds it (D141)', async () => {
    // The Go router answers the same (internal/generator/apigen/testdata/files_routes_test.go, TestPreview).
    const PREVIEWED = '1VgEh72lXvTXkL';
    const path = `/api/statements/${PREVIEWED}/preview`;
    await sdk.statements.importStatement({ accountId: PREVIEWED }, { statement: statementFile('date,amount\n2026-03-01,1\n') });

    const first = await app.request(path);
    expect(first.status).toBe(200);
    expect(Object.fromEntries(['content-type', 'content-disposition', 'cache-control', 'x-content-type-options', 'content-security-policy', 'cross-origin-resource-policy', 'etag', 'last-modified'].map(name => [name, first.headers.get(name)]))).toEqual({
      'content-type': 'text/plain; charset=utf-8',
      'content-disposition': `inline; filename="statement.txt"; filename*=UTF-8''statement.txt`,
      'cache-control': 'private, no-cache',
      'x-content-type-options': 'nosniff',
      'content-security-policy': 'sandbox allow-popups',
      'cross-origin-resource-policy': 'same-origin',
      etag: '"len-25"',
      'last-modified': 'Sun, 01 Mar 2026 00:00:00 GMT',
    });
    expect(await first.text()).toBe('date,amount\n2026-03-01,1\n');

    const before = { ...previews };
    const current: Record<string, string>[] = [
      { 'If-None-Match': '"len-25"' },
      { 'If-None-Match': '"len-1", W/"len-25"' },
      { 'If-None-Match': '*' },
      { 'If-Modified-Since': 'Sun, 01 Mar 2026 00:00:00 GMT' },
      { 'If-Modified-Since': 'Mon, 02 Mar 2026 00:00:00 GMT' },
    ];
    for (const headers of current) {
      const response = await app.request(path, { headers });
      expect([headers, response.status, await response.text()]).toEqual([headers, 304, '']);
      expect([response.headers.get('etag'), response.headers.get('cache-control'), response.headers.get('content-type')]).toEqual(['"len-25"', 'private, no-cache', null]);
    }
    // Each body the route did not send was cancelled.
    expect(previews).toEqual({ cancelled: before.cancelled + current.length });

    for (const headers of [
      { 'If-None-Match': '"len-1"' },
      { 'If-Modified-Since': 'Sat, 28 Feb 2026 23:59:59 GMT' },
      { 'If-None-Match': '"len-1"', 'If-Modified-Since': 'Mon, 02 Mar 2026 00:00:00 GMT' },
      { 'If-Modified-Since': '2026-03-02T00:00:00Z' },
    ] as Record<string, string>[]) {
      const response = await app.request(path, { headers });
      expect([headers, response.status, await response.text()]).toEqual([headers, 200, 'date,amount\n2026-03-01,1\n']);
    }

    // A media type and a name the implementation chose from the schema's
    // list, which the SDK hands the caller.
    await sdk.statements.importStatement({ accountId: PREVIEWED }, { statement: statementFile('OFXHEADER:100\n', 'april.ofx', 'application/x-ofx') });
    const page = await sdk.statements.previewStatement(PREVIEWED);
    expect([page.contentType, page.filename, await page.blob.text()]).toEqual(['text/html; charset=utf-8', 'statement.html', '<pre>OFXHEADER:100\n</pre>']);
    expect((await app.request(path)).headers.get('content-disposition')).toBe(`inline; filename="statement.html"; filename*=UTF-8''statement.html`);

    // One outside it.
    await sdk.statements.importStatement({ accountId: PREVIEWED }, { statement: statementFile('OFXHEADER:100\n', 'may.qfx', 'application/x-ofx') });
    const unlisted = await app.request(path);
    expect([unlisted.status, unlisted.headers.get('content-type'), unlisted.headers.get('etag')]).toEqual([500, 'application/problem+json', null]);
  });

  test('imports a statement through the SDK, and exports its lines as CSV', async () => {
    const result = await sdk.statements.importStatement(
      { accountId: ACCOUNT, note: 'march' },
      { statement: statementFile('date,amount\n2026-03-01,-12.50\n2026-03-02,100.00\n'), receipt: new File(['%PDF'], 'receipt.pdf', { type: 'application/pdf' }) }
    );
    expect(result).toEqual({ accountId: ACCOUNT, filename: 'march.csv', lines: 2, receipt: 'receipt.pdf (4 bytes)', note: 'march' });

    // An OFX file without its receipt.
    const ofx = await sdk.statements.importStatement({ accountId: ACCOUNT }, { statement: statementFile('OFXHEADER:100\n<STMTTRN>\n', 'April 2026.OFX', 'application/x-ofx') });
    expect(ofx).toMatchObject({ filename: 'April 2026.OFX', lines: 1, receipt: null, note: null });

    const exported = await sdk.statements.exportStatement(ACCOUNT);
    expect(exported.filename).toBe('statement.csv');
    expect(exported.contentType).toBe('text/csv; charset=utf-8');
    expect(exported.blob).toBeInstanceOf(Blob);
    expect(await exported.blob.text()).toBe('date,amount\n2026-03-01,-12.50\n2026-03-02,100.00\n<STMTTRN>\n');

    // The original, under the name it was sent with: filename* carries it.
    const original = await sdk.statements.originalStatement(ACCOUNT);
    expect(original.filename).toBe('April 2026.OFX');
    expect(await original.blob.text()).toBe('OFXHEADER:100\n<STMTTRN>\n');
    const raw = await app.request(`/api/statements/${ACCOUNT}/original`);
    expect(raw.headers.get('content-disposition')).toBe(`attachment; filename="April 2026.OFX"; filename*=UTF-8''April%202026.OFX`);
    expect(raw.headers.get('content-type')).toBe('application/octet-stream');
  });

  test('refuses each file the statement field does not admit, at its field', async () => {
    const before = imported.size;
    for (const [file, validator] of [
      [statementFile('x'.repeat(1025)), 'uploadMaxBytes'],
      [statementFile('date,amount\n', 'march.csv', 'text/plain'), 'uploadContentType'],
      [statementFile('date,amount\n', 'march.xlsx'), 'uploadExtension'],
    ] as const) {
      const refused = await refusal(sdk.statements.importStatement({ accountId: '1VgEh72lXvTXkI' }, { statement: file }));
      expect(refused.status).toBe(400);
      expect(refused.body).toMatchObject({
        code: 'bad_request',
        detail: 'Request body does not match the declared input',
        details: { location: 'body', reason: 'validation failed' },
      });
      expect(Object.keys(refused.errors as object)).toEqual(['statement']);
      expect((refused.errors as Record<string, { validator: string }[]>).statement[0]!.validator).toBe(validator);
    }

    // A body without the statement, sent as the SDK's raw method sends it.
    const form = new FormData();
    form.append('data', JSON.stringify({ accountId: ACCOUNT }));
    const missing = await refusal(sdk.statements.importStatementRaw(form));
    expect(missing.errors).toEqual({ statement: [{ validator: 'required', message: 'required file' }] });

    // The JSON fields are validated as a JSON body's are, the files set aside.
    const unknown = new FormData();
    unknown.append('data', JSON.stringify({ accountId: ACCOUNT, coupon: 'x' }));
    unknown.append('statement', statementFile('date,amount\n'));
    expect((await refusal(sdk.statements.importStatementRaw(unknown))).body).toMatchObject({ details: { location: 'body', reason: 'unknown fields: coupon' } });

    expect(imported.size).toBe(before);
  });

  test('reads a body of 1000 parts and refuses one of 1001 as no multipart body, as the Go router does', async () => {
    // The data part and the statement, then notes up to parts.
    const form = (parts: number) => {
      const body = new FormData();
      body.append('data', JSON.stringify({ accountId: ACCOUNT }));
      body.append('statement', statementFile('date,amount\n'));
      for (let part = 2; part < parts; part++) body.append('note', 'x');
      return body;
    };
    expect(await sdk.statements.importStatementRaw(form(1000))).toMatchObject({ accountId: ACCOUNT, filename: 'march.csv' });
    const refused = await refusal(sdk.statements.importStatementRaw(form(1001)));
    expect([refused.status, refused.body.code, refused.body.detail]).toEqual([400, 'bad_request', 'Request body is not multipart/form-data']);
  });

  test('a router with minimum client versions turns away an SDK whose client is older, with a typed error (D143)', async () => {
    // The Go router answers the same (internal/generator/apigen/testdata/files_routes_test.go, TestClientVersions).
    const versioned = serve({ minClientVersions: parseMinClientVersions('cli=0.5.0,desktop=2.1.0') });
    const seen: (string | null)[] = [];
    const sdkOf = (client?: { name: string; version: string }) =>
      new FixtureFilesApiSDK({
        baseUrl: BASE,
        client,
        fetch: (input: RequestInfo | URL, init?: RequestInit) => {
          seen.push(new Headers(init?.headers).get('x-client-version'));
          return versioned.request(String(input), init);
        },
      });
    const before = imported.size;
    let refused: unknown;
    try {
      await sdkOf({ name: 'cli', version: '0.4.9' }).statements.importStatement({ accountId: '1VgEh72lXvTXkM' }, { statement: statementFile('date,amount\n') });
    } catch (error) {
      refused = error;
    }
    expect(refused).toBeInstanceOf(ClientTooOldError);
    expect(refused).toBeInstanceOf(ApiError);
    const tooOld = refused as { client: string; version: string; minimum: string; statusCode: number; code: string; response: Record<string, unknown> };
    expect([tooOld.client, tooOld.version, tooOld.minimum, tooOld.statusCode, tooOld.code]).toEqual(['cli', '0.4.9', '0.5.0', 426, 'client_too_old']);
    expect(tooOld.response).toMatchObject({ title: 'Upgrade Required', details: { client: 'cli', version: '0.4.9', minimum: '0.5.0' } });
    // The route never ran.
    expect(imported.size).toBe(before);

    // At its minimum, another client, and a caller that names none.
    for (const client of [{ name: 'cli', version: '0.5.0' }, { name: 'desktop', version: '3.0.0-beta.1' }, { name: 'mobile', version: '0.0.1' }, undefined]) {
      const result = await sdkOf(client).statements.importStatement({ accountId: '1VgEh72lXvTXkM' }, { statement: statementFile('date,amount\n') });
      expect(result).toMatchObject({ accountId: '1VgEh72lXvTXkM' });
    }
    expect(seen).toEqual(['cli/0.4.9', 'cli/0.5.0', 'desktop/3.0.0-beta.1', 'mobile/0.0.1', null]);
    // A download is a route as any other.
    expect((await versioned.request(`/api/statements/1VgEh72lXvTXkM/export`, { headers: { 'X-Client-Version': 'desktop/2.0.9' } })).status).toBe(426);
  });

  test('caps the whole request at its @bodyLimit, and a download refusal is a problem', async () => {
    const receipt = new File(['x'.repeat(1024 * 1024 + 1)], 'receipt.pdf');
    const capped = await refusal(sdk.statements.importStatement({ accountId: ACCOUNT }, { statement: statementFile('date,amount\n'), receipt }));
    expect(capped.status).toBe(413);
    expect(capped.body.code).toBe('payload_too_large');

    const unknownAccount = await refusal(sdk.statements.exportStatement('1VgEh72lXvTXkJ'));
    expect(unknownAccount.status).toBe(404);
    expect(unknownAccount.body.code).toBe('not_found');
  });
});
