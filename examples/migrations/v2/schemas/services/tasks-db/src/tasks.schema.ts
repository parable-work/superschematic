import { Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable } from "@superschematic/schema";
import { AutoGenerate, Relation, index, key, rawSql } from "@superschematic/db";

// Whether a project is still worked on.
export enum ProjectStatus {
  Active = "active",
  Archived = "archived"
}

// A list of tasks that belong together.
@rawSql({
  // The dashboard reads each project's open tasks from a materialized view,
  // which raw SQL keeps on Postgres; its unique index lets it refresh
  // concurrently.
  id: "project_open_tasks",
  dialects: ["postgres"],
  up: `
    DROP MATERIALIZED VIEW IF EXISTS project_open_tasks;
    CREATE MATERIALIZED VIEW IF NOT EXISTS project_open_tasks AS
      SELECT project_id, count(*) AS open_tasks FROM task WHERE NOT done GROUP BY project_id;
    CREATE UNIQUE INDEX IF NOT EXISTS project_open_tasks_project ON project_open_tasks (project_id)
  `,
  down: "DROP MATERIALIZED VIEW IF EXISTS project_open_tasks"
})
export abstract class Project {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: Identity.Name;

  // The column's default makes the projects that exist, and those v1's
  // servers write, active.
  status: Default<ProjectStatus, ProjectStatus.Active>;

  createdAt: Temporal.DateTime;
}

// Something to do in a project.
@index<Task>(["project", "dueAt"])
@index<Task>(["summary"], { name: "summary_lower", expression: "lower(summary)" })
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
