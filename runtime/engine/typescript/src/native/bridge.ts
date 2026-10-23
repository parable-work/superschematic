/*
The bridge to behaviors written in TypeScript (D58, milestone 7b): what the
test addon, built with the `js-behaviors` cargo feature, runs a
defineBehavior object through, so the engine's tests that define behaviors
in TypeScript run against the Rust engine unchanged. No shipped addon has
it: the production build refuses such a behavior.

Each implementation is described to the addon once (describe): its
declaration as JSON text and which hooks it has, by name, with its
migrations, filters and schedules as data. The Rust engine runs it as a
dynamic behavior (runtime/engine/rust/engine/src/behaviors/dynamic.rs):
composed, checked and called inside the call's transaction, on this
thread. A hook call reaches answer() through the addon's host function
with the hook, its arguments and the context's generation, and answer()
builds the context the TypeScript engine would have given, a frozen object
whose methods call back into the addon (bridgeCall) with that generation.
The addon finds the Rust context the generation names while the hook runs,
and refuses it (context_expired) once the hook returns.

What a hook throws crosses back as data: a BehaviorVetoError, a
BehaviorConfigError, an EngineError or a BehaviorError as the Rust
engine's own refusal; an error the Rust engine threw into the hook as the
very error it was (its token); anything else parked here under a token,
which the Rust engine carries as its Host error and this facade throws
again, the same value, where the call returns. Parked values live until
the outermost call returns (settle).

A config parseConfig returns stays here, deep-frozen, under a token the
Rust engine holds; the addon lists the tokens it lets go with the next
hook call. Every hook's answer is checked as the TypeScript engine checks
it: a promise refused (behavior code is synchronous, D16), a result that
is no JSON refused at its path, a veto, an issue list or a guidance of the
wrong shape refused with the TypeScript engine's words.
*/

import { BehaviorConfigError, BehaviorError, BehaviorVetoError, EngineError } from '../errors.js';
import type { AnyBehaviorImplementation } from '../testing/behavior.js';
import { carry } from './carry.js';
import { deepFreeze, isPlainObject, jsonCopy } from './json.js';

/** The addon's entry for a context's method: generation, method, arguments as JSON; the result as JSON, '' for undefined. */
export type BridgeCall = (generation: number, method: string, args: string) => string;

/** What a hook threw, as the addon reads it back. */
type Threw =
  | { rust: number }
  | { veto: { behavior: string; action: string; schema: string; id: string; reason: string; code?: string; details?: unknown } }
  | { config: string }
  | { engine: { code: string; message: string } }
  | { behavior: { behavior: string; message: string } }
  | { host: { token: number; message: string; described: string } };

/** A hook call, as the addon asks it. */
interface HookRequest {
  /** The implementation's index (describe). */
  b: number;
  hook: string;
  /** The operation, field or schedule. */
  name?: string;
  /** The context's generation; 0 for a hook with none. */
  gen: number;
  /** The context's fixed members. */
  cx?: Record<string, unknown>;
  /** The config's token. */
  config?: number;
  args?: unknown[];
  /** Config tokens the Rust engine let go. */
  release?: number[];
}

// A symbol on an error a context method threw: the token of the Rust
// error it stands for, which goes back to the addon as that error.
const RUST_TOKEN = Symbol('superschematic.engine.rustToken');

/** What describe tells the addon of an implementation. */
export interface BehaviorDescriptor {
  id: number;
  declaration: string;
  hooks: Record<string, unknown>;
  problems: string[];
}

const HOOKS = [
  'parseConfig',
  'configChange',
  'afterConfigChange',
  'initialize',
  'guard',
  'validate',
  'checkedTypes',
  'instanceSchema',
  'guidance',
  'createParamsSchema',
  'afterChange',
  'guardReference',
  'afterReferenceChange',
] as const;

const COLUMN_TYPES = ['integer', 'real', 'text', 'blob', 'any'];

/** The bridge for one engine: its implementations, their configs and what their hooks threw. */
export class Bridge {
  private readonly implementations: AnyBehaviorImplementation[] = [];
  private readonly configs = new Map<number, unknown>();
  private readonly parked = new Map<number, unknown>();
  private readonly tokens = new WeakMap<object, number>();
  private nextToken = 1;
  private native: BridgeCall | undefined;

  /** connect gives the bridge the addon's entry for context methods. */
  connect(native: BridgeCall): void {
    this.native = native;
  }

  /** describe registers an implementation with the bridge and says what it has, for the addon. */
  describe(implementation: AnyBehaviorImplementation): BehaviorDescriptor {
    if (typeof implementation !== 'object' || implementation === null) {
      throw new TypeError('a behavior implementation is an object with its declaration');
    }
    const id = this.implementations.length;
    this.implementations.push(implementation);
    const problems: string[] = [];
    const names = (key: 'operations' | 'schemaOperations' | 'fields'): string[] => {
      const value: unknown = implementation[key];
      if (value === undefined) {
        return [];
      }
      if (typeof value !== 'object' || value === null) {
        problems.push(`${key} is an object of functions by name`);
        return [];
      }
      for (const [name, fn] of Object.entries(value)) {
        if (typeof fn !== 'function') {
          problems.push(`${key}.${name} is a function`);
        }
      }
      return Object.keys(value);
    };
    const operations = names('operations');
    const schemaOperations = names('schemaOperations');
    const fields = names('fields');
    const hooks: Record<string, unknown> = { operations, schemaOperations, fields };
    for (const hook of HOOKS) {
      const value: unknown = implementation[hook];
      if (value !== undefined && typeof value !== 'function') {
        problems.push(`${hook} is a function`);
      }
      hooks[hook] = typeof value === 'function';
    }
    hooks.migrations = migrationsOf(implementation.migrations, problems);
    hooks.filters = filtersOf(implementation.filters, problems);
    const reactions: unknown = implementation.reactions;
    if (reactions !== undefined) {
      if (typeof reactions !== 'object' || reactions === null) {
        problems.push('reactions is an object with react and an optional watches');
      } else {
        const { react, watches } = reactions as { react?: unknown; watches?: unknown };
        if (typeof react !== 'function') {
          problems.push('reactions.react is a function');
        }
        if (watches !== undefined && typeof watches !== 'function') {
          problems.push('reactions.watches is a function');
        }
        hooks.reactions = typeof react === 'function';
        hooks.watches = typeof watches === 'function';
      }
    }
    hooks.schedules = schedulesOf(implementation.schedules, problems);
    const declaration = JSON.stringify(implementation.declaration) ?? 'null';
    return { id, declaration, hooks, problems };
  }

