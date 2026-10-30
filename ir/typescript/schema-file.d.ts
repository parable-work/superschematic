// @generated; do not edit
// TypeScript types for the schema-file data form: the JSON a *.schema.json
// file holds and `superschematic format --to=json` writes, either a
// multi-definition Document or one definition in its file form. They are
// written from the JSON Schema `superschematic json-schema` emits for the
// core registry (schema-file.json in this package), which is reflected from
// the Go IR structs. A registry closes six parts of that schema, which
// these types leave open: the extensions slots, the documents, the schema
// kinds, the MCP invocation policy key, and a behavior's name and config.
// Regenerate with:
//   go run ./internal/tools/schemafiletypes

/** A schema kind: one of the core's, or one a registry adds. */
export type SchemaKind = 'API' | 'Bucket' | 'DB' | 'General' | 'MemoryStore' | 'Site' | 'Stack' | (string & {});

/**
 * Extension data, keyed by extension name. On a type, a field, an operation
 * or an operation set, an extension's object is keyed by its decorator
 * names, each holding the decorator's argument (true for a decorator that
 * takes none). On the document root the object is the extension's own. A
 * registry admits the extensions and decorators it links.
 */
export type Extensions = { [extension: string]: { [key: string]: unknown } };

/**
 * Sidecar documents, keyed by document name. A registry admits the
 * documents it registers, each with its own schema.
 */
export type Documents = { [document: string]: unknown };

/**
 * A Superschematic source file in the JSON or YAML projection: a
 * multi-definition document mirroring the Schema IR, or a single definition
 * object.
 */
export type SchemaFile = Document | TypeDef | EnumFile | UnionFile | ScalarFile | OperationSetFile;

export interface App {
  comment?: string;
  extensions?: string[];
  name: string;
}

export interface ArgumentDef {
  comment?: string;
  default?: string;
  description?: string;
  encrypted?: boolean;
  isQuery?: boolean;
  name: string;
  references?: ReferenceDef;
  required?: boolean;
  search?: boolean;
  sort?: string[];
  typeRef: TypeRef;
  validateListMax?: number;
  validateListMin?: number;
  validateMax?: number;
  validateMaxLength?: number;
  validateMin?: number;
  validateMinLength?: number;
  validatePattern?: string;
}

export interface BehaviorRef {
  /**
   * The type's config of the behavior, which the registry holds to the
   * behavior's config schema. A config of {} is stored as none.
   */
  config?: unknown;
  /**
   * The anchor field of a field-anchored use (D103): the field whose type is
   * the behavior, which holds its value and names its columns and operations.
   * Absent for a use on the whole type.
   */
  field?: string;
  /**
   * The behavior's registered name: bare for a core behavior,
   * <extension>.<Name> for an extension's. A registry admits the behaviors it
   * registers.
   */
  name: string;
}

export interface DNSPlacement {
  platform: string;
  values?: { [key: string]: unknown };
}

export interface DatabaseDecl {
  hosts: ServiceRef[];
}

export interface DeployableRef {
  app?: string;
  deployable?: string;
  deriver?: string;
  job?: string;
  runner?: boolean;
  service?: ServiceRef;
  worker?: string;
}

export interface DeployableSettings {
  concurrency?: number;
  enabled?: boolean;
  env?: { [key: string]: EnvValue };
  instances?: number;
  maxInstances?: number;
  of: DeployableRef;
  onDeploy?: boolean;
  platform?: string;
  schedule?: string;
  timeZone?: string;
  values?: { [key: string]: unknown };
}

export interface Deriver {
  batchSize?: number;
  comment?: string;
  concurrency?: number;
  name: string;
  schemas?: string[];
}

/**
 * A field of the same type that holds the currency of an amount: a
 * Finance.Currency, or text.
 */
export interface DisplayCurrency {
  field: string;
}

/**
 * How a UI shows one Workflow state: its label, the present-progressive form
 * it shows while an instance is in the state, and a tone, what the state means
 * to a reader.
 */
export interface DisplayState {
  activeForm?: string;
  label?: string;
  tone?: 'muted' | 'active' | 'success' | 'warning' | 'danger';
}

