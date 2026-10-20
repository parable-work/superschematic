# Engine HTTP corpus

What the engine's HTTP surface answered, recorded as data, so the engine's
transport-neutral routes (D58), and each server runtime that mounts them, Go
through `httptest`, Rust through `tower::oneshot`, TypeScript through Hono,
can replay it: request and response exchanges of the routes, event-stream
transcripts and MCP's JSON-RPC exchanges. It was first recorded from the
TypeScript engine's Hono app; since the cutover (milestone 14) it is recorded
through `@superschematic/engine`'s `engineApp` and `engineMcp`, the Hono
adapters over the Rust engine's own routes (`typescript/src/native/http.ts`). It is written by the recorder that writes
`../scenarios`, from the tests that serve HTTP, and in the same format:
`../scenarios/README.md` describes files, cases, engine steps, symbols,
shared values and the values JSON cannot hold, and this file adds what
serving HTTP needs. `typescript/test/corpus.test.ts` fails when a committed
file is not what the recorder writes; `UPDATE_ENGINE_CORPUS=1` rewrites it.

| File | Recorded from | What it covers |
|---|---|---|
| `http.json` | `test/http.test.ts` | Every route: envelopes, problem documents and their statuses, authentication and access, service callers (D37), `If-Match`, the merge patch media type, paging, event filters, namespaces and the behavior catalog. |
| `operations.json` | `test/operations.test.ts` | Behavior operations and schema-level operations over HTTP, their ETags and refusals, the tools route and its filter, the core's behaviors through their routes. |
| `stream.json` | `test/stream.test.ts` | The event stream: replay in pages, `ready`, live events, filters and the ids that carry the cursor past what they drop, resume from `Last-Event-ID`, `after=head`, a refused stream, 410 for a cursor retention passed, and a stream that ends. |
| `mcp.json` | `test/mcp.test.ts` | The MCP endpoint: `initialize`, `tools/list`, `tools/call` and its tool errors, an unknown tool, the 2026-07-28 revision, service callers, the core's operations as tools. |

`test/identity.test.ts` (the user model's session routes, D50) is left out:
the plan keeps `/auth` host code, and its session ids come from a random
source the recorder does not seed. The client's tests drive the same routes.

A file holds `cases` where a scenario file holds `scenarios`; a case's
members are a scenario's.

## Steps

Beside a scenario's engine steps (`open` and calls the test made on the
engine directly, to set up or to check what a request did), a case has three
more kinds.

### App

```json
{"app": "a1", "kind": "engine", "options": {"authenticate": "host", "stream": {"pageSize": 2, "heartbeatMs": 60000}}}
{"app": "a1", "kind": "mount", "mounts": [{"prefix": "/api", "kind": "engine", "options": {"authenticate": "host"}}, {"prefix": "/api", "kind": "mcp", "options": {"authenticate": "host"}}]}
```

