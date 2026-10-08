/*
A typed client for the engine's HTTP API (runtime/engine/README.md,
"HTTP"), grouped as the engine's own calls are: namespaces, schemas,
instances, events and behaviors, beside the tools document and the
search across a namespace's schemas. Each call unwraps the success envelope, sends the
sequence a write expects as If-Match and its preconditions in the
Preconditions header, and turns a refusal into an EngineProblem
(errors.ts). Credentials and the retry rule are the transport's.
*/

import { EngineTransportError } from './errors.js';
import { EventSubscription, eventQuery, type EventFilters, type SubscribeOptions } from './stream.js';
import { Transport, seqOf, type CallOptions, type EndUserAuth, type FetchLike, type RequestSpec, type ServiceCredential } from './transport.js';
import type {
  BehaviorFieldsJSON,
  BehaviorDocument,
  BehaviorSummary,
  DescribeDocument,
  EventPage,
  Instance,
  InstancePage,
  JSONObject,
  NamespaceRecord,
  Preconditions,
  PublishResult,
  SchemaSummary,
  SchemaVersion,
  SearchPage,
  SearchParams,
  ToolManifest,
} from './types.js';

export interface EngineClientOptions {
  /** The URL the engine's app is mounted at, such as `https://jobs.internal/api`; a relative one (`/api`) in a browser. */
  readonly baseUrl: string;
  /** The namespace a call uses unless it names another; `default`. */
  readonly namespace?: string;
  /** The end user's credentials. */
  readonly auth?: EndUserAuth;
  /** The calling service's credential (D37). */
  readonly serviceCredential?: ServiceCredential;
  /** The fetch to send with; globalThis.fetch by default. */
  readonly fetch?: FetchLike;
  /** Milliseconds a call may take, 30000 by default, 0 for none; the event stream's covers its headers only. */
  readonly timeoutMs?: number;
  /** Headers sent on every request. */
  readonly headers?: Readonly<Record<string, string>>;
}

/** A write's options: the sequence it expects (If-Match) and its preconditions (the Preconditions header). */
export interface WriteOptions extends CallOptions {
  /** The instance's sequence the caller last read; a write to an instance that has moved on is 412 `seq_mismatch`. */
  readonly expectedSeq?: number;
  /** Entries by behavior name, such as `{ Lease: { token: 7 } }`. */
  readonly preconditions?: Preconditions;
}

export interface CreateOptions extends CallOptions {
  /** The instance's id; the engine makes one when absent. */
  readonly id?: string;
  /** The parameters the create gives the type's behaviors, by behavior name. */
  readonly behaviors?: Readonly<Record<string, unknown>>;
}

export interface ListOptions extends CallOptions {
  readonly limit?: number;
  readonly cursor?: string;
  /**
   * Field values the instances hold, by field: an own field by its key, a
   * behavior's by its qualified name (`Workflow.status`); a value, null
   * for none, or a list any of which they hold; sent as a JSON object in
   * the `where` query parameter.
   * A filtered page can hold fewer instances than limit while next is not
   * null.
   */
  readonly where?: Readonly<Record<string, unknown>>;
}

export interface ReadEventsOptions extends EventFilters, CallOptions {
  /**
   * Events after this cursor; `head` for an empty page whose next is the
   * log's last event. Absent or 0, the start of the log: after retention,
   * the oldest event it kept. A cursor retention has pruned past is 410
   * `cursor_expired`, whose problem carries the floor and the head.
   */
  readonly after?: number | 'head';
  /** How many events to scan, 50 by default and at most 500. */
  readonly limit?: number;
}

/** The outcome of an instance operation: its result, and the instance's sequence after it. */
export interface OperationOutcome<R = unknown> {
  readonly result: R;
  readonly seq: number;
}

export const DEFAULT_NAMESPACE = 'default';

const segment = encodeURIComponent;

