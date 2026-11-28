import { Contact as ContactScalars, Identity, Temporal } from "superscalar";
import { Default, Nullable } from "@superschematic/schema";
import {
  AutoGenerate,
  Relation,
  User,
  UserRole,
  accessGrants,
  attachments,
  channelIdentities,
  conversations,
  key,
  notifier,
  party,
  queue,
  references,
  runRequests,
  seedRows,
  unique
} from "@superschematic/db";

// One message per reference a target's delete cleared (D154).
@queue()
export abstract class ReferenceCleared {}

// One message per file whose object may be released (D154).
@queue()
export abstract class AttachmentReleased {}

// A person who signs in.
export abstract class Account implements User<{ login: "email" }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: ContactScalars.Email;
}

// A named set of permissions, and the rows a grant of it reaches from its
// target (D156). The roles are seed rows, which the migrations write
// (#625): a contractor reaches the job its grant names and writes reports
// on it; the office may message parties and settle triage.
@seedRows<Role>([
  { name: "contractor", permissions: ["reports.write"], reaches: ["Report:job"] },
  { name: "office", permissions: ["parties.message", "parties.triage"], reaches: null },
])
export abstract class Role implements UserRole {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  name: Identity.Slug;

  permissions: string[];

  reaches: Nullable<string[]>;
}

// The grants a party reaches rows through (D156).
@accessGrants()
export abstract class Grant {}

// Someone outside the organisation: a principal of kind party, which acts
// through its grants alone (D157).
@party<Contact>({ title: "name" })
export abstract class Contact {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;
}

// A job a contact may be granted.
export abstract class Job {
  @key
  id: AutoGenerate<Identity.UUID>;

  title: string;

  status: Default<string, "open">;
}

// A report on a job, which a party's run writes, with the photos it came
// with.
@attachments<Report>({ released: AttachmentReleased })
export abstract class Report {
  @key
  id: AutoGenerate<Identity.UUID>;

  job: Relation<Job, { onDelete: "CASCADE" }>;

  body: string;

  createdAt: Default<Temporal.DateTime, "now">;
}

// The conversations, one per party and channel, and their messages, which
// hold files and point at the rows they are about (D157).
@conversations("conversations")
export abstract class Conversation {}

@conversations("messages")
@references<Message>({ cleared: ReferenceCleared })
@attachments<Message>({ released: AttachmentReleased })
export abstract class Message {}

// The run requests an inbound message from a party with a grant makes.
@queue()
@runRequests()
export abstract class RunRequest {}

// The notifier's tables (D153), which a user's reply runs through.
@notifier("inbox")
export abstract class Notice {}

@notifier("deliveries")
export abstract class Delivery {}

@notifier("routes")
export abstract class NoticeRoute {}

// A user's reply by text may only acknowledge, since a sender id can be
// spoofed; by email it may run what its sender may (#625).
@seedRows<ChannelTrust>([
  { channelKind: "sms", level: "low", permissions: [] },
  { channelKind: "email", level: "standard", permissions: null },
])
@notifier("channelTrust")
export abstract class ChannelTrust {}

@notifier("replies")
export abstract class Reply {}

@queue()
@notifier("dispatch")
export abstract class NoticeWork {}

// The addresses a principal, a user or a party, is reached at and writes
// from.
@channelIdentities()
export abstract class ChannelIdentity {}
