import { Contact, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import {
  AutoGenerate,
  Recurrence,
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

// A named set of permissions. tickets.work runs "done" from a reply;
// notifications.routes edits the routes.
export abstract class Role implements UserRole {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  name: Identity.Slug;

  permissions: string[];
}

// One message per reference a deleted ticket held, which the build fills
// (D154). The notifier records it on the ticket that held it.
@queue()
export abstract class ReferenceCleared {}

// A ticket, seen by its owner and those it names. Its assignment is
// news, its passing its due time open is urgent, and a summary waits for
// the next digest.
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

// A duty done each month. Its Recurrence emits an occurrence's opening,
// reminder and miss (D152); a missed one is urgent.
@event<Duty>({ kind: "recurrence.missed", title: "title", urgency: "urgent" })
@event<Duty>({ kind: "recurrence.reminded", title: "title" })
export abstract class Duty
  implements
    Recurrence<Duty, {
      anchors: ["calendar"];
      timeZone: "UTC";
      opensBefore: "P7D";
      remindBefore: "P2D";
      overdueAfter: "P1D";
      permission: "tickets.work";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;

  title: string;

  owner: Identity.UUID;
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

// The addresses a person is reached at and replies from.
@channelIdentities()
export abstract class ChannelIdentity {}