  /** The implementation an index names. */
  implementation(id: number): AnyBehaviorImplementation | undefined {
    return this.implementations[id];
  }

  /** answer runs one hook the addon asks for and writes its answer: { value } or { threw }. */
  readonly answer = (payload: string): string => {
    const request = JSON.parse(payload) as HookRequest;
    for (const token of request.release ?? []) {
      this.configs.delete(token);
    }
    this.depth += 1;
    try {
      const value = this.run(request);
      return value === undefined ? '{}' : JSON.stringify({ value: encodeValue(value) });
    } catch (error) {
      return JSON.stringify({ threw: this.threw(error) });
    } finally {
      this.depth -= 1;
    }
  };

  /** How many hooks are running on this thread now. */
  depth = 0;

  /** settle forgets what hooks threw, once the outermost call has returned. */
  settle(): void {
    this.parked.clear();
  }

  /** firstParked is the first value a hook threw since the bridge settled, if one did. */
  firstParked(): { value: unknown } | undefined {
    for (const value of this.parked.values()) {
      return { value };
    }
    return undefined;
  }

  /** parkedValue is what a hook threw under a token, if the bridge still holds it. */
  parkedValue(token: number): { value: unknown } | undefined {
    return this.parked.has(token) ? { value: this.parked.get(token) } : undefined;
  }

  /** markRust marks an error a context method threw with the token of the Rust error it stands for. */
  static markRust(error: unknown, token: unknown): void {
    if (typeof token === 'number' && typeof error === 'object' && error !== null) {
      Object.defineProperty(error, RUST_TOKEN, { value: token, enumerable: false, configurable: true });
    }
  }

  // threw is a thrown value as the addon reads it.
  private threw(error: unknown): Threw {
    if (typeof error === 'object' && error !== null && typeof (error as { [RUST_TOKEN]?: unknown })[RUST_TOKEN] === 'number') {
      return { rust: (error as { [RUST_TOKEN]: number })[RUST_TOKEN] };
    }
    if (error instanceof BehaviorVetoError) {
      if (error.vetoDetails !== undefined) {
        const copied = jsonCopy(error.vetoDetails);
        if (!('value' in copied) || !isPlainObject(copied.value)) {
          return { behavior: { behavior: error.behavior, message: "a veto's details are a JSON object" } };
        }
      }
      return { veto: vetoOf(error) };
    }
    if (error instanceof BehaviorConfigError) {
      return { config: error.message };
    }
    if (error instanceof EngineError && typeof error.code === 'string' && error.constructor === EngineError) {
      return { engine: { code: error.code, message: error.message } };
    }
    if (error instanceof BehaviorError && error.constructor === BehaviorError) {
      const prefix = `behavior ${error.behavior}: `;
      return { behavior: { behavior: error.behavior, message: error.message.startsWith(prefix) ? error.message.slice(prefix.length) : error.message } };
    }
    let token = typeof error === 'object' && error !== null ? this.tokens.get(error) : undefined;
    if (token === undefined || !this.parked.has(token)) {
      token = this.nextToken;
      this.nextToken += 1;
      this.parked.set(token, error);
      if (typeof error === 'object' && error !== null) {
        this.tokens.set(error, token);
      }
    }
    return { host: { token, message: error instanceof Error ? error.message : String(error), described: described(error) } };
  }

  private config(token: number | undefined): unknown {
    return token === undefined ? undefined : this.configs.get(token);
  }

  private keep(config: unknown): number {
    const token = this.nextToken;
    this.nextToken += 1;
    this.configs.set(token, config);
    return token;
  }

  // invoke calls a context's method in the addon.
  private invoke(gen: number, method: string, args: readonly unknown[]): unknown {
    const native = this.native;
    if (native === undefined) {
      throw new Error('the behavior bridge is not connected to an addon');
    }
    let text: string;
    try {
      text = native(gen, method, JSON.stringify(carry(args.map(encodeValue))));
    } catch (error) {
      throw this.restore(error);
    }
    return text === '' ? undefined : decodeValue(JSON.parse(text));
  }

  /** restore is the error the addon threw from a context method, as the TypeScript engine throws it. */
  restore: (error: unknown) => unknown = (error) => error;

