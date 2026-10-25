/**
 * The kinds the core compiler registers. Closed: an extension adds a kind by
 * registering it with superschematic, not by extending this enum. A Stack
 * service declares what runs where over the others (`@superschematic/stack`)
 * and is named by none, so it has no sentinel. A Site service is a static
 * site: its config names the APIs it calls and how it builds, and its code
 * sits at its implementation path (D55). A Bucket service is private object
 * storage that APIs list in their `buckets` (D54): its config is all it
 * has, `{ name, kind: SchemaKind.Bucket, outputs: {} }`.
 */
export enum SchemaKind {
  DB = "DB",
  API = "API",
  General = "General",
  Stack = "Stack",
  Site = "Site",
  Bucket = "Bucket"
}

/**
 * The name of a kind an extension registers ("Catalog"). The authoring
 * packages cannot see the registry, so the name is any string here; superschematic
 * checks it against the registered kinds when it loads the config.
 */
// `string & {}` rather than `string`: a bare string in the SchemaKindName
// union would swallow the enum members and their completions. The generated
// JSON Schema renders it as a plain string.
export type ExtensionKind = string & {};

/** What a config's `kind` admits: a core SchemaKind member or an extension kind name. */
export type SchemaKindName = SchemaKind | ExtensionKind;

export enum TargetLanguage {
  Go = "go",
  TypeScript = "typescript",
  Python = "python",
  Rust = "rust"
}

/**
 * A handle to a service: what service() returns and a generated sentinel
 * exports. K is the service's kind, as a string ("API"), C the type of its
 * @envVars class, J the names of its @job classes, W the names of its
 * @worker classes and D the names of its @deriver classes. All five are
 * phantom: the sentinel generator writes them
 * (`service<"API", ShopApiConfig, "ExpireCarts", "FulfilOrders">({...})`),
 * and superschematic reads only name and kind. A DB or General handle has
 * no config type, no jobs, no workers and no derivers, nor has an API
 * without an @envVars class, a @job class, a @worker class or a @deriver
 * class, or a handle written by hand, so C, J, W and D keep their
 * defaults: any config, and any job, worker or deriver name. An API with
 * workers and no jobs has the jobs `never`, and one with derivers has
 * `never` for the jobs and the workers it lacks.
 */
export type ServiceHandle<
  K extends SchemaKindName = SchemaKindName,
  C = unknown,
  J extends string = string,
  W extends string = string,
  D extends string = string
> = {
  readonly __brand: "ServiceHandle";
  readonly name: string;
  readonly kind: K;
  /** Phantom: carries C for the type checker and is never set. */
  readonly __config?: C;
  /** Phantom: carries J for the type checker and is never set. */
  readonly __jobs?: J;
  /** Phantom: carries W for the type checker and is never set. */
  readonly __workers?: W;
  /** Phantom: carries D for the type checker and is never set. */
  readonly __derivers?: D;
};

export type TargetOutputConfig = {
  readonly enabled: boolean;
};

/**
 * The type library in each language. A library imports the types of every dependency it takes types from, so each such dependency must enable the same languages; build-all and build --with-deps refuse a build where one does not. A DB schema needs go: the Go ORM the kind always generates imports the Go types.
 */
export type TypesOutputConfig = Partial<Record<TargetLanguage, TargetOutputConfig>>;

/**
 * The REST API server language. GO (the default) emits the chi server module, RUST the axum crate, TYPESCRIPT the Hono package built on the TypeScript HTTP runtime (`<out>/api/<service>`, `<scope>/<service>-api`).
 */
export type ApiLanguage = "GO" | "RUST" | "TYPESCRIPT";

/**
 * The REST API server. The server imports the service's types in its language, so an enabled API needs that language enabled in types: go for GO (the default), rust for RUST, typescript for TYPESCRIPT.
 */
export type ApiOutputConfig = {
  readonly enabled: boolean;
  readonly language?: ApiLanguage;
  readonly scaffoldsOutputDir?: string;
  /**
   * Serve the operations the mcp decorator makes visible tools over MCP at POST /api/mcp (D81): the caller authenticates as on the API's other routes, and each tools/call runs through the route of its operation. A TypeScript API serves it; the build refuses it on a Go or Rust API, where support follows. Unset is off.
   */
  readonly mcp?: boolean;
};