/** The engine's HTTP API, typed. */
export class EngineClient {
  readonly namespaces: NamespaceCalls;
  readonly schemas: SchemaCalls;
  readonly instances: InstanceCalls;
  readonly events: EventCalls;
  readonly behaviors: BehaviorCalls;
  /** The namespace calls use unless they name another. */
  readonly namespace: string;
  private readonly transport: Transport;
  private readonly scope: Scope;

  constructor(options: EngineClientOptions) {
    this.transport = new Transport(options);
    this.namespace = options.namespace ?? DEFAULT_NAMESPACE;
    this.scope = new Scope(this.transport, this.namespace);
    this.namespaces = new NamespaceCalls(this.scope);
    this.schemas = new SchemaCalls(this.scope);
    this.instances = new InstanceCalls(this.scope);
    this.events = new EventCalls(this.scope);
    this.behaviors = new BehaviorCalls(this.scope);
  }

  /** setToken replaces the end user's token the client holds. */
  setToken(token: string): void {
    this.transport.setToken(token);
  }

  /** clearToken drops the end user's token the client holds; `auth.getToken` answers again. */
  clearToken(): void {
    this.transport.clearToken();
  }

  /** tools returns the namespace's tools document, `tools/schema.json`'s shape. */
  tools(options: CallOptions = {}): Promise<ToolManifest> {
    return this.scope.data({ method: 'GET', path: `${this.scope.ns(options)}/tools`, options });
  }

  /** search searches every schema of the namespace that composes Search, and merges the hits. */
  search(params: SearchParams, options: CallOptions = {}): Promise<SearchPage> {
    return this.scope.data({ method: 'POST', path: `${this.scope.ns(options)}/search`, body: params, options });
  }
}

// The transport and the client's namespace, which each group of calls shares.
class Scope {
  constructor(
    readonly transport: Transport,
    private readonly namespace: string
  ) {}

  ns(options: CallOptions): string {
    return `/namespaces/${segment(options.namespace ?? this.namespace)}`;
  }

  schema(options: CallOptions, name: string): string {
    return `${this.ns(options)}/schemas/${segment(name)}`;
  }

  instance(options: CallOptions, schema: string, id: string): string {
    return `${this.schema(options, schema)}/instances/${segment(id)}`;
  }

  async data<T>(spec: RequestSpec): Promise<T> {
    return (await this.transport.json<T>(spec)).data;
  }
}

/**
 * The namespaces themselves: list, read, create, archive and unarchive,
 * as the engine's access policy allows (`manage`). Each names the
 * namespace it acts on, whatever the client's namespace is.
 */
export class NamespaceCalls {
  constructor(private readonly scope: Scope) {}

  /** list returns the namespaces the caller may list. */
  list(options: CallOptions = {}): Promise<NamespaceRecord[]> {
    return this.scope.data({ method: 'GET', path: '/namespaces', options });
  }

  /** get reads one namespace; one there is not is 404 `unknown_namespace`. */
  get(name: string, options: CallOptions = {}): Promise<NamespaceRecord> {
    return this.scope.data({ method: 'GET', path: `/namespaces/${segment(name)}`, options });
  }

  /** create makes a namespace; a name that is one already is 409 `conflict`. */
  create(name: string, options: CallOptions = {}): Promise<NamespaceRecord> {
    return this.scope.data({ method: 'POST', path: '/namespaces', body: { name }, options });
  }

  /** archive archives a namespace a create made: it is read as it was, and every write is 409 `namespace_archived`. */
  archive(name: string, options: CallOptions = {}): Promise<NamespaceRecord> {
    return this.scope.data({ method: 'POST', path: `/namespaces/${segment(name)}/archive`, options });
  }

  /** unarchive lets an archived namespace be written again. */
  unarchive(name: string, options: CallOptions = {}): Promise<NamespaceRecord> {
    return this.scope.data({ method: 'POST', path: `/namespaces/${segment(name)}/unarchive`, options });
  }
}

/** Schemas: list, read, define, publish and describe. */
export class SchemaCalls {
  constructor(private readonly scope: Scope) {}

  /** list returns the names the namespace reaches. */
  list(options: CallOptions = {}): Promise<SchemaSummary[]> {
    return this.scope.data({ method: 'GET', path: `${this.scope.ns(options)}/schemas`, options });
  }