  // run runs one hook.
  private run(request: HookRequest): unknown {
    const implementation = this.implementations[request.b];
    if (implementation === undefined) {
      throw new Error(`the Rust engine asked the bridge for behavior ${request.b}, which it does not hold`);
    }
    const name = (implementation.declaration as { name?: string } | undefined)?.name ?? 'implementation';
    const args = (request.args ?? []).map(decodeValue);
    const gen = request.gen;
    const cx = request.cx ?? {};
    const config = this.config(request.config);
    switch (request.hook) {
      case 'parseConfig': {
        const [raw, target] = args as [unknown, Record<string, unknown>];
        const json = deepFreeze(raw);
        const parse = implementation.parseConfig;
        if (parse === undefined) {
          return this.keep(json);
        }
        let open = true;
        const types = Object.freeze({
          names: deepFreeze(target.typeNames as string[]),
          get: (type: string) => {
            if (!open) {
              throw new BehaviorError(name, 'ConfigTarget.types.get reads a type only while parseConfig runs');
            }
            return typeof type !== 'string' ? undefined : (deepFreeze(this.invoke(gen, 'target.types.get', [type]) ?? undefined) as never);
          },
        });
        const schemas =
          target.hasSchemas === true
            ? Object.freeze({ get: (schema: string) => deepFreeze(this.invoke(gen, 'target.schemas.get', [schema]) ?? undefined) as never })
            : undefined;
        const context = deepFreeze({
          schema: target.schema,
          type: target.type,
          fields: target.fields,
          fieldSchemas: target.fieldSchemas,
          types,
          behaviors: target.behaviors,
          behaviorFields: target.behaviorFields,
          configs: target.configs,
          ...(schemas === undefined ? {} : { schemas }),
        });
        let parsed: unknown;
        try {
          parsed = parse.call(implementation, json, context as never);
        } finally {
          open = false;
        }
        synchronous(name, 'parseConfig', parsed);
        return this.keep(deepFreeze(parsed));
      }
      case 'configChange': {
        const [before, after] = args as [number | null, number | null];
        const instances = () => this.invoke(gen, 'change.instances', []) as boolean;
        const change = Object.freeze({
          get instances(): boolean {
            return instances();
          },
        });
        const answer: unknown = implementation.configChange?.call(
          implementation,
          before === null ? undefined : this.config(before),
          after === null ? undefined : this.config(after),
          change
        );
        synchronous(name, 'configChange', answer);
        if (answer === undefined || answer === null) {
          return null;
        }
        if (typeof answer !== 'string' || answer === '') {
          throw new BehaviorError(name, 'configChange returns a reason (a non-empty string) to refuse, or undefined to allow');
        }
        return answer;
      }
      case 'checkedTypes': {
        const names: unknown = implementation.checkedTypes?.call(implementation, config);
        synchronous(name, 'checkedTypes', names);
        if (!Array.isArray(names) || names.some((type) => typeof type !== 'string')) {
          throw new BehaviorError(name, 'checkedTypes returns a list of type names');
        }
        return [...new Set(names as string[])];
      }
      case 'afterConfigChange': {
        const context = this.publishContext(gen, name, cx);
        synchronous(name, 'afterConfigChange', implementation.afterConfigChange?.call(implementation, context as never));
        return undefined;
      }
      case 'initialize': {
        const [params] = args;
        const context = this.instanceContext(gen, name, cx, config);
        synchronous(name, 'initialize', implementation.initialize?.call(implementation, context as never, deepFreeze(params) as never));
        return undefined;
      }
      case 'guard': {
        const [request_] = args;
        const view = this.viewContext(gen, name, cx, config);
        const answer: unknown = implementation.guard?.call(implementation, view as never, deepFreeze(request_) as never);
        synchronous(name, 'guard', answer);
        return guardAnswer(name, 'guard', answer);
      }
      case 'validate': {
        const [request_] = args;
        const context = this.validationContext(gen, name, cx, config);
        const answer: unknown = implementation.validate?.call(implementation, context as never, deepFreeze(request_) as never);
        synchronous(name, 'validate', answer);
        return returnedIssues(name, answer);
      }
      case 'instanceSchema': {
        const [form] = args as [string];
        const typeSchema = (type: string, options?: { nullable?: boolean }) =>
          this.invoke(gen, 'typeSchema', [type, options?.nullable === true]);
        const answer: unknown = implementation.instanceSchema?.call(implementation, config, form as never, typeSchema);
        synchronous(name, 'instanceSchema', answer);
        const copied = jsonCopy(answer);
        if (!('value' in copied) || !Array.isArray(copied.value)) {
          throw new BehaviorError(name, 'instanceSchema returns a list of JSON Schemas');
        }
        return copied.value;
      }
      case 'guidance': {
        const [target] = args;
        const answer: unknown = implementation.guidance?.call(implementation, config, deepFreeze(target) as never);
        synchronous(name, 'guidance', answer);
        return guidanceOf(name, answer);
      }
      case 'createParamsSchema': {
        const [target] = args;
        const answer: unknown = implementation.createParamsSchema?.call(implementation, config, deepFreeze(target) as never);
        synchronous(name, 'createParamsSchema', answer);
        const copied = jsonCopy(answer);
        if (!('value' in copied)) {
          throw new BehaviorError(name, `createParamsSchema returns a JSON Schema: ${copied.problem}`);
        }
        return copied.value;
      }
      case 'operation': {
        const [params] = args;
        const operation = request.name as string;
        const handler = implementation.operations?.[operation];
        const context = this.operationContext(gen, name, cx, config);
        const result: unknown = handler?.call(implementation.operations, context as never, deepFreeze(params) as never);
        synchronous(name, `operation ${operation}`, result);
        return json(name, `operation ${operation} result`, result === undefined ? null : result);
      }
      case 'schemaOperation': {
        const [params] = args;
        const operation = request.name as string;
        const handler = implementation.schemaOperations?.[operation];
        const context = this.schemaContext(gen, name, cx, config);
        const result: unknown = handler?.call(implementation.schemaOperations, context as never, deepFreeze(params) as never);
        synchronous(name, `operation ${operation}`, result);
        return json(name, `operation ${operation} result`, result === undefined ? null : result);
      }
      case 'field': {
        const field = request.name as string;
        const reader = implementation.fields?.[field];
        const view = this.viewContext(gen, name, cx, config);
        const value: unknown = reader?.call(implementation.fields, view as never);
        synchronous(name, `field ${field}`, value);
        return value === undefined ? null : json(name, `field ${field}`, value);
      }
      case 'afterChange': {
        const [change] = args;
        const context = this.instanceContext(gen, name, cx, config);
        synchronous(name, 'afterChange', implementation.afterChange?.call(implementation, context as never, deepFreeze(change) as never));
        return undefined;
      }
      case 'guardReference': {
        const [reference, request_] = args;
        const view = this.viewContext(gen, name, cx, config);
        const answer: unknown = implementation.guardReference?.call(implementation, view as never, deepFreeze(reference) as never, deepFreeze(request_) as never);
        synchronous(name, 'guardReference', answer);
        return guardAnswer(name, 'guardReference', answer);
      }
      case 'afterReferenceChange': {
        const [reference, change] = args;
        const context = this.referenceContext(gen, name, cx, config);
        synchronous(
          name,
          'afterReferenceChange',
          implementation.afterReferenceChange?.call(implementation, context as never, deepFreeze(reference) as never, deepFreeze(change) as never)
        );
        return undefined;
      }
      case 'watches': {
        const [schema] = args as [string];
        const reactions = implementation.reactions;
        const named: unknown = reactions?.watches?.call(reactions, config, schema);
        synchronous(name, 'reactions.watches', named);
        if (named === null) {
          return null;
        }
        if (!Array.isArray(named) || named.some((each) => typeof each !== 'string')) {
          throw new BehaviorError(name, 'reactions.watches returns a list of schema names, or null to turn the reactions off');
        }
        return named;
      }
      case 'react': {
        const [event] = args;
        const reactions = implementation.reactions;
        const frozen = deepFreeze(event);
        const context = this.reactionContext(gen, name, cx, config, frozen);
        synchronous(name, 'react', reactions?.react.call(reactions, context as never, frozen as never));
        return undefined;
      }
      case 'every': {
        const schedule = request.name as string;
        const spec = implementation.schedules?.[schedule];
        const every = spec?.everyMs;
        const value: unknown = typeof every === 'function' ? every.call(spec, config) : every;
        synchronous(name, `schedule ${schedule} everyMs`, value);
        if (value === null) {
          return null;
        }
        if (typeof value !== 'number' || !Number.isSafeInteger(value)) {
          throw new BehaviorError(
            name,
            `schedule ${schedule}: everyMs(config) returns an integer of at least 1000, got ${typeof value === 'number' ? String(value) : typeof value}`
          );
        }
        return value;
      }
      case 'schedule': {
        const schedule = request.name as string;
        const spec = implementation.schedules?.[schedule];
        const context = this.scheduleContext(gen, name, cx, config);
        synchronous(name, `schedule ${schedule}`, spec?.run.call(spec, context as never));
        return undefined;
      }
      case 'migration': {
        const version = request.name as unknown as number;
        const migration = (implementation.migrations ?? [])[Number(version) - 1];
        const sql = this.tableWriter(gen);
        synchronous(name, `migration ${version}`, migration?.up?.call(migration, sql as never));
        return undefined;
      }
      default:
        throw new Error(`the Rust engine asked the bridge for an unknown hook: ${request.hook}`);
    }
  }

