/**
 * What a state means to a reader, which a UI maps onto its own colors:
 * `muted` for a state nothing happens in (a draft, a backlog), `active`
 * for one work is under way in, `success` and `danger` for one that went
 * well or badly, `warning` for one that needs attention.
 */
export type DisplayTone = "muted" | "active" | "success" | "warning" | "danger";

/** How a UI shows one Workflow state. Give at least one member. */
export interface DisplayState {
  /** The state's label: "In review". */
  readonly label?: string;
  /** The present-progressive form a UI shows while an instance is in the state: "Implementing". */
  readonly activeForm?: string;
  readonly tone?: DisplayTone;
}

/** @display's argument. Give at least one member. */
export interface DisplayConfig {
  /** What to call one instance: "Ticket". */
  readonly noun?: string;
  /** What to call several: "Tickets". */
  readonly plural?: string;
  /**
   * The field whose value is an instance's title: one of the type's own
   * fields, holding a single text value (a string, or a scalar whose
   * values are strings), neither secret nor uiHidden.
   */
  readonly titleField?: string;
  /** What a button that creates an instance says: "New ticket". */
  readonly createLabel?: string;
  /**
   * The fields that summarize an instance in a list, in order: the type's
   * own fields, or fields its behaviors add, such as Workflow's status.
   */
  readonly summaryFields?: readonly string[];
  /** Labels of states of the type's Workflow, by state. */
  readonly states?: Readonly<Record<string, DisplayState>>;
  /**
   * Labels of transitions of the type's Workflow, by the state a
   * transition leaves, then by the state it enters:
   * `{ todo: { doing: "Start" } }`.
   */
  readonly transitions?: Readonly<Record<string, Readonly<Record<string, string>>>>;
}

/**
 * Says how a UI, or an agent, renders the type's instances. It adds no
 * field, operation or storage, and changes no generated code. The loader
 * holds the fields it names to the type and its behaviors, and its states
 * and transitions to the type's Workflow config. A type takes one.
 */
export function display(_config: DisplayConfig): ClassDecorator {
  return () => {};
}
