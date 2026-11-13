// Type-level checks of @stateMachine (D117). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { stateMachine } from "./decorators";
import type { StateMachineConfig, StateOf } from "./decorators";
import type { Default, Nullable } from "./wrappers";

enum TicketState {
  Open = "open",
  Resolved = "resolved",
  Closed = "closed",
}

enum Review {
  Retry = "RETRY",
  Approved = "APPROVED",
}

abstract class Ticket {
  state: Default<TicketState, TicketState.Open>;
  review: Nullable<Review>;
  priority: TicketState;
  title: string;
  estimate: Nullable<number>;
}

// A field's states are its enum, read through each wrapper.
const plain: StateOf<Ticket["priority"]> = TicketState.Closed;
const defaulted: StateOf<Ticket["state"]> = TicketState.Resolved;
const nullable: StateOf<Ticket["review"]> = Review.Retry;
// @ts-expect-error a state of another field's enum
const crossed: StateOf<Ticket["state"]> = Review.Retry;
// @ts-expect-error null is no state
const absent: StateOf<Ticket["review"]> = null;

// A machine on each form of field: an enum, one with a default, and a
// nullable one that starts at null and returns to it.
@stateMachine<Ticket>({
  field: "state",
  transitions: {
    [TicketState.Open]: [TicketState.Resolved, TicketState.Closed],
    [TicketState.Resolved]: [TicketState.Open, TicketState.Closed],
  },
  terminal: [TicketState.Closed],
})
@stateMachine<Ticket>({
  field: "review",
  fromNull: [Review.Retry, Review.Approved],
  transitions: { [Review.Retry]: [null, Review.Approved] },
  terminal: [Review.Approved],
})
@stateMachine<Ticket>({
  field: "priority",
  transitions: { [TicketState.Open]: [TicketState.Closed] },
  terminal: [TicketState.Closed],
})
export abstract class Declared extends Ticket {}

// The options are a type of their own, one machine per field.
const reviewed: StateMachineConfig<Ticket> = {
  field: "review",
  fromNull: [Review.Retry],
  transitions: {},
  terminal: [Review.Approved],
};

export function refused(): void {
  // @ts-expect-error a target of another field's enum
  stateMachine<Ticket>({
    field: "state",
    transitions: { [TicketState.Open]: [Review.Approved] },
    terminal: [],
  });
  stateMachine<Ticket>({
    field: "state",
    // @ts-expect-error a transition from a state of another field's enum
    transitions: { [Review.Retry]: [TicketState.Closed] },
    terminal: [],
  });
  // @ts-expect-error a terminal state of another field's enum
  stateMachine<Ticket>({
    field: "state",
    transitions: {},
    terminal: [Review.Retry],
  });
  // @ts-expect-error a null start of another field's enum
  stateMachine<Ticket>({
    field: "review",
    transitions: {},
    terminal: [],
    fromNull: [TicketState.Open],
  });
  stateMachine<Ticket>({
    // @ts-expect-error a field the class does not declare
    field: "status",
    transitions: {},
    terminal: [],
  });
  stateMachine<Ticket>({
    field: "review",
    transitions: {},
    // @ts-expect-error null starts a machine or ends a move, and is no terminal state
    terminal: [null],
  });
  stateMachine<Ticket>({
    field: "review",
    transitions: {},
    terminal: [],
    // @ts-expect-error fromNull lists states, and null is none
    fromNull: [null],
  });
  stateMachine<Ticket>({
    field: "title",
    transitions: {},
    // @ts-expect-error a text field has no states to list
    terminal: ["draft"],
  });
  stateMachine<Ticket>({
    field: "estimate",
    transitions: {},
    // @ts-expect-error a number field has no states to list
    terminal: [1],
  });
  // @ts-expect-error a machine names its field
  stateMachine<Ticket>({ transitions: {}, terminal: [] });
  // @ts-expect-error a machine lists its terminal states, none as []
  stateMachine<Ticket>({ field: "state", transitions: {} });
}
