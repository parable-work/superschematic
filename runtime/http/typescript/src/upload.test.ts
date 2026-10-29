import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { BODY_NOT_MULTIPART, checkUpload, uploadBodyLimitBytes, type OperationSpec, type OperationUpload } from './index';
import { mountOperation, type DecodedRequest } from './hono';

/*
A multipart input (D81) through the Hono adapter: the files first, each
against its field, then the JSON fields in the data part, validated as a
JSON body's are with the file fields' errors set aside; a JSON body with no
file; and the body cap, the files' limits beside the JSON cap unless the
operation declares @bodyLimit.
*/

const statement: OperationUpload = {
  name: 'statement',
  required: true,
  maxBytes: 64,
  contentTypes: ['text/csv', 'application/x-ofx'],
  extensions: ['.csv', '.ofx'],
};
const receipt: OperationUpload = { name: 'receipt', required: false, maxBytes: 2 * 1024 * 1024, contentTypes: [], extensions: [] };

/** As a generated strict parser refuses: unknown keys, then the rules, with field errors. */
function parseImport(value: unknown): unknown {
  const fields = value as Record<string, unknown>;
  const unknown = Object.keys(fields).filter(key => !['accountId', 'statement', 'receipt', 'note'].includes(key));
  if (unknown.length > 0) {
    throw Object.assign(new Error('unknown'), {
      reason: `unknown fields: ${unknown.join(', ')}`,
      errors: Object.fromEntries(unknown.map(key => [key, [{ validator: 'unknown', message: 'unknown field' }]])),
    });
  }
  const errors: Record<string, { validator: string; message: string }[]> = {};
  if (typeof fields.accountId !== 'string') errors.accountId = [{ validator: 'required', message: 'required field' }];
  if (typeof fields.statement !== 'string') errors.statement = [{ validator: 'required', message: 'required field' }];
  if (typeof fields.note === 'string' && fields.note.length > 5) errors.note = [{ validator: 'maxLength', message: 'too long' }];
  if (Object.keys(errors).length > 0) throw Object.assign(new Error('validation failed'), { reason: 'validation failed', errors });
  return { ...fields, parsed: true };
}

const importStatement: OperationSpec = {
  name: 'importStatement',
  namespace: 'statements',
  method: 'POST',
  path: '/api/statements',
  pathParams: [],
  queryParams: [],
  bodyParams: [],
  input: { parse: parseImport, fromJSON: value => ({ ...(value as object), converted: true }), required: true },
  uploads: [statement, receipt],
  auth: { public: true, required: false, permissions: [] },
  manual: false,
};

function serve(spec: OperationSpec = importStatement, options: { bodyLimitBytes?: number } = {}) {
  const seen: DecodedRequest[] = [];
  const app = new Hono();
  mountOperation(
    app,
    spec,
    async (_ctx, request) => {
      seen.push(request);
      return { files: Object.keys(request.files ?? {}) };
    },
    options
  );
  return { app, seen };
}

function form(parts: Record<string, string | File>): FormData {
  const body = new FormData();
  for (const [name, value] of Object.entries(parts)) body.append(name, value);
  return body;
}

const csv = (name = 'march.csv', type = 'text/csv', text = 'date,amount\n') => new File([text], name, { type });

async function post(app: Hono, body: BodyInit, headers: Record<string, string> = {}) {
  const response = await app.request('/api/statements', { method: 'POST', body, headers });
  return { response, body: await response.json() };
}

