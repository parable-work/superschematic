// A project's status and a task's (D103), read by the schema with typeof.
// Each terminal state has its outcome, which Reactions' allTerminal and
// anyTerminal read.
export const projectFlow = {
  states: ["planned", "active", "done", "failed", "cancelled"],
  transitions: [
    { from: "planned", to: "active" },
    { from: "planned", to: "cancelled" },
    { from: "active", to: "done" },
    { from: "active", to: "failed" },
    { from: "active", to: "cancelled" },
  ],
  outcomes: { done: "success", failed: "failure", cancelled: "neutral" },
} as const;

export const taskFlow = {
  states: ["todo", "doing", "done", "failed", "dropped"],
  transitions: [
    { from: "todo", to: "doing" },
    { from: "todo", to: "dropped" },
    { from: "doing", to: "done" },
    { from: "doing", to: "failed" },
    { from: "doing", to: "dropped" },
  ],
  outcomes: { done: "success", failed: "failure", dropped: "neutral" },
} as const;
