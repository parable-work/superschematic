import type { Env, Hono } from 'hono';

/*
Serving a generated router's OpenAPI document and its documentation page,
as the generated Go and Rust servers serve them (D81): GET /api/openapi.json
answers the document the build wrote (openapi.json, which the generated
package embeds as a string, so nothing reads a file at request time and it
serves on Bun, Node.js and Workers alike), with info.version and the
server list the running service states in place of the build's
placeholders, and GET /api/docs a RapiDoc page that renders it. Both are on
unless the router's options turn them off.
*/

/** The path the document is served at, the Go and Rust servers'. */
export const OPENAPI_PATH = '/api/openapi.json';

/** The path its RapiDoc page is served at. */
export const OPENAPI_DOCS_PATH = '/api/docs';

/** The info.version a served document states when the service sets none, the Go server's default. */
export const DEFAULT_OPENAPI_VERSION = '1.0.0';

/** The server URL a served document states when the service sets none, the Go server's default. */
export const DEFAULT_OPENAPI_BASE_URL = 'http://localhost:8080';

/** What a generated router serves beside its routes: the Go server's Config and the Rust server's RouterOptions fields. */
export interface OpenAPIOptions {
  /** Serve the document at GET /api/openapi.json and its page at GET /api/docs. On unless false, as in the Go and Rust servers. */
  readonly serveOpenAPI?: boolean;
  /** The info.version the served document states; DEFAULT_OPENAPI_VERSION when absent or empty. */
  readonly openAPIVersion?: string;
  /** The URL of the one server the served document lists; DEFAULT_OPENAPI_BASE_URL when absent or empty. */
  readonly openAPIBaseURL?: string;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** value with the keys of every object sorted, as the Go and Rust servers encode a document they parsed. */
function sortedKeys(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(sortedKeys);
  if (!isObject(value)) return value;
  const sorted: Record<string, unknown> = {};
  for (const key of Object.keys(value).sort()) sorted[key] = sortedKeys(value[key]);
  return sorted;
}

/**
 * The document a build wrote, with info.version and the server list the
 * running service states in place of the build's placeholders, indented
 * by two spaces with its keys sorted, as the Go server's
 * injectOpenAPIRuntimeValues writes it. A document that does not parse as
 * an object is served as it is.
 */
export function openAPIDocument(raw: string, version: string = DEFAULT_OPENAPI_VERSION, baseURL: string = DEFAULT_OPENAPI_BASE_URL): string {
  let document: unknown;
  try {
    document = JSON.parse(raw);
  } catch {
    return raw;
  }
  if (!isObject(document)) return raw;
  const info = isObject(document.info) ? document.info : {};
  const served = { ...document, info: { ...info, version }, servers: [{ url: baseURL, description: 'Runtime server' }] };
  return JSON.stringify(sortedKeys(served), null, 2);
}

function escapeHtml(text: string): string {
  return text.replace(/&/gu, '&amp;').replace(/</gu, '&lt;').replace(/>/gu, '&gt;').replace(/"/gu, '&quot;');
}

/** The RapiDoc page that renders /api/openapi.json, the Go and Rust servers' /api/docs, titled after the API. */
export function rapidocHtml(title: string): string {
  const name = escapeHtml(title);
  return `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>${name} API Documentation</title>
  <script type="module" src="https://unpkg.com/rapidoc/dist/rapidoc-min.js"></script>
</head>
<body>
  <rapi-doc
    spec-url="/api/openapi.json"
    theme="dark"
    bg-color="#1a1a1a"
    text-color="#f0f0f0"
    primary-color="#4a9eff"
    header-color="#1a1a1a"
    nav-bg-color="#2d2d2d"
    nav-text-color="#f0f0f0"
    nav-hover-bg-color="#3d3d3d"
    nav-hover-text-color="#ffffff"
    nav-accent-color="#4a9eff"
    render-style="read"
    show-header="true"
    allow-authentication="true"
    persist-auth="true"
    allow-server-selection="true"
    allow-api-list-style-selection="false"
    fill-request-fields-with-example="true"
    show-method-in-nav-bar="as-colored-block"
    use-path-in-nav-bar="false"
    schema-style="table"
    schema-expand-level="2"
    schema-description-expanded="true"
    default-schema-tab="model"
    response-area-height="400px"
    regular-font="'Open Sans', sans-serif"
    mono-font="'Roboto Mono', monospace"
    font-size="default"
  >
    <div slot="nav-logo" style="padding: 20px; text-align: center;">
      <h2 style="margin: 0; color: #4a9eff;">${name}</h2>
      <p style="margin: 5px 0 0 0; color: #999; font-size: 14px;">API Documentation</p>
    </div>
  </rapi-doc>
</body>
</html>`;
}

/**
 * Mounts GET /api/openapi.json, the document `raw` with the version and
 * server URL of `options`, and GET /api/docs, its RapiDoc page titled
 * `title`, on app; nothing when options.serveOpenAPI is false. The
 * document is built once, here, as the Go and Rust servers build it when
 * they mount it. A generated router's buildRouter calls it before it
 * mounts its operations, as the Go server registers these routes first.
 */
export function mountOpenAPI<E extends Env>(app: Hono<E>, raw: string, title: string, options: OpenAPIOptions = {}): void {
  if (options.serveOpenAPI === false) return;
  const document = openAPIDocument(raw, options.openAPIVersion || DEFAULT_OPENAPI_VERSION, options.openAPIBaseURL || DEFAULT_OPENAPI_BASE_URL);
  const page = rapidocHtml(title);
  app.get(OPENAPI_PATH, () => new Response(document, { headers: { 'content-type': 'application/json' } }));
  app.get(OPENAPI_DOCS_PATH, () => new Response(page, { headers: { 'content-type': 'text/html; charset=utf-8' } }));
}
