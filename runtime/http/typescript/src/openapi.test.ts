import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { DEFAULT_OPENAPI_BASE_URL, DEFAULT_OPENAPI_VERSION, mountOpenAPI, openAPIDocument, rapidocHtml } from './openapi';

/*
The OpenAPI routes a generated TypeScript router serves (D81), as the Go
server's createOpenAPIHandler and createRapiDocHandler and the Rust
runtime's openapi_router serve them: the document with the running
service's version and server, its keys sorted and indented as Go's
MarshalIndent writes them, at GET /api/openapi.json as application/json,
and the RapiDoc page at GET /api/docs; neither with serveOpenAPI false.
*/

const RAW = '{"openapi":"3.0.3","info":{"title":"shop","version":"__OPENAPI_VERSION__"},"servers":[{"url":"__OPENAPI_BASE_URL__"}],"paths":{"/api/b":{},"/api/a":{}}}';

describe('openAPIDocument', () => {
  test('states the running version and server', () => {
    const document = JSON.parse(openAPIDocument(RAW, '2.3.4', 'https://api.example'));
    expect(document.info).toEqual({ title: 'shop', version: '2.3.4' });
    expect(document.servers).toEqual([{ url: 'https://api.example', description: 'Runtime server' }]);
    expect(Object.keys(document.paths)).toEqual(['/api/a', '/api/b']);
  });

  test("writes the keys sorted and indented by two spaces, as the Go server's MarshalIndent does", () => {
    expect(openAPIDocument('{"z":1,"info":{"title":"t"},"a":[{"y":1,"b":2}]}', '1.0.0', 'http://x')).toBe(
      '{\n  "a": [\n    {\n      "b": 2,\n      "y": 1\n    }\n  ],\n  "info": {\n    "title": "t",\n    "version": "1.0.0"\n  },\n  "servers": [\n    {\n      "description": "Runtime server",\n      "url": "http://x"\n    }\n  ],\n  "z": 1\n}'
    );
  });

  test('serves a document that does not parse as it is, and replaces an info that is no object', () => {
    expect(openAPIDocument('not json')).toBe('not json');
    expect(openAPIDocument('[1]')).toBe('[1]');
    expect(JSON.parse(openAPIDocument('{"info":7}')).info).toEqual({ version: DEFAULT_OPENAPI_VERSION });
  });
});

test('the page renders the served document and escapes its title', () => {
  const page = rapidocHtml('a<b>');
  expect(page).toContain('<title>a&lt;b&gt; API Documentation</title>');
  expect(page).toContain('spec-url="/api/openapi.json"');
  expect(page).toContain('<script type="module" src="https://unpkg.com/rapidoc/dist/rapidoc-min.js"></script>');
});

describe('mountOpenAPI', () => {
  test('serves the document and the page, with the defaults the Go server states', async () => {
    const app = new Hono();
    mountOpenAPI(app, RAW, 'shop');
    const document = await app.request('/api/openapi.json');
    expect(document.status).toBe(200);
    expect(document.headers.get('content-type')).toBe('application/json');
    const body = await document.json();
    expect(body.info.version).toBe(DEFAULT_OPENAPI_VERSION);
    expect(body.servers[0].url).toBe(DEFAULT_OPENAPI_BASE_URL);
    const page = await app.request('/api/docs');
    expect(page.status).toBe(200);
    expect(page.headers.get('content-type')).toBe('text/html; charset=utf-8');
    expect(await page.text()).toContain('<rapi-doc');
  });

  test('states the options, an empty one being the default', async () => {
    const app = new Hono();
    mountOpenAPI(app, RAW, 'shop', { openAPIVersion: '3.1.0', openAPIBaseURL: '' });
    const body = await (await app.request('/api/openapi.json')).json();
    expect(body.info.version).toBe('3.1.0');
    expect(body.servers[0].url).toBe(DEFAULT_OPENAPI_BASE_URL);
  });

  test('serves neither route with serveOpenAPI false', async () => {
    const app = new Hono();
    mountOpenAPI(app, RAW, 'shop', { serveOpenAPI: false });
    expect((await app.request('/api/openapi.json')).status).toBe(404);
    expect((await app.request('/api/docs')).status).toBe(404);
  });
});