  /** live returns the schema's live version. */
  live(name: string, options: CallOptions = {}): Promise<SchemaVersion> {
    return this.scope.data({ method: 'GET', path: this.scope.schema(options, name), options });
  }

  /** draft returns the schema's draft. */
  draft(name: string, options: CallOptions = {}): Promise<SchemaVersion> {
    return this.scope.data({ method: 'GET', path: `${this.scope.schema(options, name)}/draft`, options });
  }

  /** version returns one published version. */
  version(name: string, version: number, options: CallOptions = {}): Promise<SchemaVersion> {
    return this.scope.data({ method: 'GET', path: `${this.scope.schema(options, name)}/versions/${segment(String(version))}`, options });
  }

  /** define stores a document as its name's draft. */
  define(document: JSONObject, options: CallOptions = {}): Promise<SchemaVersion> {
    return this.scope.data({ method: 'POST', path: `${this.scope.ns(options)}/schemas`, body: document, options });
  }

  /** publish makes the draft the next live version. */
  publish(name: string, options: CallOptions = {}): Promise<PublishResult> {
    return this.scope.data({ method: 'POST', path: `${this.scope.schema(options, name)}/publish`, options });
  }

  /** describe returns the live version's describe document: the instance's JSON Schema, the behaviors and every operation. */
  describe(name: string, options: CallOptions = {}): Promise<DescribeDocument> {
    return this.scope.data({ method: 'GET', path: `${this.scope.schema(options, name)}/describe`, options });
  }
}

/** Instances, their operations and a schema's operations. */
export class InstanceCalls {
  constructor(private readonly scope: Scope) {}

  /** create stores a new instance, with its behaviors' create parameters. */
  create<T = JSONObject, B = BehaviorFieldsJSON>(schema: string, data: JSONObject, options: CreateOptions = {}): Promise<Instance<T, B>> {
    return this.scope.data({
      method: 'POST',
      path: `${this.scope.schema(options, schema)}/instances`,
      body: { ...(options.id !== undefined ? { id: options.id } : {}), data, ...(options.behaviors !== undefined ? { behaviors: options.behaviors } : {}) },
      options,
    });
  }

  /** get reads an instance; one that does not exist is 404 `not_found`. */
  get<T = JSONObject, B = BehaviorFieldsJSON>(schema: string, id: string, options: CallOptions = {}): Promise<Instance<T, B>> {
    return this.scope.data({ method: 'GET', path: this.scope.instance(options, schema, id), options });
  }

  /** list returns a page of instances in creation order. */
  list<T = JSONObject, B = BehaviorFieldsJSON>(schema: string, options: ListOptions = {}): Promise<InstancePage<T, B>> {
    const query: Array<[string, string]> = [];
    if (options.limit !== undefined) {
      query.push(['limit', String(options.limit)]);
    }
    if (options.cursor !== undefined) {
      query.push(['cursor', options.cursor]);
    }
    if (options.where !== undefined) {
      query.push(['where', JSON.stringify(options.where)]);
    }
    return this.scope.data({ method: 'GET', path: `${this.scope.schema(options, schema)}/instances`, query, options });
  }

  /**
   * lookup reads the instance whose unique fields hold a key's values,
   * `{ slug: 'openai/gpt-5' }`, sent as a JSON object in the query, so a
   * value may hold a slash; none is 404 `not_found`, as for get.
   */
  lookup<T = JSONObject, B = BehaviorFieldsJSON>(schema: string, key: JSONObject, options: CallOptions = {}): Promise<Instance<T, B>> {
    return this.scope.data({ method: 'GET', path: `${this.scope.schema(options, schema)}/lookup`, query: [['key', JSON.stringify(key)]], options });
  }

  /** update applies a JSON merge patch (RFC 7386) and returns the instance. */
  update<T = JSONObject, B = BehaviorFieldsJSON>(schema: string, id: string, patch: JSONObject, options: WriteOptions = {}): Promise<Instance<T, B>> {
    return this.scope.data({
      method: 'PATCH',
      path: this.scope.instance(options, schema, id),
      body: patch,
      contentType: 'application/merge-patch+json',
      headers: writeHeaders(options),
      options,
    });
  }