  // The contexts, as src/testing/behavior.ts types them. Each method
  // checks its arguments as the TypeScript engine's does, then calls the
  // addon with the context's generation.

  private scope(gen: number, name: string, cx: Record<string, unknown>, config: unknown): Record<string, unknown> {
    const invoke = (method: string, args: readonly unknown[] = []) => this.invoke(gen, method, args);
    const frozen = <T>(value: T): T => deepFreeze(value);
    return {
      behavior: name,
      config,
      namespace: cx.namespace,
      schema: cx.schema,
      version: cx.version,
      principal: frozen(cx.principal),
      now: cx.now,
      can: (permission: string) => {
        if (typeof permission !== 'string' || permission === '') {
          throw new BehaviorError(name, 'can() takes a permission: a non-empty string');
        }
        return invoke('can', [permission]);
      },
      instances: Object.freeze({
        get: (schema: string, id: string, options?: { fields?: readonly string[] }) => {
          checkName(name, 'instances.get', 'id', id);
          checkName(name, 'instances.get', 'schema', schema);
          return frozen(invoke('instances.get', [schema, id, fieldsOption(name, 'instances.get', options)]) ?? undefined);
        },
        getMany: (schema: string, ids: readonly string[], options?: { fields?: readonly string[] }) => {
          if (!Array.isArray(ids) || ids.some((id) => typeof id !== 'string')) {
            throw new BehaviorError(name, 'instances.getMany takes a list of instance ids');
          }
          checkName(name, 'instances.getMany', 'schema', schema);
          const entries = invoke('instances.getMany', [schema, ids, fieldsOption(name, 'instances.getMany', options)]) as Array<[string, unknown]>;
          return new Map(entries.map(([id, record]) => [id, frozen(record)]));
        },
        invoke: (schema: string, id: string, operation: string, params?: unknown, options?: { preconditions?: unknown }) => {
          checkName(name, 'instances.invoke', 'schema', schema);
          checkName(name, 'instances.invoke', 'id', id);
          checkName(name, 'instances.invoke', 'operation', operation);
          if (options !== undefined && (typeof options !== 'object' || options === null)) {
            throw new BehaviorError(name, 'instances.invoke takes options { preconditions? }');
          }
          return frozen(invoke('instances.invoke', [schema, id, operation, params ?? {}, options?.preconditions ?? null]));
        },
        invokeSchema: (schema: string, operation: string, params?: unknown) => {
          checkName(name, 'instances.invokeSchema', 'schema', schema);
          checkName(name, 'instances.invokeSchema', 'operation', operation);
          return frozen(invoke('instances.invokeSchema', [schema, operation, params ?? {}]));
        },
        create: (schema: string, data: unknown, options?: { id?: string; behaviors?: unknown }) => {
          checkName(name, 'instances.create', 'schema', schema);
          if (options !== undefined && (typeof options !== 'object' || options === null)) {
            throw new BehaviorError(name, 'instances.create takes options { id?, behaviors? }');
          }
          if (options?.id !== undefined) {
            checkName(name, 'instances.create', 'id', options.id);
          }
          const copied = jsonCopy(data);
          if (!('value' in copied)) {
            throw new BehaviorError(name, `instances.create: the data is not JSON${copied.path ? ` at ${copied.path}` : ''}: ${copied.problem}`);
          }
          if (!isPlainObject(copied.value)) {
            throw new BehaviorError(name, "instances.create takes the instance's own fields: a JSON object");
          }
          let behaviors: unknown = null;
          if (options?.behaviors !== undefined) {
            const given = jsonCopy(options.behaviors);
            if (!('value' in given)) {
              throw new BehaviorError(name, `instances.create: behaviors is not JSON${given.path ? ` at ${given.path}` : ''}: ${given.problem}`);
            }
            if (!isPlainObject(given.value)) {
              throw new BehaviorError(name, "instances.create takes behaviors, the create's parameters by behavior name: a JSON object");
            }
            behaviors = given.value;
          }
          return frozen(invoke('instances.create', [schema, copied.value, options?.id ?? null, behaviors]));
        },
      }),
      schemas: Object.freeze({
        config: (schema: string, behavior: string) => {
          checkName(name, 'schemas.config', 'schema', schema);
          checkName(name, 'schemas.config', 'behavior', behavior);
          return frozen(invoke('schemas.config', [schema, behavior]));
        },
        readable: (schema: string) => {
          checkName(name, 'schemas.readable', 'schema', schema);
          return invoke('schemas.readable', [schema]);
        },
      }),
      validate: (type: string, value: unknown) => frozen(invoke('validate', [String(type), jsonArgument(name, 'validate', value)])),
    };
  }

  // sqlReader is a read's SQL: run() refuses, as the TypeScript engine's
  // read-mode handle does.
  private sqlReader(gen: number, name: string): Record<string, unknown> {
    return Object.freeze({
      ...this.sqlMembers(gen),
      run: () => {
        throw new BehaviorError(name, 'a read cannot run a statement that writes; run() is for writes');
      },
    });
  }

  private sqlMembers(gen: number): Record<string, unknown> {
    const invoke = (method: string, args: readonly unknown[] = []) => this.invoke(gen, method, args);
    return {
      table: (table: string) => invoke('sql.table', [String(table)]),
      instances: () => invoke('sql.instances'),
      get: (sql: string, params: readonly unknown[] = []) => rowOf(invoke('sql.get', [String(sql), sqlParams(params)])),
      all: (sql: string, params: readonly unknown[] = []) => (invoke('sql.all', [String(sql), sqlParams(params)]) as unknown[]).map(rowOf),
    };
  }

