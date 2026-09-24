/**
 * The kinds the core compiler registers. Closed: an extension adds a kind by
 * registering it with superschematic, not by extending this enum.
 */
export enum SchemaKind {
  DB = "DB",
  API = "API",
  General = "General"
}

/**
 * The name of a kind an extension registers ("Platform"). The authoring
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

export type ServiceHandle = {
  readonly __brand: "ServiceHandle";
  readonly name: string;
  readonly kind: SchemaKindName;
};

export type TargetOutputConfig = {
  readonly enabled: boolean;
};

export type TypesOutputConfig = Partial<Record<TargetLanguage, TargetOutputConfig>>;

/**
 * The REST API server language. GO (the default) emits the chi server module, RUST the axum crate, TYPESCRIPT the Hono package built on the TypeScript HTTP runtime (`<out>/api/<service>`, `<scope>/<service>-api`).
 */
export type ApiLanguage = "GO" | "RUST" | "TYPESCRIPT";

export type ApiOutputConfig = {
  readonly enabled: boolean;
  readonly language?: ApiLanguage;
  readonly scaffoldsOutputDir?: string;
};

export type SdkOutputConfig = Partial<Record<TargetLanguage, TargetOutputConfig>>;

/**
 * The DB kind's sql output. The DDL and the ORM are implied by the kind; this block places and owns the generated projection view migrations.
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
};

export type SchemaOutputs = {
  readonly types?: TypesOutputConfig;
  readonly api?: ApiOutputConfig;
  readonly sdk?: SdkOutputConfig;
  readonly sql?: SqlOutputConfig;
};

export type SchemaConfig = {
  readonly name: string;
  readonly kind: SchemaKindName;
  readonly public?: boolean;
  readonly authDb?: ServiceHandle;
  readonly dependencies?: readonly ServiceHandle[];
  readonly outputs: SchemaOutputs;
};

/**
 * A dependency reference in the JSON/YAML config forms. The TypeScript form
 * builds ServiceHandle sentinels with the service() helper; the data forms
 * carry the same name + kind pair as a plain object.
 */
export type ServiceDependencyRef = {
  readonly name: string;
  readonly kind: SchemaKindName;
};

/**
 * The schema.config.json / schema.config.yaml document shape.
 *
 * This is the SchemaConfig contract projected onto plain data: authDb is the
 * service name, and dependencies is an explicit array (there is no import
 * system in the JSON/YAML forms to derive it from). superschematic validates the data
 * forms against the JSON Schema generated from this type (see the
 * gen-json-schema package script).
 */
export type SchemaConfigDocument = {
  readonly name: string;
  readonly kind: SchemaKindName;
  readonly public?: boolean;
  readonly authDb?: string;
  readonly dependencies?: readonly ServiceDependencyRef[];
  readonly outputs: SchemaOutputs;
};

export function defineConfig<TConfig extends SchemaConfig>(cfg: TConfig): TConfig {
  return cfg;
}

export function service(cfg: { readonly name: string; readonly kind: SchemaKindName }): ServiceHandle {
  return {
    __brand: "ServiceHandle",
    name: cfg.name,
    kind: cfg.kind
  };
}

export const envVars: ClassDecorator = () => {};
