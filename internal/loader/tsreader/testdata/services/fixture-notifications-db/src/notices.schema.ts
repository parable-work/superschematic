import { Contact, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import {
  AutoGenerate,
  User,
  UserRole,
  audience,
  channelIdentities,
  event,
  key,
  notifier,
  queue,
  references,
  unique
} from "@superschematic/db";

// A person who signs in.
export abstract class Account implements User<{ login: "email" }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: Contact.Email;
}

// A named set of permissions.
export abstract class Role implements UserRole {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  name: Identity.Slug;

  permissions: string[];
}

// One message per reference a target's delete cleared, which the build
// fills (D154); the notifier raises reference.cleared on the holder.
@queue()
export abstract class ReferenceCleared {}

// A ticket, which its owner may keep to themselves. Its watchers hear of
// it; an overdue one is urgent.
@audience<Ticket>({ owner: "owner" })
@references<Ticket>({ cleared: ReferenceCleared })
@event<Ticket>({ kind: "ticket.assigned", payload: ["title", "status"], title: "title" })
@event<Ticket>({ kind: "ticket.overdue", payload: ["title"], title: "title", urgency: "urgent", description: "A ticket passed its due time open." })
@event<Ticket>({ kind: "ticket.summary", title: "title", urgency: "digest" })
@event<Ticket>({ kind: "reference.cleared", title: "title", urgency: "record" })
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;

  title: string;

  status: string;

  owner: Identity.UUID;

  watchers: Nullable<Identity.UUID[]>;

  dueAt: Nullable<Temporal.DateTime>;
}

// An approval someone asked for, in the shape of D158's approvals: whoever
// holds its permission hears of it and decides it by a reply (D153's
// approve and deny).
@event<Approval>({ kind: "approval.requested", title: "summary", urgency: "urgent" })
export abstract class Approval {
  @key
  id: AutoGenerate<Identity.UUID>;

  operation: string;

  summary: string;

  permission: string;

  state: string;

  decidedBy: Nullable<string>;

  decidedVia: Nullable<string>;

  decidedAt: Nullable<Temporal.DateTime>;
}

// The notifier's tables, which the build fills (D153).
@notifier("inbox")
export abstract class Notice {}

@notifier("deliveries")
export abstract class Delivery {}

@notifier("routes")
export abstract class NoticeRoute {}

@notifier("preferences")
export abstract class NoticePreference {}

@notifier("subscriptions")
export abstract class Subscription {}

@notifier("channelTrust")
export abstract class ChannelTrust {}

@notifier("replies")
export abstract class Reply {}

@queue()
@notifier("dispatch")
export abstract class NoticeWork {}

// The addresses a principal is reached at, and that its replies come from.
@channelIdentities()
export abstract class ChannelIdentity {}
