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
   * The glyph a UI shows beside the type's name, as a field's and an
   * operation's `@icon` do: "receipt". It is a name an app's icon set
   * draws; which names exist is the app's rule.
   */
  readonly icon?: string;
  /**
   * The fields that summarize an instance in a list, in order: the type's
   * own fields, by name, or fields its behaviors add, by qualified name,
   * the behavior's name, a dot and the field's: "Workflow.status".
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

/** @display's argument on a field. Give one member. */
export interface FieldDisplayConfig {
  /**
   * A control of several lines (true) or one line (false), whatever the
   * field's maxLength. The field must hold text, or a list of text, and
   * must not be secret.
   */
  readonly multiline?: boolean;
}

/**
 * Says how a UI, or an agent, renders the type's instances. It adds no
 * field, operation or storage, and changes no generated code. The loader
 * holds the fields it names to the type and its behaviors, and its states
 * and transitions to the type's Workflow config. A type takes one.
 */
export function display(config: DisplayConfig): ClassDecorator;
/**
 * Says how a UI renders the field's control: `@display({ multiline: true })`
 * asks for one that holds several lines. It adds no storage or validation,
 * and changes no generated code. A field takes one; an operation and an
 * argument take none.
 */
export function display(config: FieldDisplayConfig): PropertyDecorator;
export function display(_config: DisplayConfig | FieldDisplayConfig): ClassDecorator & PropertyDecorator {
  return () => {};
}
