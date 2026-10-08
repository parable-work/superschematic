import { Validate, behavior, display, docs, icon } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["todo", "implementing", "review", "done", "dropped"],
  transitions: [
    { from: "todo", to: "implementing" },
    { from: "implementing", to: "review" },
    { from: "review", to: "implementing" },
    { from: "review", to: "done", permission: "tickets.accept" },
    { from: "todo", to: "dropped" },
  ],
  outcomes: { dropped: "neutral" },
})
@behavior("Comments")
@display({
  noun: "Ticket",
  plural: "Tickets",
  titleField: "title",
  createLabel: "New ticket",
  summaryFields: ["Workflow.status", "assignee"],
  states: {
    todo: { label: "To do", tone: "muted" },
    implementing: { label: "Implement", activeForm: "Implementing", tone: "active" },
    review: { label: "Review", activeForm: "In review", tone: "warning" },
    done: { label: "Done", tone: "success" },
    dropped: { label: "Dropped", tone: "danger" },
  },
  transitions: {
    todo: { implementing: "Start", dropped: "Drop" },
    implementing: { review: "Send to review" },
    review: { implementing: "Request changes", done: "Accept" },
  },
})
export abstract class Ticket {
  @docs({ title: "Title" })
  @icon("text")
  title: Validate<string, { maxLength: 200 }>;

  @docs({ title: "Assignee" })
  @icon("person")
  assignee?: string;

  body?: string;
}