  /** delete removes an instance; one that does not exist is 404 `not_found`. */
  async delete(schema: string, id: string, options: WriteOptions = {}): Promise<void> {
    await this.scope.data({ method: 'DELETE', path: this.scope.instance(options, schema, id), headers: writeHeaders(options), options });
  }

  /** invoke calls a behavior operation on an instance and returns its result. */
  async invoke<R = unknown>(schema: string, id: string, operation: string, params: JSONObject = {}, options: WriteOptions = {}): Promise<R> {
    return (await this.operate<R>(schema, id, operation, params, options)).result;
  }

  /** operate calls a behavior operation on an instance and returns its result and the instance's sequence after it. */
  async operate<R = unknown>(schema: string, id: string, operation: string, params: JSONObject = {}, options: WriteOptions = {}): Promise<OperationOutcome<R>> {
    const { data, response } = await this.scope.transport.json<R>({
      method: 'POST',
      path: `${this.scope.instance(options, schema, id)}/operations/${segment(operation)}`,
      body: params,
      headers: writeHeaders(options),
      options,
    });
    const seq = seqOf(response);
    if (seq === undefined) {
      throw new EngineTransportError(`operation ${operation} of ${schema} ${id} answered without the instance's sequence as its ETag`);
    }
    return { result: data, seq };
  }

  /** invokeSchema calls a schema-level behavior operation, such as Queue's `claimNext`. */
  invokeSchema<R = unknown>(schema: string, operation: string, params: JSONObject = {}, options: CallOptions = {}): Promise<R> {
    return this.scope.data({ method: 'POST', path: `${this.scope.schema(options, schema)}/operations/${segment(operation)}`, body: params, options });
  }
}

/** The event log: pages, the head and the stream. */
export class EventCalls {
  constructor(private readonly scope: Scope) {}

  /** read returns a page of the log after a cursor, filtered. */
  read(options: ReadEventsOptions = {}): Promise<EventPage> {
    return this.scope.data({ method: 'GET', path: `${this.scope.ns(options)}/events`, query: eventQuery(options, options.after, options.limit), options });
  }

  /** head returns the cursor of the log's last event, which a read or a subscription starts after to see only what commits next. */
  async head(options: CallOptions = {}): Promise<number> {
    return (await this.read({ ...options, after: 'head' })).next;
  }

  /**
   * subscribe opens the event stream: iterate the subscription for each
   * event and a `ready` per connection; it resumes by Last-Event-ID when
   * the connection drops.
   */
  subscribe(options: SubscribeOptions = {}): EventSubscription {
    return new EventSubscription(this.scope.transport, `${this.scope.ns(options)}/events`, options);
  }
}

/** The behaviors the engine runs. */
export class BehaviorCalls {
  constructor(private readonly scope: Scope) {}

  /** list returns a summary of each behavior the engine runs. */
  list(options: CallOptions = {}): Promise<BehaviorSummary[]> {
    return this.scope.data({ method: 'GET', path: '/behaviors', options });
  }

  /** describe returns one behavior's declaration, with its config's JSON Schema. */
  describe(name: string, options: CallOptions = {}): Promise<BehaviorDocument> {
    return this.scope.data({ method: 'GET', path: `/behaviors/${segment(name)}`, options });
  }
}

function writeHeaders(options: WriteOptions): Record<string, string> {
  const headers: Record<string, string> = {};
  if (options.expectedSeq !== undefined) {
    if (!Number.isInteger(options.expectedSeq) || options.expectedSeq < 0) {
      throw new TypeError(`expectedSeq is an instance's sequence, a non-negative integer, got ${String(options.expectedSeq)}`);
    }
    headers['if-match'] = `"${options.expectedSeq}"`;
  }
  if (options.preconditions !== undefined) {
    headers.preconditions = JSON.stringify(options.preconditions);
  }
  return headers;
}
