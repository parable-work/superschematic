// A project's status and a task's (D103), read by the schema with typeof.
// Each terminal state has its outcome, which Reactions' allTerminal and
// anyTerminal read. Done is no terminal state: a task that is done may be
// reopened, and a project that is done taken up again, so each names done's
// outcome in finished (D113), and done still counts as finished.
export const projectFlow = {
  states: ["planned", "active", "done", "failed", "cancelled"],
  transitions: [
    { from: "planned", to: "active" },
    { from: "planned", to: "cancelled" },
    { from: "active", to: "done" },
    { from: "active", to: "failed" },
    { from: "active", to: "cancelled" },
    { from: "done", to: "active" },
  ],
  outcomes: { failed: "failure", cancelled: "neutral" },
  finished: { done: "success" },
} as const;

export const taskFlow = {
  states: ["todo", "doing", "done", "failed", "dropped"],
  transitions: [
    { from: "todo", to: "doing" },
    { from: "todo", to: "dropped" },
    { from: "doing", to: "done" },
    { from: "doing", to: "failed" },
    { from: "doing", to: "dropped" },
    { from: "done", to: "todo" },
  ],
  outcomes: { failed: "failure", dropped: "neutral" },
  finished: { done: "success" },
} as const;