/**
 * The client SDK in each language. An SDK imports the service's types in its language, so each enabled SDK language needs the same language enabled in types.
 */
export type SdkOutputConfig = Partial<Record<TargetLanguage, TargetOutputConfig>>;

/**
 * A database the DB kind's sql output is built for.
 */
export type SqlDialect = "postgres" | "sqlite";

/**
 * The DB kind's sql output. The DDL and the ORM are implied by the kind; this block places and owns the generated projection view migrations and lists the dialects the DDL is written for.
 */
export type SqlOutputConfig = {
  /**
   * Where the projection view migrations are written, relative to the service directory. Unset keeps them under <out>/sql/<service>/projections/migrations.
   */
  readonly migrationsDir?: string;
  /**
   * The Postgres role the migrations create the views as: SET ROLE around the view DDL and RESET ROLE after it, so the schema's default privileges for that role apply. The migration runner must be a member of the role. Unset creates the views as the runner.
   */
  readonly viewOwner?: string;
  /**
   * The Postgres schema that holds the tables the views read: each view selects <sourceSchema>.<table> for its source table and every joined table, so it does not depend on the migration runner's search_path. Unset leaves the tables unqualified.
   */
  readonly sourceSchema?: string;
  /**
   * The databases the service is built for. With sqlite listed, the build also writes sqlite/create.sql and refuses a schema that uses what SQLite does not support, and migrate plan --dialect sqlite plans for it. The list must hold postgres: the Go ORM the kind always generates runs on Postgres. Unset is ["postgres"].
   */
  readonly dialects?: readonly SqlDialect[];
};

/**
 * A DB service's engine document (D58), which the build writes when a type of the schema composes a behavior: an engine type, whose instances the engine keeps, which sql and orm leave out.
 */
export type EngineOutputConfig = {
  /**
   * The engine namespace the schema's engine types are defined, published and instantiated in: lowercase letters, digits and hyphens, starting with a letter, at most 63 characters. Unset is the service's name.
   */
  readonly namespace?: string;
  /**
   * The path a server of an API on this database mounts the engine's routes at, its event stream and its MCP endpoint under it: one or more segments, each of letters, digits and . _ ~ -. Unset is /engine/<service>.
   */
  readonly mount?: string;
  /**
   * The access policy the servers open the engine with: "permission", which allows an action on a type to a caller holding <Type>.<action> (Ticket.read, Ticket.write, Ticket.define, Ticket.publish); "allowAll", for tests and local use; or ordered rules, the first that matches a question deciding it. Unset is "permission".
   */
  readonly policy?: EnginePolicy;
  /**
   * How long the engine's event log keeps events: an age, a count or both, as the engine reads them. Every server, job, worker and runner on this database opens the engine with it, and the database's runner prunes the log, never past an event a reaction has yet to handle. Unset keeps every event.
   */
  readonly retention?: EngineRetention;
  /**
   * The engine's tool options the deployment sets beside the binary's invocation policy and vendor keys (D70).
   */
  readonly tools?: EngineToolsConfig;
};

/**
 * What a deployment sets of the engine's tool options (D11, D70).
 */
export type EngineToolsConfig = {
  /**
   * The MCP invocation policy of an engine tool or operation, by the name the engine's tools.invocation takes: a built-in tool's (create, delete, defineSchema), or a behavior operation's, <Behavior>.<operation> (Payments.capturePayment), in place of its declaration's. Each value is one of the binary's invocation policy's values (auto or ask in the core). The build refuses a name no engine type's behavior declares, and where the engine types compose Payments or Stock it refuses a map that does not name each of their operations that move money or stock (Payments' openPayment, capturePayment, voidPayment, refundPayment and refundFor; Stock's receiveStock, adjustStock and countStock). Every server, job, worker and runner on this database opens the engine with it, so an agent's MCP client asks before it runs an operation marked ask.
   */
  readonly invocation?: { readonly [name: string]: string };
  /**
   * Behavior operations, by <Behavior>.<operation>, that the engine's MCP tools leave out: a service calls them through the API or in-process, and no agent is offered them. The build refuses a name no engine type's behavior declares, and the engine document adds the service operations of the core's commerce behaviors (Payments' listPaymentActions, recordPaymentEvent and settleRefundFor) wherever they are composed.
   */
  readonly hidden?: readonly string[];
};