An app the test serves over the engine: `engine` (`engineApp`'s routes),
`mcp` (`engineMcp`'s endpoint), or `mount`, a parent app that mounts them at
the prefixes `mounts` names, in order. `engine` names the engine it serves
when the case opened more than one. `options` are the options the test built
it with: `authenticate` and `authenticateService`, the host's, are `"host"`,
since each exchange records the caller they established; any other function
(a `tools` filter, an `onError`) is `"custom"` and marks the case `js`
(`app.tools`, `app.onError`). `stream.heartbeatMs` is the heartbeat the case
ran with. Paths in exchanges are the parent app's, prefix and all.

### Exchange

```json
{"request": {"method": "POST", "path": "/namespaces/default/schemas/Order/instances", "headers": {"authorization": "Bearer bob", "content-type": "application/json"}, "body": {"id": "o6", "data": {"title": "o6"}}, "caller": {"principal": {"subject": "bob", "permissions": []}}},
 "response": {"status": 201, "headers": {"content-type": "application/json", "etag": "\"1\"", "location": "/namespaces/default/schemas/Order/instances/o6", "x-request-id": "{{id:3}}"}, "body": {"data": {...}, "meta": {"requestId": "{{id:3}}"}}}}
```

| Member | What it holds |
|---|---|
| `request.method`, `request.path` | As the app received them. |
| `request.query` | The query string without `?`, as sent; absent when none. |
| `request.headers` | Those the engine reads, lowercase, as sent: `accept`, `authorization`, `content-type`, `if-match`, `last-event-id`, `mcp-protocol-version`, `mcp-session-id`, `origin`, `preconditions`, `service-authorization`, `x-request-id`. |
| `request.body` | A JSON body parsed; any other as `{"text": "..."}`; absent when none. |
| `request.caller` | Who the host established, which the engine's routes take as given: `principal`, what the deployment's Authenticator returned (`null` for none); `service`, what the service authenticator verified (D37; `null` for none); `serviceRefused`, `{status, code}` of the service authenticator's refusal. Absent when the request never reached authentication. The host keeps authentication, and the engine the rule for a service that brings no end user (`typescript/src/http/callers.ts`): with `service` and no `authorization` header, the service stands in. |
| `response.status` | The status. |
| `response.headers` | Those that matter, lowercase: `allow`, `cache-control`, `content-type`, `etag`, `location`, `mcp-session-id`, `retry-after`, `www-authenticate`, `x-request-id`. |
| `response.body` | A JSON body (the D15 envelope, an RFC 9457 problem document, a JSON-RPC message) parsed; any other as `{"text": "..."}`; absent when empty or a stream. |
| `stream` | For a response whose body is a stream (`text/event-stream`): the stream's name, `s1`, `s2`, and on. |
| `app` | The app served, by its name, when the case served more than one. |
| `now` | As an engine step's, for what the request read of a fixed clock. |

A request id the host made is drawn from the seeded generator, so it is a
symbol, `{{id:N}}`, in the `x-request-id` header and the body's `requestId`.

### Stream

```json
{"stream": "s1", "frames": [{"comment": "open"}, {"id": "1", "data": {"cursor": 1, "kind": "define", ...}}, {"event": "ready", "id": "7", "data": {"cursor": 7}}]}
{"stream": "s1", "frames": [{"id": "8", "data": {...}}]}
{"stream": "s1", "end": true}
```

The frames a stream sent, as the client took them, after the steps before
them and before the steps after: consecutive frames with no step between them
are one step. A frame is a server-sent event's `comment`, `event`, `id` and
`data` (parsed when JSON, else `text`); a frame with only an `id` moves the
client's last event id past what a filter dropped. `{"stream": "s1", "end":
true}` is the server ending the body while the client was still there: the
engine closed, retention passed the stream's cursor, the read was refused, or
an MCP response was complete.

Two things a stream does on a timer are left out, since how many a test sees
is its timing, not the engine's: the heartbeat's `: keepalive` comments, and
the client's disconnect, which the server learns of while the test goes on.
A heartbeat that carries the cursor (an `id` frame while the stream waits) is
recorded, at the step that waited for it. A replayer ignores `keepalive`
comments, and may close a stream once no later step names it.

MCP's endpoint answers each JSON-RPC request as a stream of one `message`
event, so an MCP exchange is a request whose response is a stream: its frame's
`data` is the JSON-RPC response. A notification is answered 202 with no body.

## Replaying

As `../scenarios/README.md` says, with these:

1. An `app` step builds the app over the named engine with the options given;
   the host's authentication is the replayer's, which takes each exchange's
   `caller` as what it established.
2. An exchange sends the request (its symbols replaced) through the app with
   its caller, and compares the status, the headers of the list above (one
   the recording lacks must be absent) and the body.
3. A stream step reads the stream until it has taken the listed frames,
   leaving out `keepalive` comments, and compares them in order; `end`
   requires the body to end.