  private sqlWriter(gen: number): Record<string, unknown> {
    return Object.freeze({
      ...this.sqlMembers(gen),
      run: (sql: string, params: readonly unknown[] = []) => invokeRun(this.invoke(gen, 'sql.run', [String(sql), sqlParams(params)])),
    });
  }

  private tableWriter(gen: number): Record<string, unknown> {
    return this.sqlWriter(gen);
  }

  private valueReader(gen: number, name: string): Record<string, unknown> {
    const refuse = (what: string) => () => {
      throw new BehaviorError(name, `a read cannot change what the value store holds (values.${what})`);
    };
    return Object.freeze({
      load: (json: string, refs?: string | null) => deepFreeze(this.invoke(gen, 'values.load', [String(json), refs ?? null])),
      stow: refuse('stow'),
      release: refuse('release'),
    });
  }

  private valueWriter(gen: number, name: string): Record<string, unknown> {
    return Object.freeze({
      load: (json: string, refs?: string | null) => deepFreeze(this.invoke(gen, 'values.load', [String(json), refs ?? null])),
      stow: (key: string, object: unknown) => {
        if (typeof key !== 'string') {
          throw new BehaviorError(name, 'values.stow takes the key of the row: a string');
        }
        return this.invoke(gen, 'values.stow', [key, jsonArgument(name, 'values.stow', object)]);
      },
      release: (key?: string) => {
        this.invoke(gen, 'values.release', [key ?? null]);
      },
    });
  }

  // instanceLike is a context of one instance: a guard's or a field
  // reader's view, a reference's, a write's or an operation's.
  private instanceLike(kind: 'view' | 'reference' | 'instance' | 'operation', gen: number, name: string, cx: Record<string, unknown>, config: unknown): object {
    // eslint-disable-next-line @typescript-eslint/no-this-alias
    const bridge = this;
    const invoke = (method: string, args: readonly unknown[] = []) => this.invoke(gen, method, args);
    const writes = kind === 'instance' || kind === 'operation';
    let data: unknown;
    const context: Record<string, unknown> = {
      ...this.scope(gen, name, cx, config),
      id: cx.id,
      get data(): unknown {
        if (data === undefined) {
          data = deepFreeze(bridge.invoke(gen, 'data', []));
        }
        return data;
      },
      columns: Object.freeze({
        get: () => rowOf(invoke('columns.get')),
        set: (values: Readonly<Record<string, unknown>>) => {
          if (!writes) {
            throw new BehaviorError(name, 'a read cannot set columns');
          }
          if (typeof values !== 'object' || values === null || Array.isArray(values)) {
            throw new BehaviorError(name, 'columns.set takes an object of column values');
          }
          invoke('columns.set', [Object.entries(values).map(([column, value], index) => [column, sqlValue(value, index)])]);
        },
      }),
      sql: writes ? this.sqlWriter(gen) : this.sqlReader(gen, name),
      references: Object.freeze(
        writes
          ? {
              list: () => deepFreeze(invoke('references.list')),
              add: (schema: string, id: string, key: string = '', hears?: unknown) => {
                const reference = referenceOf(name, schema, id, key, hears);
                invoke('references.add', [reference.schema, reference.id, reference.key, reference.hears ?? null]);
              },
              remove: (schema: string, id: string, key: string = '') => {
                const reference = referenceOf(name, schema, id, key);
                return invoke('references.remove', [reference.schema, reference.id, reference.key]);
              },
            }
          : { list: () => deepFreeze(invoke('references.list')) }
      ),
      values: writes ? this.valueWriter(gen, name) : this.valueReader(gen, name),
    };
    if (kind === 'reference') {
      context.writing = cx.writing === true;
    }
    if (writes) {
      context.call = (behavior: string, operation: string, params?: unknown) => deepFreeze(invoke('call', [String(behavior), String(operation), params ?? {}]));
    }
    if (kind === 'operation') {
      context.update = (patch: unknown, options?: { attribution?: unknown }) => {
        const args = [updatePatch(name, patch)];
        if (options?.attribution !== undefined) {
          args.push(updateAttribution(name, options.attribution));
        }
        const value = deepFreeze(invoke('update', args));
        data = value;
        return value;
      };
      context.validateUpdate = (patch: unknown) => deepFreeze(invoke('validateUpdate', [updatePatch(name, patch)]));
      context.unchanged = () => {
        invoke('unchanged');
      };
    }
    return Object.freeze(context);
  }

  private viewContext(gen: number, name: string, cx: Record<string, unknown>, config: unknown): object {
    return this.instanceLike('view', gen, name, cx, config);
  }

  private referenceContext(gen: number, name: string, cx: Record<string, unknown>, config: unknown): object {
    return this.instanceLike('reference', gen, name, cx, config);
  }

  private instanceContext(gen: number, name: string, cx: Record<string, unknown>, config: unknown): object {
    return this.instanceLike('instance', gen, name, cx, config);
  }

  private operationContext(gen: number, name: string, cx: Record<string, unknown>, config: unknown): object {
    return this.instanceLike('operation', gen, name, cx, config);
  }

  private validationContext(gen: number, name: string, cx: Record<string, unknown>, config: unknown): object {
    return Object.freeze({
      behavior: name,
      config,
      namespace: cx.namespace,
      schema: cx.schema,
      version: cx.version,
      id: cx.id,
      principal: deepFreeze(cx.principal),
      now: cx.now,
      can: (permission: string) => {
        if (typeof permission !== 'string' || permission === '') {
          throw new BehaviorError(name, 'can() takes a permission: a non-empty string');
        }
        return this.invoke(gen, 'can', [permission]);
      },
      checkType: (type: string, value: unknown, path: string) => {
        if (typeof path !== 'string') {
          throw new BehaviorError(name, 'checkType takes the path of the value: a string');
        }
        return deepFreeze(this.invoke(gen, 'checkType', [String(type), jsonArgument(name, 'checkType', value), path]));
      },
    });
  }

  private schemaContext(gen: number, name: string, cx: Record<string, unknown>, config: unknown): object {
    return Object.freeze({ ...this.scope(gen, name, cx, config), sql: this.sqlWriter(gen), values: this.valueWriter(gen, name) });
  }