export interface Document {
  apps?: App[];
  comment?: string;
  derivers?: Deriver[];
  description?: string;
  documents?: Documents;
  enums?: { [key: string]: EnumDef };
  extensions?: Extensions;
  imports?: Import[];
  jobs?: Job[];
  kind?: SchemaKind;
  memoryStoreDeclarations?: MemoryStoreDeclaration[];
  name?: string;
  operationSets?: OperationSet[];
  references?: ServiceRef[];
  scalars?: { [key: string]: ScalarDef };
  types?: { [key: string]: TypeDef };
  unions?: { [key: string]: UnionDef };
  workers?: Worker[];
}

export interface EnqueueCondition {
  equals?: string | number | boolean;
  field: string;
  isNull?: boolean;
  notNull?: boolean;
}

export interface EnqueueOnChange {
  backfill?: boolean;
  fields?: string[];
  message: { [key: string]: string };
  on: string[];
  queue: string;
  when?: EnqueueCondition[];
}

export interface EnumDef {
  comment?: string;
  description?: string;
  name: string;
  owner?: string;
  values: EnumValueDef[];
}

export interface EnumFile {
  comment?: string;
  description?: string;
  kind: 'Enum';
  name: string;
  owner?: string;
  values: EnumValueDef[];
}

export interface EnumValueDef {
  comment?: string;
  description?: string;
  name: string;
  serializedAs?: string;
  title?: string;
}

export interface EnvValue {
  parameter?: string;
  value?: string | number | boolean;
}

export interface EnvironmentDecl {
  dns?: DNSPlacement;
  domain?: string;
  order?: number;
  parameters?: string[];
  settings?: DeployableSettings[];
  target?: string;
  values?: { [key: string]: unknown };
}

export interface FieldDef {
  arguments?: ArgumentDef[];
  auth?: boolean;
  autoGenerated?: boolean;
  comment?: string;
  conflictUnit?: 'atomic' | 'keyed' | 'jsonSchema' | 'excluded';
  decrypted?: boolean;
  default?: string;
  description?: string;
  display?: FieldDisplay;
  docs?: OperationDocs;
  encrypted?: boolean;
  encryptedAtRest?: boolean;
  extensions?: Extensions;
  fileDownload?: FileDownloadDef;
  fileUpload?: FileUploadConfig;
  filterable?: boolean;
  hasMany?: boolean;
  hmacVerifiedProvider?: string;
  httpBinaryStream?: HTTPBinaryStreamDef;
  httpMethod?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  icon?: string;
  inheritedFrom?: string;
  internalMetadata?: boolean;
  jsonField?: boolean;
  jsonTag?: string;
  key?: boolean;
  manualRouteRegistration?: boolean;
  manyToMany?: boolean;
  mcp?: OperationMCP;
  middleware?: MiddlewareConfig;
  name: string;
  page?: OperationPage;
  paramType?: 'query';
  permissions?: string[];
  placeholder?: string;
  platformDefault?: string;
  projectedFrom?: string;
  projectedFunction?: ProjectionFunctionCall;
  public?: boolean;
  purpose?: string;
  references?: ReferenceDef;
  relation?: RelationDef;
  requireOwnership?: boolean;
  required?: boolean;
  restMethod?: string;
  restPath?: string;
  searchField?: boolean;
  secret?: boolean;
  serviceCallers?: ServiceCallers;
  sourceMustProject?: boolean;
  sqlName?: string;
  temporalFormat?: string;
  title?: string;
  typeRef: TypeRef;
  uiControl?: string;
  uiHidden?: boolean;
  unique?: boolean;
  validateDimensions?: number;
  validateListMax?: number;
  validateListMin?: number;
  validateMax?: number;
  validateMaxLength?: number;
  validateMin?: number;
  validateMinLength?: number;
  validatePattern?: string;
  validatePrecision?: number;
  validateScale?: number;
  validateUploadMaxBytes?: number;
  virtual?: boolean;
  webhook?: boolean;
}

/**
 * How a UI renders a field's control and its value (@display on a field).
 */
export interface FieldDisplay {
  currency?: string | DisplayCurrency;
  multiline?: boolean;
}

