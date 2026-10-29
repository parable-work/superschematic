/*
Signals (D77): a message about one instance that the engine delivers to
the event streams open on it and keeps nowhere: a client's typing
indicator, who is looking, a draft being written. A behavior declares its
signals; a caller sends one of access read or write (engine.signals.send,
POST .../instances/{id}/signals/{behavior}/{name}), and a behavior's
operation one of any access through its context's signal(name, data). A
stream opened with signals receives each as an `event: signal` message
with no id (runtime/engine/README.md, "The event stream").
*/

/** One signal, as a stream delivers it. */
export interface EngineSignal {
  namespace: string;
  schema: string;
  instanceId: string;
  /** The behavior that declares it. */
  behavior: string;
  name: string;
  /** Its data, which the signal's paramsSchema accepted. */
  data: unknown;
  /** The sender's subject: the caller's, or the operation's principal's. */
  from: string;
  /** The engine's clock when it was sent, in epoch milliseconds. */
  at: number;
}

/** What a caller sends (engine.signals.send). */
export interface SendSignalInput {
  /** The instance's namespace; default when absent. */
  namespace?: string;
  schema: string;
  instanceId: string;
  /** The behavior that declares the signal. */
  behavior: string;
  name: string;
  /** At most 4096 bytes of JSON its paramsSchema accepts; {} when absent. */
  data?: unknown;
}

/** The engine's signal options (the engine option signals). */
export interface SignalOptions {
  /**
   * The signals a principal may send on one instance each second, and its
   * burst: 20 when absent. Past it a send is refused rate_limited (429).
   * An operation's signals are not limited.
   */
  perSecond?: number;
}

/** The signals a principal may send on one instance each second unless the options say. */
export const DEFAULT_SIGNALS_PER_SECOND = 20;

/** The most a signal's data may be, in bytes of JSON. */
export const MAX_SIGNAL_DATA_BYTES = 4096;

/**
 * A call of a behavior's namespace operation (D77, engine.behaviors.invoke):
 * an operation its declaration gives `scope: "namespace"`, which runs on
 * the behavior's tables across every schema of the namespace that
 * composes it.
 */
export interface NamespaceOperationInput {
  /** The namespace; default when absent. */
  namespace?: string;
  /** The behavior that declares the operation. */
  behavior: string;
  operation: string;
  /** What its paramsSchema accepts; {} when absent. */
  params?: unknown;
}