  private reactionContext(gen: number, name: string, cx: Record<string, unknown>, config: unknown, event: unknown): object {
    return Object.freeze({
      ...this.scope(gen, name, cx, config),
      sql: this.sqlReader(gen, name),
      values: this.valueReader(gen, name),
      before: (given: unknown) => deepFreeze(this.invoke(gen, 'before', [given === event ? null : given])),
    });
  }

  private scheduleContext(gen: number, name: string, cx: Record<string, unknown>, config: unknown): object {
    return Object.freeze({
      ...this.scope(gen, name, cx, config),
      sql: this.sqlWriter(gen),
      values: this.valueWriter(gen, name),
      schedule: cx.schedule,
      previous: cx.previous ?? undefined,
    });
  }

  private publishContext(gen: number, name: string, cx: Record<string, unknown>): object {
    return Object.freeze({
      behavior: name,
      config: cx.config === null || cx.config === undefined ? undefined : this.config(cx.config as number),
      before: cx.before === null || cx.before === undefined ? undefined : this.config(cx.before as number),
      namespace: cx.namespace,
      schema: cx.schema,
      version: cx.version,
      now: cx.now,
      sql: this.tableWriter(gen),
      values: this.valueWriter(gen, name),
      eachInstance: (visit: (instance: unknown) => void) => {
        if (typeof visit !== 'function') {
          throw new BehaviorError(name, 'eachInstance takes a function');
        }
        for (const instance of this.invoke(gen, 'eachInstance', []) as unknown[]) {
          synchronous(name, 'eachInstance', visit(deepFreeze(instance)));
        }
      },
      validate: (type: string, value: unknown) => deepFreeze(this.invoke(gen, 'validate', [String(type), jsonArgument(name, 'validate', value)])),
    });
  }
}

// described names a thrown value as the TypeScript runner's status does.
function described(error: unknown): string {
  if (error instanceof EngineError) {
    return `${error.code}: ${error.message}`;
  }
  if (error instanceof Error) {
    return `${error.name}: ${error.message}`;
  }
  return String(error);
}

function vetoOf(error: BehaviorVetoError): { behavior: string; action: string; schema: string; id: string; reason: string; code?: string; details?: unknown } {
  // The message is `behavior <b> vetoes <action> of <schema> <id>: <reason>`.
  const prefix = `behavior ${error.behavior} vetoes ${error.action} of `;
  const suffix = `: ${error.reason}`;
  let schema = '';
  let id = '';
  if (error.message.startsWith(prefix) && error.message.endsWith(suffix)) {
    const middle = error.message.slice(prefix.length, error.message.length - suffix.length);
    const space = middle.indexOf(' ');
    schema = space < 0 ? middle : middle.slice(0, space);
    id = space < 0 ? '' : middle.slice(space + 1);
  }
  return {
    behavior: error.behavior,
    action: error.action,
    schema,
    id,
    reason: error.reason,
    ...(error.vetoCode === undefined ? {} : { code: error.vetoCode }),
    ...(error.vetoDetails === undefined ? {} : { details: error.vetoDetails }),
  };
}

function synchronous(behavior: string, what: string, value: unknown): void {
  if ((typeof value === 'object' || typeof value === 'function') && value !== null && typeof (value as { then?: unknown }).then === 'function') {
    (value as PromiseLike<unknown>).then(undefined, () => undefined);
    throw new BehaviorError(behavior, `${what} is synchronous (D16): it returned a promise`);
  }
}

function json(behavior: string, what: string, value: unknown): unknown {
  const copied = jsonCopy(value);
  if (!('value' in copied)) {
    throw new BehaviorError(behavior, `${what} is not JSON${copied.path ? ` at ${copied.path}` : ''}: ${copied.problem}`);
  }
  return copied.value;
}

// jsonArgument is a value a context method passes on, which must be JSON.
function jsonArgument(behavior: string, what: string, value: unknown): unknown {
  const copied = jsonCopy(value);
  if (!('value' in copied)) {
    throw new BehaviorError(behavior, `${what}: the value is not JSON${copied.path ? ` at ${copied.path}` : ''}: ${copied.problem}`);
  }
  return copied.value;
}

function updatePatch(behavior: string, patch: unknown): unknown {
  const copied = jsonCopy(patch);
  if (!('value' in copied)) {
    throw new BehaviorError(behavior, `update() takes a JSON merge patch: it is not JSON${copied.path ? ` at ${copied.path}` : ''}: ${copied.problem}`);
  }
  return copied.value;
}

// updateAttribution copies update()'s attribution as JSON; the engine
// holds it to its shape.
function updateAttribution(behavior: string, attribution: unknown): unknown {
  const copied = jsonCopy(attribution);
  if (!('value' in copied)) {
    throw new BehaviorError(behavior, `update()'s attribution is not JSON${copied.path ? ` at ${copied.path}` : ''}: ${copied.problem}`);
  }
  return copied.value;
}

// guardAnswer reads a guard's answer: null to allow, or a veto.
function guardAnswer(behavior: string, what: string, answer: unknown): unknown {
  if (answer === undefined || answer === null) {
    return null;
  }
  if (typeof answer === 'string' && answer !== '') {
    return { reason: answer };
  }
  const shape = `${what} returns a reason (a non-empty string) or { reason, code?, details? } to veto, or undefined to allow`;
  if (!isPlainObject(answer) || Object.keys(answer).some((key) => key !== 'reason' && key !== 'code' && key !== 'details')) {
    throw new BehaviorError(behavior, shape);
  }
  const { reason, code, details } = answer;
  if (typeof reason !== 'string' || reason === '') {
    throw new BehaviorError(behavior, shape);
  }
  if (code !== undefined && typeof code !== 'string') {
    throw new BehaviorError(behavior, `a veto's code ${JSON.stringify(code)} is not one its declaration lists`);
  }
  if (details === undefined) {
    return code === undefined ? { reason } : { reason, code };
  }
  const copied = jsonCopy(details);
  if (!('value' in copied) || !isPlainObject(copied.value)) {
    throw new BehaviorError(behavior, "a veto's details are a JSON object");
  }
  return { reason, ...(code === undefined ? {} : { code }), details: copied.value };
}