describe('a multipart input', () => {
  test('hands the implementation its files and its JSON fields, converted', async () => {
    const { app, seen } = serve();
    const { response, body } = await post(app, form({ data: JSON.stringify({ accountId: 'a1', note: 'march' }), statement: csv(), receipt: new File(['x'], 'r.pdf') }));
    expect(response.status).toBe(200);
    expect(body.data).toEqual({ files: ['statement', 'receipt'] });
    expect(seen[0]!.input).toEqual({ accountId: 'a1', note: 'march', converted: true });
    expect(seen[0]!.files!.statement!.name).toBe('march.csv');
    expect(await seen[0]!.files!.statement!.text()).toBe('date,amount\n');
  });

  test('admits a media type with parameters and an extension in another case', async () => {
    const { app } = serve();
    const { response } = await post(app, form({ data: '{"accountId":"a1"}', statement: csv('MARCH.CSV', 'text/csv; charset=utf-8') }));
    expect(response.status).toBe(200);
  });

  test('refuses each file its field does not admit, at its field, before the JSON fields', async () => {
    const { app, seen } = serve();
    for (const [file, validator] of [
      [undefined, 'required'],
      [csv('march.csv', 'text/csv', 'x'.repeat(65)), 'uploadMaxBytes'],
      [csv('march.csv', 'text/plain'), 'uploadContentType'],
      [csv('march.txt'), 'uploadExtension'],
    ] as const) {
      const parts: Record<string, string | File> = { data: '{"note":"far too long"}' };
      if (file) parts.statement = file;
      const { response, body } = await post(app, form(parts));
      expect(response.status).toBe(400);
      expect(body.code).toBe('bad_request');
      expect(body.detail).toBe('Request body does not match the declared input');
      expect(body.details).toEqual({ location: 'body', reason: 'validation failed' });
      expect(Object.keys(body.errors)).toEqual(['statement']);
      expect(body.errors.statement[0].validator).toBe(validator);
    }
    expect(seen).toHaveLength(0);
  });

  test('reads a text part named as a file field as no file', async () => {
    const { app } = serve();
    const { body } = await post(app, form({ data: '{"accountId":"a1"}', statement: 'not a file' }));
    expect(body.errors).toEqual({ statement: [{ validator: 'required', message: 'required file' }] });
  });

  test('validates the JSON fields as a JSON body is validated, the file fields set aside', async () => {
    const { app } = serve();
    const refused = await post(app, form({ data: '{"note":"far too long"}', statement: csv() }));
    expect(refused.response.status).toBe(400);
    expect(refused.body.details).toEqual({ location: 'body', reason: 'validation failed' });
    expect(refused.body.errors).toEqual({ accountId: [{ validator: 'required', message: 'required field' }], note: [{ validator: 'maxLength', message: 'too long' }] });

    const unknown = await post(app, form({ data: '{"accountId":"a1","coupon":"x"}', statement: csv() }));
    expect(unknown.body.details).toEqual({ location: 'body', reason: 'unknown fields: coupon' });

    const notJSON = await post(app, form({ data: '{', statement: csv() }));
    expect(notJSON.body.detail).toBe('Request body is not valid JSON');

    // No data part: each JSON field is absent.
    const noData = await post(app, form({ statement: csv() }));
    expect(noData.body.errors).toEqual({ accountId: [{ validator: 'required', message: 'required field' }] });
  });

  test('reads a JSON body as an input with no file', async () => {
    const optional: OperationSpec = { ...importStatement, uploads: [{ ...statement, required: false }, receipt] };
    const { app, seen } = serve(optional);
    const { response } = await post(app, JSON.stringify({ accountId: 'a1' }), { 'content-type': 'application/json' });
    expect(response.status).toBe(200);
    expect(seen[0]!.files).toEqual({});
    expect(seen[0]!.input).toEqual({ accountId: 'a1', converted: true });

    // A required file is missing from a JSON body too.
    const refused = await post(serve().app, JSON.stringify({ accountId: 'a1' }), { 'content-type': 'application/json' });
    expect(refused.body.errors).toEqual({ statement: [{ validator: 'required', message: 'required file' }] });
  });

  test('refuses a body that is neither multipart/form-data nor JSON', async () => {
    const { app } = serve();
    for (const headers of [{ 'content-type': 'text/plain' }, { 'content-type': 'multipart/form-data; boundary=x' }]) {
      const { response, body } = await post(app, 'hello', headers);
      expect(response.status).toBe(400);
      expect(body).toMatchObject({ code: 'bad_request', detail: BODY_NOT_MULTIPART });
    }
  });

  test('caps the body by its files and the JSON cap, or by @bodyLimit', async () => {
    expect(uploadBodyLimitBytes([statement, receipt], 1024)).toBe(64 + 2 * 1024 * 1024 + 1024);
    // A receipt past the JSON cap, within its own limit.
    const big = new File(['x'.repeat(1024 * 1024 + 10)], 'r.pdf');
    const { response } = await post(serve().app, form({ data: '{"accountId":"a1"}', statement: csv(), receipt: big }));
    expect(response.status).toBe(200);

    const capped = await post(serve({ ...importStatement, bodyLimitBytes: 1024 * 1024 }).app, form({ data: '{"accountId":"a1"}', statement: csv(), receipt: big }));
    expect(capped.response.status).toBe(413);
    expect(capped.body.code).toBe('payload_too_large');
  });
});

describe('checkUpload', () => {
  test('names the rule a file breaks', () => {
    expect(checkUpload(receipt, undefined)).toBeUndefined();
    expect(checkUpload(statement, undefined)).toEqual({ validator: 'required', message: 'required file' });
    expect(checkUpload(statement, csv('a.ofx', 'application/x-ofx'))).toBeUndefined();
    expect(checkUpload(statement, csv('a.csv', 'application/octet-stream'))?.validator).toBe('uploadContentType');
    // A part without a Content-Type is application/octet-stream (RFC 7578).
    expect(checkUpload({ ...statement, contentTypes: ['application/octet-stream'] }, csv('a.csv', ''))).toBeUndefined();
  });
});