export interface FileDownloadDef {
  contentType: string;
  filename?: string;
}

export interface FileUploadConfig {
  allowedExtensions?: string[];
  allowedTypes?: string[];
  category?: string;
  maxSize?: number;
}

export interface GraphMemberConfig {
  graph: string;
  order?: string;
  parent?: GraphParent;
  singleton?: boolean;
}

export interface GraphParent {
  key: string;
  of: string;
}

export interface HTTPBinaryStreamDef {
  contentType: string;
  responseHeaders?: string[];
}

export interface IdentityFirstUser {
  permissions: string[];
  role: string;
}

export interface ImageConstraints {
  maxAspectRatio?: number;
  maxHeight?: number;
  maxWidth?: number;
  minAspectRatio?: number;
  requireTransparency?: boolean;
}

export interface Import {
  from?: string;
  package: string;
  types: string[];
}

export interface IndexDef {
  concurrently?: boolean;
  efConstruction?: number;
  expression?: string;
  keys: string[];
  lists?: number;
  m?: number;
  method?: string;
  name?: string;
  ops?: string;
  unique?: boolean;
  where?: string;
}

export interface Job {
  comment?: string;
  name: string;
  retries?: number;
  schedule?: string;
  timeZone?: string;
  timeout?: string;
}

export interface MCPIcon {
  family?: string;
  name: string;
  style?: string;
}

export interface MemoryStoreDeclaration {
  doc?: string;
  file?: string;
  kind: 'channel' | 'counter' | 'claim';
  maxTtlSeconds?: number;
  name: string;
  parts?: MemoryStorePart[];
  payload?: TypeRef;
  reserved?: boolean;
  symbol: string;
  ttlSeconds?: number;
}

export interface MemoryStorePart {
  format?: 'string' | 'integer' | 'boolean' | 'enum';
  name: string;
  type: TypeRef;
}

export interface MiddlewareConfig {
  bodyLimit?: number;
  rateLimit?: number;
  timeout?: number;
}

export interface OperationDocs {
  audience?: string;
  capability: string;
  description: string;
  doNotUseWhen?: string;
  errors?: OperationDocsError[];
  expectedRevisionPointers?: string[];
  idempotencyKeyPointers?: string[];
  lifecycle: 'draft' | 'experimental' | 'active' | 'deprecated' | 'retired';
  mappingStatus: 'mapped' | 'uncertain';
  replacement?: string;
  replayMode?: 'read_only' | 'idempotent' | 'compare_and_swap';
  success?: string;
  sunset?: string;
  title: string;
  useWhen?: string;
  visibility: 'public' | 'internal' | 'preview';
}

export interface OperationDocsError {
  code: string;
  commonCorrection: string;
  description: string;
}

export interface OperationMCP {
  _meta?: { [key: string]: unknown };
  description?: string;
  handle?: string;
  hidden: boolean;
  hiddenReason?: string;
  icon?: MCPIcon;
  name?: string;
  /**
   * The invocation policy of a visible tool, under the key the registry's
   * policy names and with one of its values. The core's key is
   * invocationPolicy, with the values auto or ask.
   */
  [invocationPolicyKey: string]: unknown;
}

/**
 * The operation's list result pages by an opaque cursor (@paginated): it takes
 * the query parameters cursor and limit and answers {items, next}. A member
 * left out takes its default.
 */
export interface OperationPage {
  defaultLimit?: number;
  defaultSort?: string;
  maxLimit?: number;
}

export interface OperationSet {
  comment?: string;
  description?: string;
  encrypted?: boolean;
  extensions?: Extensions;
  middleware?: MiddlewareConfig;
  name: string;
  operations: FieldDef[];
  serviceCallers?: ServiceCallers;
  userAdministration?: UserAdministrationConfig;
  userSessions?: UserSessionsConfig;
}

export interface OperationSetFile {
  comment?: string;
  description?: string;
  encrypted?: boolean;
  extensions?: Extensions;
  kind: 'OperationSet';
  middleware?: MiddlewareConfig;
  name: string;
  operations: FieldDef[];
  serviceCallers?: ServiceCallers;
  userAdministration?: UserAdministrationConfig;
  userSessions?: UserSessionsConfig;
}