function returnedIssues(behavior: string, answer: unknown): unknown[] {
  if (answer === undefined || answer === null) {
    return [];
  }
  const shaped = (issue: unknown): issue is { path: string; rule: string; message: string } =>
    typeof issue === 'object' &&
    issue !== null &&
    typeof (issue as { path?: unknown }).path === 'string' &&
    typeof (issue as { rule?: unknown }).rule === 'string' &&
    (issue as { rule: string }).rule !== '' &&
    typeof (issue as { message?: unknown }).message === 'string' &&
    (issue as { message: string }).message !== '';
  if (!Array.isArray(answer) || !answer.every(shaped)) {
    throw new BehaviorError(behavior, 'validate returns a list of issues, each { path, rule, message } with a rule and a message, or nothing to accept');
  }
  return answer.map(({ path, rule, message }) => ({ path, rule, message }));
}

// guidanceOf holds a guidance's shape; the Rust engine holds its content.
function guidanceOf(behavior: string, answer: unknown): unknown {
  const copied = jsonCopy(answer);
  if (!('value' in copied) || !isPlainObject(copied.value)) {
    throw new BehaviorError(behavior, 'guidance returns { summary, operations? }, a JSON object');
  }
  const value = copied.value;
  const unknown = Object.keys(value).filter((key) => key !== 'summary' && key !== 'operations');
  if (unknown.length > 0) {
    throw new BehaviorError(behavior, `guidance returns { summary, operations? }, not ${unknown.map((key) => JSON.stringify(key)).join(', ')}`);
  }
  const text = (at: string, given: unknown): unknown => {
    if (given !== undefined && typeof given !== 'string') {
      throw new BehaviorError(behavior, `${at} is a string without surrounding whitespace`);
    }
    return given;
  };
  text('summary', value.summary);
  if (value.operations !== undefined) {
    if (!isPlainObject(value.operations)) {
      throw new BehaviorError(behavior, "guidance operations is an object of each operation's guidance, by its name");
    }
    for (const [operation, raw] of Object.entries(value.operations)) {
      const at = `guidance of ${operation}`;
      if (!isPlainObject(raw)) {
        throw new BehaviorError(behavior, `${at} is { useWhen?, doNotUseWhen?, success?, errors? }`);
      }
      const keys = Object.keys(raw).filter((key) => !['useWhen', 'doNotUseWhen', 'success', 'errors'].includes(key));
      if (keys.length > 0) {
        throw new BehaviorError(behavior, `${at} has the unknown key ${keys.map((key) => JSON.stringify(key)).join(', ')}`);
      }
      for (const key of ['useWhen', 'doNotUseWhen', 'success']) {
        text(`${at}: ${key}`, raw[key]);
      }
      if (raw.errors !== undefined) {
        if (!Array.isArray(raw.errors)) {
          throw new BehaviorError(behavior, `${at}: errors is a list of { code, description?, commonCorrection }`);
        }
        for (const error of raw.errors as unknown[]) {
          if (!isPlainObject(error) || typeof error.code !== 'string') {
            throw new BehaviorError(behavior, `${at}: an error's code is a veto code its declaration lists, not ${isPlainObject(error) ? JSON.stringify(error.code) : 'none'}`);
          }
          text(`${at}: the description of ${error.code}`, error.description);
          text(`${at}: the commonCorrection of ${error.code}`, error.commonCorrection);
        }
      }
    }
  }
  return value;
}

function checkName(behavior: string, what: string, name: string, value: unknown): void {
  if (typeof value !== 'string' || value === '') {
    throw new BehaviorError(behavior, `${what} takes ${name === 'id' ? 'an instance id' : `a ${name} name`}: a non-empty string`);
  }
}

function fieldsOption(behavior: string, what: string, options: { fields?: readonly string[] } | undefined): readonly string[] | null {
  const fields = options?.fields;
  if (fields === undefined) {
    return null;
  }
  if (!Array.isArray(fields) || fields.some((field) => typeof field !== 'string' || !field.includes('.'))) {
    throw new BehaviorError(behavior, `${what}: fields is a list of behavior fields by qualified name, <behavior>.<field> (Workflow.status)`);
  }
  return fields;
}

function referenceOf(behavior: string, schema: string, id: string, key: string, hears?: unknown): { schema: string; id: string; key: string; hears?: unknown } {
  checkName(behavior, 'references', 'schema', schema);
  checkName(behavior, 'references', 'id', id);
  if (typeof key !== 'string') {
    throw new BehaviorError(behavior, 'a reference key is a string');
  }
  return hears === undefined ? { schema, id, key } : { schema, id, key, hears: checkHears(behavior, hears) };
}

function checkHears(behavior: string, hears: unknown): unknown {
  if (hears === 'delete') {
    return hears;
  }
  const refuse = (what: string): BehaviorError => new BehaviorError(behavior, `references.add: hears is 'delete' or { path, crosses? }, ${what}`);
  if (typeof hears !== 'object' || hears === null || Array.isArray(hears)) {
    throw refuse(`not ${JSON.stringify(hears) ?? String(hears)}`);
  }
  const { path, crosses, ...rest } = hears as { path?: unknown; crosses?: unknown };
  if (Object.keys(rest).length > 0) {
    throw refuse(`with no other member (${Object.keys(rest).join(', ')})`);
  }
  if (typeof path !== 'string' || !/^(\/([^~/]|~[01])*)+$/.test(path) || !/^\/(data|behaviors\/([^~/]|~[01])+)\/./.test(path)) {
    throw refuse(
      `its path a JSON pointer into the target as a read returns it, /data/<field> or /behaviors/<behavior>/<field>, not ${JSON.stringify(path) ?? String(path)}`
    );
  }
  if (crosses === undefined) {
    return { path };
  }
  if (typeof crosses !== 'number' || !Number.isFinite(crosses)) {
    throw refuse(`its crosses a finite number, not ${JSON.stringify(crosses) ?? String(crosses)}`);
  }
  return { path, crosses };
}

// SQL values cross as JSON: a number, a string or null as it is, an
// integer the engine binds as one, a bigint and a blob tagged.
function sqlValue(value: unknown, index: number): unknown {
  if (value === null || typeof value === 'string') {
    return value;
  }
  if (typeof value === 'number' && Number.isFinite(value)) {
    return Number.isInteger(value) ? value : { $real: value };
  }
  if (typeof value === 'bigint') {
    return { $bigint: value.toString() };
  }
  if (value instanceof Uint8Array) {
    return { $blob: Buffer.from(value.buffer, value.byteOffset, value.byteLength).toString('base64') };
  }
  const kind = typeof value === 'number' ? String(value) : value === undefined ? 'undefined' : typeof value;
  throw new TypeError(`SQL parameter ${index + 1} is ${kind}: bind a string, a finite number, a bigint, a Uint8Array or null`);
}

