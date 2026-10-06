import { Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Relation, key } from "@superschematic/db";

// A list of tasks that belong together.
export abstract class Project {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: Identity.Name;

  createdAt: Temporal.DateTime;
}

// Something to do in a project.
export abstract class Task {
  @key
  id: AutoGenerate<Identity.UUID>;

  project: Relation<Project, { onDelete: "CASCADE" }>;

  title: string;

  done: boolean;

  dueAt: Nullable<Temporal.DateTime>;

  // How many minutes before dueAt to remind the assignee, kept as text.
  remindBefore: string[];

  createdAt: Temporal.DateTime;
}

// A comment on a task.
export abstract class Comment {
  @key
  id: AutoGenerate<Identity.UUID>;

  task: Relation<Task, { onDelete: "CASCADE" }>;

  body: string;

  // The comment's id in the tracker it was imported from.
  legacyId: string;

  createdAt: Temporal.DateTime;
}