/**
 * The event log's retention (D58): maxAgeMs, maxEvents or both; an event goes when either lets it go. A stream or read that resumes from a cursor retention has pruned past is 410 cursor_expired.
 */
export type EngineRetention = (EngineRetentionOptions & { readonly maxAgeMs: number }) | (EngineRetentionOptions & { readonly maxEvents: number });

/**
 * The members of an engine's retention, each an integer in the engine's range.
 */
export type EngineRetentionOptions = {
  /**
   * Events older than this many milliseconds, by when they were written, are pruned.
   *
   * @asType integer
   * @minimum 1
   * @maximum 9007199254740991
   */
  readonly maxAgeMs?: number;
  /**
   * A namespace's events past its newest this many are pruned.
   *
   * @asType integer
   * @minimum 1
   * @maximum 9007199254740991
   */
  readonly maxEvents?: number;
  /**
   * The longest a reaction that does not advance (halted, in an archived namespace, or whose behavior the engine does not run) holds events retention lets go, in milliseconds. Unset, one that has run holds them until it advances, and one that has not holds none.
   *
   * @asType integer
   * @minimum 1
   * @maximum 9007199254740991
   */
  readonly maxHoldMs?: number;
  /**
   * How often the runner prunes, in milliseconds. Unset is 60000.
   *
   * @asType integer
   * @minimum 1000
   * @maximum 9007199254740991
   */
  readonly everyMs?: number;
  /**
   * Events pruned in one transaction. Unset is 1000.
   *
   * @asType integer
   * @minimum 1
   * @maximum 10000
   */
  readonly batchSize?: number;
};

/**
 * An engine's access policy (D58): a shorthand, or ordered rules, each matching on the question's action, operation, schema and namespace and the caller's subject and service, and either naming a permission the principal must hold or allowing or refusing outright. The engine checks the rules when a server opens it.
 */
export type EnginePolicy = "permission" | "allowAll" | { readonly rules: readonly EngineAccessRule[] };

/**
 * One rule of an engine's access policy, as the engine reads it.
 */
export type EngineAccessRule = { readonly [member: string]: unknown };

/**
 * One CI renderer's options for a Stack service's generated workflow.
 */
export type CiRendererConfig = {
  /**
   * The branch pull requests target and a push deploys from: a branch name, not a pattern. Unset is "main".
   */
  readonly branch?: string;
  /**
   * The directory, relative to the repository root, the build installs the workflow into when it exists. Unset is the renderer's own, .github/workflows for github.
   */
  readonly install?: string;
};

/**
 * The CI a Stack service's build writes, keyed by the renderer that writes it: github for GitHub Actions, or a renderer an extension registers. Only a Stack service sets it. The build writes each workflow under <out>/ci/<stack>/.
 */
export type CiOutputConfig = {
  readonly github?: CiRendererConfig;
  readonly [renderer: string]: CiRendererConfig | undefined;
};

export type SchemaOutputs = {
  readonly types?: TypesOutputConfig;
  readonly api?: ApiOutputConfig;
  readonly sdk?: SdkOutputConfig;
  readonly sql?: SqlOutputConfig;
  readonly ci?: CiOutputConfig;
  readonly engine?: EngineOutputConfig;
};

/**
 * The outputs block of the data forms: the core keys, plus the output key of
 * any generator an extension registers. The authoring packages cannot see the
 * registry, so an extension key is any name here; superschematic checks each
 * key against the registered generators, and each section against its
 * generator's OutputSchema, when it reads the config.
 */
export type SchemaOutputsDocument = SchemaOutputs & {
  readonly [outputKey: string]: unknown;
};

/**
 * How a Site service builds and what it serves (D55). Its code is the package at the naming file's [implementation_paths] site template, web/{service} unless set, a member of the Bun workspace.
 */