/** sqlParams writes SQL parameters as the addon reads them, refusing what a TypeScript driver refuses. */
export function sqlParams(params: readonly unknown[]): unknown[] {
  if (!Array.isArray(params)) {
    throw new TypeError('SQL parameters are a list');
  }
  return params.map(sqlValue);
}

// rowOf is a row as a TypeScript driver returns it: a blob a Uint8Array.
export function rowOf(row: unknown): Record<string, unknown> | undefined {
  if (row === null || row === undefined) {
    return undefined;
  }
  const out: Record<string, unknown> = {};
  for (const [column, value] of Object.entries(row as Record<string, unknown>)) {
    out[column] = typeof value === 'object' && value !== null && '$blob' in value ? new Uint8Array(Buffer.from((value as { $blob: string }).$blob, 'base64')) : value;
  }
  return out;
}

function invokeRun(result: unknown): { changes: number; lastInsertRowid: number } {
  const { changes, lastInsertRowid } = result as { changes: number; lastInsertRowid: number | null };
  return { changes, lastInsertRowid: lastInsertRowid ?? 0 };
}

// encodeValue and decodeValue carry a value across JSON: undefined as
// absent, as JSON.stringify writes it.
function encodeValue(value: unknown): unknown {
  return value === undefined ? null : value;
}

function decodeValue(value: unknown): unknown {
  return value;
}

function migrationsOf(migrations: unknown, problems: string[]): unknown[] {
  if (migrations === undefined) {
    return [];
  }
  if (!Array.isArray(migrations)) {
    problems.push('migrations is a list');
    return [];
  }
  return migrations.map((migration: Record<string, unknown>, index) => {
    const at = `migration ${index + 1}`;
    if (typeof migration !== 'object' || migration === null) {
      problems.push(`${at} is an object`);
      return { version: index + 1, name: `migration${index + 1}`, columns: [], indexes: [], up: false };
    }
    let version = index + 1;
    if (typeof migration.version === 'number' && Number.isSafeInteger(migration.version) && migration.version > 0) {
      version = migration.version;
    } else {
      problems.push(`migrations are numbered 1, 2, 3, ... in order; position ${index + 1} holds version ${String(migration.version)}`);
    }
    let name = migration.name;
    if (typeof name !== 'string' || name === '') {
      problems.push(`${at} needs a name`);
      name = `migration${index + 1}`;
    }
    if (migration.up !== undefined && typeof migration.up !== 'function') {
      problems.push(`${at} up is a function`);
    }
    const columns: unknown[] = [];
    if (migration.columns !== undefined) {
      if (typeof migration.columns !== 'object' || migration.columns === null) {
        problems.push(`${at} columns is an object of column specs by name`);
      } else {
        for (const [column, spec] of Object.entries(migration.columns as Record<string, unknown>)) {
          if (typeof spec !== 'object' || spec === null) {
            problems.push(`${at}: column ${column} is { type, notNull?, default? }`);
            continue;
          }
          const { type, notNull, default: fallback } = spec as { type?: unknown; notNull?: unknown; default?: unknown };
          if (typeof type !== 'string' || !COLUMN_TYPES.includes(type)) {
            problems.push(`${at}: column ${column}: type is one of ${COLUMN_TYPES.join(', ')}`);
            continue;
          }
          if (notNull !== undefined && typeof notNull !== 'boolean') {
            problems.push(`${at}: column ${column}: notNull is a boolean`);
          }
          columns.push([column, { type, notNull: notNull === true, default: fallback === undefined ? null : fallback }]);
        }
      }
    }
    const indexes: unknown[] = [];
    if (migration.indexes !== undefined) {
      if (typeof migration.indexes !== 'object' || migration.indexes === null || Array.isArray(migration.indexes)) {
        problems.push(`${at} indexes is an object of column lists by index name`);
      } else {
        for (const [index_, list] of Object.entries(migration.indexes as Record<string, unknown>)) {
          if (!Array.isArray(list) || list.length === 0 || list.some((column) => typeof column !== 'string')) {
            problems.push(`${at} index ${index_} is a non-empty list of the behavior's column names`);
            continue;
          }
          indexes.push([index_, list]);
        }
      }
    }
    return { version, name, columns, indexes, up: typeof migration.up === 'function' };
  });
}

function filtersOf(filters: unknown, problems: string[]): unknown[] {
  if (filters === undefined) {
    return [];
  }
  if (typeof filters !== 'object' || filters === null || Array.isArray(filters)) {
    problems.push('filters is an object of { column, type } by field name');
    return [];
  }
  return Object.entries(filters as Record<string, unknown>).map(([name, filter]) => {
    const { column, type, description } = (typeof filter === 'object' && filter !== null ? filter : {}) as {
      column?: unknown;
      type?: unknown;
      description?: unknown;
    };
    return [
      name,
      {
        column: typeof column === 'string' ? column : '',
        type: typeof type === 'string' ? type : '',
        description: description === undefined ? null : typeof description === 'string' ? description : '',
      },
    ];
  });
}

const SCHEDULE_NAME = /^[a-z][A-Za-z0-9]*$/;

function schedulesOf(schedules: unknown, problems: string[]): unknown[] {
  if (schedules === undefined) {
    return [];
  }
  if (typeof schedules !== 'object' || schedules === null || Array.isArray(schedules)) {
    problems.push('schedules is an object of schedules by name');
    return [];
  }
  const out: unknown[] = [];
  for (const [name, schedule] of Object.entries(schedules)) {
    const at = `schedule ${name}`;
    if (!SCHEDULE_NAME.test(name)) {
      problems.push(`${at}: a schedule name is camelCase (${SCHEDULE_NAME.source})`);
    }
    if (typeof schedule !== 'object' || schedule === null) {
      problems.push(`${at} is { everyMs, run }`);
      continue;
    }
    const { everyMs, run } = schedule as { everyMs?: unknown; run?: unknown };
    if (typeof everyMs !== 'function' && (typeof everyMs !== 'number' || !Number.isSafeInteger(everyMs) || everyMs < 1000)) {
      problems.push(
        `${at}: everyMs is an integer of at least 1000, or a function of the config that returns one, got ${typeof everyMs === 'number' ? String(everyMs) : typeof everyMs}`
      );
      continue;
    }
    if (typeof run !== 'function') {
      problems.push(`${at}: run is a function`);
    }
    out.push([name, typeof everyMs === 'number' ? everyMs : null]);
  }
  return out;
}
