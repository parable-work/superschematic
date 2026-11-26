import { Contact, Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import {
  AutoGenerate,
  Comments,
  Relation,
  Schedules,
  User,
  UserRole,
  accessGrants,
  channelIdentities,
  key,
  notifier,
  queue,
  searchable,
  unique
} from "@superschematic/db";

// A person who signs in.
export abstract class Account implements User<{ login: "email" }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: Contact.Email;
}

// A named set of permissions; a role a grant names lists the rows it
// reaches from the grant's target (D156). An agent's role is one of these.
export abstract class Role implements UserRole {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  name: Identity.Slug;

  permissions: string[];

  reaches: Nullable<string[]>;
}

// The grants a customer and an agent run reach rows through (D156).
@accessGrants()
export abstract class Grant {}

// One message per row whose indexed text changed (D155).
@queue()
export abstract class SearchIndexing {}

// A customer outside the user table, who reaches a task through a grant.
export abstract class Customer {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;
}

// A task an agent may work on. A schedule on it wakes an agent; a person
// and an agent discuss it in its thread; an agent may be assigned to it.
@searchable<Task>({ title: "title", body: "details", queue: SearchIndexing, comments: false })
export abstract class Task
  implements
    Schedules<Task, {
      max: 5;
      kinds: ["after", "every"];
      outcomes: ["started", "skipped"];
    }>,
    Comments<Task, {}>
{
  @key
  id: AutoGenerate<Identity.UUID>;

  title: string;

  details: Nullable<string>;

  state: string;

  assignee: Nullable<string>;

  createdAt: Temporal.DateTime;
}

// A note on a task, which a run on the task reaches through its role.
@searchable<TaskNote>({ title: "text", queue: SearchIndexing })
export abstract class TaskNote {
  @key
  id: AutoGenerate<Identity.UUID>;

  task: Relation<Task, { onDelete: "CASCADE" }>;

  text: string;
}

// An invoice of a task: paying it moves money, which an agent only asks to.
export abstract class Invoice {
  @key
  id: AutoGenerate<Identity.UUID>;

  task: Relation<Task, { onDelete: "CASCADE" }>;

  amount: Generic.Int64;

  paid: Generic.Int64;
}

// The notifier's tables (D153): it routes a schedule's run to an agent and
// an approval to those who may give it.
@notifier("inbox")
export abstract class Notice {}

@notifier("deliveries")
export abstract class Delivery {}

@notifier("routes")
export abstract class NoticeRoute {}

@notifier("preferences")
export abstract class NoticePreference {}

@notifier("channelTrust")
export abstract class ChannelTrust {}

@notifier("replies")
export abstract class Reply {}

@queue()
@notifier("dispatch")
export abstract class NoticeWork {}

@channelIdentities()
export abstract class ChannelIdentity {}
