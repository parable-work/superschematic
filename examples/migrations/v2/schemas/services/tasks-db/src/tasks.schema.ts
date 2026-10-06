import { Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Relation, index, key } from "@superschematic/db";

// A list of tasks that belong together.
export abstract class Project {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: Identity.Name;

  createdAt: Temporal.DateTime;
}

// Something to do in a project.
@index<Task>(["project", "dueAt"])
export abstract class Task {
  @key
  id: AutoGenerate<Identity.UUID>;

  project: Relation<Project, { onDelete: "CASCADE" }>;

  // Was title.
  summary: string;

  done: boolean;

  dueAt: Nullable<Temporal.DateTime>;

  // How many minutes before dueAt to remind the assignee, now as numbers.
  remindBefore: Generic.Int64[];

  createdAt: Temporal.DateTime;
}

// A comment on a task.
export abstract class Comment {
  @key
  id: AutoGenerate<Identity.UUID>;

  task: Relation<Task, { onDelete: "CASCADE" }>;

  body: string;

  // legacyId is gone: the import is over.

  // The users the comment mentions. Existing comments mention no one.
  mentions: string[];

  createdAt: Temporal.DateTime;
}
