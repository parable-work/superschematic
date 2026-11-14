import { Identity, Temporal } from "superscalar";
import { Default, Nullable, jsonField, stateMachine } from "@superschematic/schema";
import { AutoGenerate, key } from "@superschematic/db";
import { AccountStatus } from "@schemas/fixture-state-machine-enums";

// Where a ticket stands. A member's value is not its name: the database
// holds the value, and a language's types name the member.
export enum TicketState {
  Open = "open",
  InProgress = "in_progress",
  Resolved = "resolved",
  Closed = "closed"
}

// What a review of a ticket's answer found. Each member's value is its
// name.
export enum ReviewOutcome {
  RETRY = "RETRY",
  APPROVED = "APPROVED",
  REJECTED = "REJECTED"
}

// Whether a ticket was raised to a manager, and heard.
export enum Escalation {
  Raised = "raised",
  Acknowledged = "acknowledged"
}

// A step of a refund's sign-off.
export enum SignOffStep {
  Requested = "requested",
  Granted = "granted",
  Refused = "refused"
}

// The sign-off a ticket's refund carries, kept as JSON. It is no table, so
// the database guards nothing of it, and the types still say which step
// follows which.
@stateMachine<SignOff>({
  field: "step",
  transitions: {
    [SignOffStep.Requested]: [SignOffStep.Granted, SignOffStep.Refused]
  },
  terminal: [SignOffStep.Granted, SignOffStep.Refused]
})
@jsonField
export abstract class SignOff {
  step: Default<SignOffStep, SignOffStep.Requested>;

  note: Nullable<string>;
}

// A support ticket. Its state starts open, may be reopened until it is
// closed, and lists a move from open to open, which is no move. Its review
// is null until a pass reviews the answer, and a retry clears it for the
// next pass. Its escalation is null until it is raised, and once
// acknowledged only returns to null. Its account's status is an enum of
// another service.
@stateMachine<Ticket>({
  field: "state",
  transitions: {
    [TicketState.Resolved]: [TicketState.Open, TicketState.Closed],
    [TicketState.Open]: [TicketState.Open, TicketState.InProgress, TicketState.Resolved, TicketState.Closed],
    [TicketState.InProgress]: [TicketState.Resolved, TicketState.Open]
  },
  terminal: [TicketState.Closed]
})
@stateMachine<Ticket>({
  field: "review",
  fromNull: [ReviewOutcome.RETRY, ReviewOutcome.APPROVED, ReviewOutcome.REJECTED],
  transitions: {
    [ReviewOutcome.RETRY]: [null, ReviewOutcome.APPROVED, ReviewOutcome.REJECTED]
  },
  terminal: [ReviewOutcome.APPROVED, ReviewOutcome.REJECTED]
})
@stateMachine<Ticket>({
  field: "escalation",
  fromNull: [Escalation.Raised],
  transitions: {
    [Escalation.Raised]: [Escalation.Acknowledged],
    [Escalation.Acknowledged]: [null]
  },
  terminal: []
})
@stateMachine<Ticket>({
  field: "accountStatus",
  transitions: {
    [AccountStatus.Active]: [AccountStatus.Suspended, AccountStatus.Closed],
    [AccountStatus.Suspended]: [AccountStatus.Active, AccountStatus.Closed]
  },
  terminal: [AccountStatus.Closed]
})
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;

  subject: string;

  state: Default<TicketState, TicketState.Open>;

  review: Nullable<ReviewOutcome>;

  escalation: Nullable<Escalation>;

  accountStatus: Default<AccountStatus, AccountStatus.Active>;

  signOff: Nullable<SignOff>;

  updatedAt: Temporal.DateTime;
}