export type SiteConfig = {
  /**
   * The script of the site's package.json that builds it, which a deploy runs with bun run after a frozen install of the workspace. Unset is "build".
   */
  readonly build?: string;
  /**
   * The directory the build writes, relative to the site's directory and inside it. Unset is "dist".
   */
  readonly output?: string;
  /**
   * The file, relative to output, served for a path that names no file: "index.html" for a single-page application, whose router reads the path. Unset answers such a path 404.
   */
  readonly fallback?: string;
};

export type SchemaConfig = {
  readonly name: string;
  readonly kind: SchemaKindName;
  readonly public?: boolean;
  readonly authDb?: ServiceHandle;
  readonly dependencies?: readonly ServiceHandle[];
  /**
   * The API services this API's implementation calls, or this site's code calls from the browser. Only an API or a Site service sets it, and each entry is an API service's handle. Each callee is built before its caller. An API a site calls must be exposed in each stack that deploys the site.
   */
  readonly calls?: readonly ServiceHandle<"API">[];
  /**
   * The Bucket services this API's implementation uses: each is a Bucket in its Deps, and a bucket edge from its server and jobs. Only an API service sets it, and each entry is a Bucket service's handle.
   */
  readonly buckets?: readonly ServiceHandle<"Bucket">[];
  /**
   * How a Site service builds and what it serves. Only a Site service sets it.
   */
  readonly site?: SiteConfig;
  /**
   * The generated outputs. A Site service has none, and may leave it out.
   */
  readonly outputs?: SchemaOutputs;
};

/**
 * A service reference in the JSON/YAML config forms, in dependencies,
 * calls and buckets. The TypeScript form builds ServiceHandle sentinels with the
 * service() helper; the data forms carry the same name + kind pair as a
 * plain object.
 */
export type ServiceDependencyRef = {
  readonly name: string;
  readonly kind: SchemaKindName;
};

/**
 * The schema.config.json / schema.config.yaml document shape.
 *
 * This is the SchemaConfig contract projected onto plain data: authDb is the
 * service name, and dependencies and calls are explicit arrays of
 * name + kind pairs (there is no import system in the JSON/YAML forms to
 * derive them from). superschematic validates the data
 * forms against the JSON Schema generated from this type (see the
 * gen-json-schema package script).
 */
export type SchemaConfigDocument = {
  readonly name: string;
  readonly kind: SchemaKindName;
  readonly public?: boolean;
  readonly authDb?: string;
  readonly dependencies?: readonly ServiceDependencyRef[];
  /**
   * The API services this API's implementation calls, or this site's code calls from the browser. Only an API or a Site service sets it, and each entry names an API service. Each callee is built before its caller.
   */
  readonly calls?: readonly ServiceDependencyRef[];
  /**
   * The Bucket services this API's implementation uses: each is a Bucket in its Deps, and a bucket edge from its server and jobs. Only an API service sets it, and each entry names a Bucket service.
   */
  readonly buckets?: readonly ServiceDependencyRef[];
  /**
   * How a Site service builds and what it serves. Only a Site service sets it.
   */
  readonly site?: SiteConfig;
  /**
   * The generated outputs. A Site service has none, and may leave it out.
   */
  readonly outputs?: SchemaOutputsDocument;
};

export function defineConfig<TConfig extends SchemaConfig>(cfg: TConfig): TConfig {
  return cfg;
}

/**
 * Builds a handle to the service named. The kind comes back as its string,
 * so `service({ name: "shop-api", kind: SchemaKind.API })` is a
 * `ServiceHandle<"API">`, the type the sentinel's `service<"API">` gives.
 */
export function service<K extends SchemaKindName, C = unknown, J extends string = string, W extends string = string, D extends string = string>(cfg: {
  readonly name: string;
  readonly kind: K;
}): ServiceHandle<`${K}`, C, J, W, D> {
  return {
    __brand: "ServiceHandle",
    name: cfg.name,
    kind: cfg.kind as `${K}`
  };
}

/**
 * On a class of a General or API schema: its fields are the service's
 * environment variables, with a generated loader and values-schema.json. On
 * an API it is the config of the API's server, whose fields a stack's `env`
 * binds.
 */
export const envVars: ClassDecorator = () => {};