export interface Origin {
  apis?: ServiceRef[];
  site: ServiceRef;
}

export interface ProjectionCollapse {
  by: string[];
  order?: ProjectionOrder[];
}

export interface ProjectionCondition {
  column: string;
  equals: string;
}

export interface ProjectionDef {
  collapse?: ProjectionCollapse;
  joins?: ProjectionJoin[];
  migration: string;
  name: string;
  pool?: string;
  predicates?: ProjectionPredicate[];
  source: string;
}

export interface ProjectionFunctionCall {
  args: string[];
  function: string;
}

export interface ProjectionJoin {
  alias: string;
  kind: string;
  on: ProjectionJoinKey[];
  type: string;
}

export interface ProjectionJoinKey {
  left: string;
  right: string;
}

export interface ProjectionLiteral {
  bool?: boolean;
  number?: number;
  string?: string;
}

export interface ProjectionOrder {
  column: string;
  direction?: string;
  nulls?: string;
  rank?: string[];
}

export interface ProjectionPredicate {
  anyOf?: ProjectionPredicate[];
  args?: string[];
  column?: string;
  equals?: ProjectionLiteral;
  function?: string;
  isNull?: boolean;
  notNull?: boolean;
  optional?: boolean;
  requiredSettings?: string[];
  setting?: string;
  when?: ProjectionCondition;
}

export interface PruneReference {
  keyColumn: string;
  table: string;
  versionColumn: string;
}

export interface QueryDef {
  dialects?: string[];
  name: string;
  notNull?: string[];
  orderBy?: QueryOrderBy;
  page?: QueryPage;
  params: string;
  returns?: string;
  row: string;
  sql: string;
}

export interface QueryOrderBy {
  default?: string;
  options: { [key: string]: string };
  param: string;
}

export interface QueryPage {
  desc?: boolean;
  key: string[];
}

export interface QueueDef {
  backoff?: string;
  key?: string;
  lease?: string;
  maxBackoff?: string;
  onDuplicate?: string;
  retain?: string;
  retries?: number;
  serialize?: boolean;
}

export interface RawHeritage {
  extends?: string;
  implements?: string[];
}

export interface RawSQLDef {
  after?: string;
  dialects?: string[];
  down?: string;
  id: string;
  up: string;
}

export interface ReferenceDef {
  label?: string;
  options?: string;
  type: string;
  value?: string;
}

export interface RelationDef {
  field?: string;
  onDelete?: string;
  type?: string;
}

export interface RoleSeed {
  name: string;
  permissions: string[];
}

export interface ScalarDef {
  caseInsensitive?: boolean;
  comment?: string;
  description?: string;
  example?: string;
  fileUpload?: FileUploadConfig;
  format?: string;
  hasCustomNormalize?: boolean;
  hasCustomParse?: boolean;
  hasCustomValidate?: boolean;
  imageConstraints?: ImageConstraints;
  languagePrimitive: 'string' | 'number' | 'boolean' | 'object';
  maxLength?: number;
  maximum?: number;
  minLength?: number;
  minimum?: number;
  name: string;
  pattern?: string;
  primitive?: string;
  reservedWords?: string[];
  reservedWordsCaseInsensitive?: boolean;
  reservedWordsMatchPartial?: boolean;
  typeMappings?: { [key: string]: string };
}

export interface ScalarFile {
  caseInsensitive?: boolean;
  comment?: string;
  description?: string;
  example?: string;
  fileUpload?: FileUploadConfig;
  format?: string;
  hasCustomNormalize?: boolean;
  hasCustomParse?: boolean;
  hasCustomValidate?: boolean;
  imageConstraints?: ImageConstraints;
  kind: 'Scalar';
  languagePrimitive: 'string' | 'number' | 'boolean' | 'object';
  maxLength?: number;
  maximum?: number;
  minLength?: number;
  minimum?: number;
  name: string;
  pattern?: string;
  primitive?: string;
  reservedWords?: string[];
  reservedWordsCaseInsensitive?: boolean;
  reservedWordsMatchPartial?: boolean;
  typeMappings?: { [key: string]: string };
}

