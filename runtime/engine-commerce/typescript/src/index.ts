/*
@superschematic/engine-commerce: commerce for @superschematic/engine
(D70). The core declares the commerce behaviors (internal/registry/behaviors
in the repository), so every binary's meta-schema admits a schema that
composes them, but an engine runs them only once a deployment names them:
pass commerceBehaviors, or the names of the ones it uses, as the engine's
behaviors option. They are Rust (runtime/engine-commerce/rust, D58), which
the engine's addon is built with and registers by name. This package holds
their names, their configs' and records' types and their constants.
*/

export { DEFAULT_MAX_ITEMS } from './types.js';
export type {
  CurrencySource,
  FieldRef,
  HoldCheck,
  HoldConfig,
  HoldItem,
  HoldsConfig,
  HoldsRecord,
  HoldState,
  InstanceRef,
  PaymentAction,
  PaymentActionKind,
  PaymentAttempt,
  PaymentAttemptStatus,
  PaymentEventKind,
  PaymentRequirement,
  PaymentsConfig,
  PaymentsRecord,
  PaymentsState,
  StockChange,
  StockConfig,
  StockMovement,
  StockMovementKind,
  StockRecord,
} from './types.js';

/** The name of each behavior this package ships. */
export type CommerceBehavior = 'Stock' | 'Calendar' | 'Holds' | 'Payments';

/**
 * Every behavior this package ships, by name, in the order a deployment
 * registers them: the engine's behaviors option takes them as they are
 * (`openEngine({ ..., behaviors: commerceBehaviors })`).
 */
export const commerceBehaviors: readonly CommerceBehavior[] = Object.freeze(['Stock', 'Calendar', 'Holds', 'Payments']);
