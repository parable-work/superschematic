import { Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable, Validate } from "@superschematic/schema";
import { AutoGenerate, HasMany, Relation, index, key, searchField, unique } from "@superschematic/db";

// Where an issue stands.
export enum IssueStatus {
  Open = "open",
  Started = "started",
  Done = "done"
}

// A team, which owns issues. Its key is unique: a second team of one key
// is a conflict.
export abstract class Team {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  key: Identity.Slug;

  name: Identity.Name;

  issues: HasMany<Issue>;
}

// A person an issue is assigned to.
export abstract class Member {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: Identity.Name;
}

// An issue of a team. Its writes record who made them, a delete keeps the
// row, deletedAt stamped, and its title and body are searched.
@index<Issue>(["team"])
export abstract class Issue {
  @key
  id: AutoGenerate<Identity.UUID>;

  team: Relation<Team, { onDelete: "RESTRICT" }>;

  @searchField
  title: Validate<string, { minLength: 1; maxLength: 200 }>;

  @searchField
  body: Nullable<string>;

  status: Default<IssueStatus, IssueStatus.Open>;

  priority: Default<Generic.Int64, 0>;

  estimate: Nullable<number>;

  dueOn: Nullable<Temporal.Date>;

  assignee: Nullable<Relation<Member, { onDelete: "SET NULL" }>>;

  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
  updatedAt: Nullable<Temporal.DateTime>;
  updatedBy: Nullable<Identity.UUID>;
  deletedAt: Nullable<Temporal.DateTime>;
  deletedBy: Nullable<Identity.UUID>;

  comments: HasMany<Comment>;
}

// A comment on an issue. A delete removes the row.
export abstract class Comment {
  @key
  id: AutoGenerate<Identity.UUID>;

  issue: Relation<Issue, { onDelete: "CASCADE" }>;

  body: Validate<string, { minLength: 1 }>;

  createdAt: Temporal.DateTime;
}