export interface ServerDecl {
  serves: ServiceRef[];
}

export interface ServiceCallers {
  from?: string[];
  mode: 'require' | 'allow';
}

export interface ServiceRef {
  kind: 'API' | 'Bucket' | 'DB' | 'General' | 'MemoryStore' | 'Site' | 'Stack';
  name: string;
}

export interface SourceRef {
  omittedFromSource?: string[];
  target: string;
  virtual?: string[];
}

export interface StackDecl {
  deploy?: ServiceRef[];
  expose?: DeployableRef[];
  origins?: Origin[];
}

export interface TraitConfigSchema {
  fields?: FieldDef[];
}

export interface TraitRef {
  configArgs?: { [key: string]: unknown };
  name: string;
}

export interface TypeDef {
  allowUnknownFields?: boolean;
  /**
   * The behaviors the type composes, in the order their checks run. A registry
   * admits the behaviors it registers: the core's, which the engine
   * implements, and its extensions'.
   */
  behaviors?: BehaviorRef[];
  comment?: string;
  database?: DatabaseDecl;
  denyUnknownFields?: boolean;
  description?: string;
  display?: TypeDisplay;
  enqueueOnChange?: EnqueueOnChange[];
  envVars?: boolean;
  environment?: EnvironmentDecl;
  extends?: string;
  extensions?: Extensions;
  fields?: FieldDef[];
  graphMember?: GraphMemberConfig;
  implements?: TraitRef[];
  indexes?: IndexDef[];
  isTrait?: boolean;
  jsonField?: boolean;
  kind?: string;
  name: string;
  optimistic?: boolean;
  owner?: string;
  projection?: ProjectionDef;
  query?: QueryDef;
  queue?: QueueDef;
  rawHeritage?: RawHeritage;
  rawSql?: RawSQLDef[];
  role: 'DBTable' | 'APIView' | 'APIInput' | 'EmbeddedStruct' | 'APIOperationSet' | 'Trait' | 'Projection' | 'Query';
  seedRoles?: RoleSeed[];
  server?: ServerDecl;
  source?: SourceRef;
  sqlName?: string;
  stack?: StackDecl;
  strictJSON?: boolean;
  traitConfig?: TraitConfigSchema;
  user?: UserTrait;
  userRole?: UserRoleTrait;
  versionGraph?: VersionGraphConfig;
  versioned?: boolean;
  versionedConfig?: VersionedConfig;
}

/**
 * What a UI reads to render the instances of a type (@display): what to call
 * one and many, which field is an instance's title, what a create button says,
 * the glyph shown beside its name, which fields summarize it in a list, and
 * the labels of its Workflow's states and transitions.
 */
export interface TypeDisplay {
  createLabel?: string;
  icon?: string;
  noun?: string;
  plural?: string;
  states?: { [key: string]: DisplayState };
  summaryFields?: string[];
  titleField?: string;
  transitions?: { [key: string]: { [key: string]: string } };
}

export interface TypeRef {
  elemNonNull?: boolean;
  isArray?: boolean;
  isArrayOfArrays?: boolean;
  isMap?: boolean;
  name: string;
}

export interface UnionDef {
  comment?: string;
  description?: string;
  name: string;
  types: string[];
}

export interface UnionFile {
  comment?: string;
  description?: string;
  kind: 'Union';
  name: string;
  types: string[];
}

export interface UserAdministrationConfig {
  path?: string;
}

export interface UserRoleTrait {
}

export interface UserSessionsConfig {
  defaultRoles?: string[];
  firstUser?: IdentityFirstUser;
  noLogin?: boolean;
  path?: string;
  register?: boolean;
  registration?: string;
}

export interface UserTrait {
  login: string;
  name?: string;
}

export interface VersionGraphConfig {
  name?: string;
  schemaEpoch?: number;
  snapshotEvery?: number;
}

export interface VersionedConfig {
  exclude?: string[];
  partitionBy?: string;
  pruneKeepReferencedBy?: PruneReference[];
  retentionDays?: number;
}

export interface Worker {
  comment?: string;
  concurrency?: number;
  grace?: string;
  name: string;
  queue: string;
}
