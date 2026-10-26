/*
@superschematic/engine/testing: behaviors written in TypeScript, for tests
(D58). A shipped engine's behaviors are Rust crates: the core's, the work
queue's and an extension's own, which a project's engine crate builds into
the addon it loads (runtime/engine/README.md, "Behaviors"). A test that
wants a behavior of its own, a fixture or a stand-in, defines it here and
passes it to openEngine's behaviors, or registers it with
engine.behaviors.register: the engine's test addon runs it through its
bridge, on the JavaScript thread, inside the call's transaction, as the
Rust engine runs its own (scripts/engine-napi.sh --js-behaviors builds the
test addon; the addon a release ships refuses an implementation object).
What a behavior's context reaches, and when each of its functions runs,
is src/testing/behavior.ts's.
*/

export { BehaviorConfigError } from '../errors.js';
export { MAX_BATCH_READ, MAX_CALL_DEPTH, MIN_SCHEDULE_MS, RELATION_COLUMNS, defineBehavior } from './behavior.js';
export type {
  AnyBehaviorImplementation,
  AttributionEntry,
  BehaviorFilter,
  BehaviorGuidance,
  BehaviorImplementation,
  BehaviorMigration,
  BehaviorReactions,
  BehaviorSchedule,
  BehaviorScope,
  ColumnSpec,
  Columns,
  ConfigBehaviorField,
  ConfigChange,
  ConfigSchema,
  ConfigSchemas,
  ConfigTarget,
  ConfigType,
  ConfigTypeField,
  ConfigTypes,
  CreateInstanceOptions,
  DescribeTarget,
  DescribedTypeOperation,
  FieldAttribution,
  FieldReader,
  FrozenJSON,
  GuardAnswer,
  GuardRequest,
  GuidanceError,
  InstanceChange,
  InstanceContext,
  InstanceSchemaForm,
  InstanceView,
  Instances,
  InstancesInvokeOptions,
  OperationContext,
  OperationGuidance,
  NamespaceContext,
  NamespaceOperationHandler,
  NamespaceSql,
  OperationHandler,
  PublishContext,
  ReactionContext,
  ReadOptions,
  Reference,
  ReferenceContext,
  ReferenceHears,
  ReferenceReader,
  References,
  ScheduleContext,
  SchemaContext,
  SchemaOperationHandler,
  Schemas,
  SqlReader,
  SqlWriter,
  StoredInstance,
  StowedObject,
  TableReader,
  TableWriter,
  TypeCheck,
  TypeSchema,
  UpdateOptions,
  ValidationContext,
  ValidationRequest,
  ValueReader,
  ValueWriter,
  WorkContext,
  WritableColumns,
} from './behavior.js';
export { page, pageRequest } from './paging.js';
