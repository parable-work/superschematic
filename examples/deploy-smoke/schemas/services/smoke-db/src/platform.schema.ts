import { Identity } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import {
  AutoGenerate,
  Recurrence,
  accessGrants,
  attachments,
  channelIdentities,
  conversations,
  event,
  key,
  notifier,
  party,
  queue,
  references,
  runRequests,
  searchable,
} from "@superschematic/db";
import { FileReleased } from "./smoke-db.schema";

// The features of plan2 the smoke deploys beside D151's behaviors, each on
// a table of its own: a recurrence whose reminder fires (D152), the
// notifier that tells its owner (D153), search across tables (D155), and
// the inbound path from a party with a grant (D157).

// A bill paid each day: its Recurrence plans its occurrences, and each
// reminds its owner twenty seconds before it is due. The remind deadline
// waits on the table's deadline queue, which the behavior worker
// BillReminders fires, and its event reaches the notifier, which routes it
// to the bill's owner (D152, D153).
@event<Bill>({ kind: "recurrence.reminded", title: "title" })
export abstract class Bill
  implements
    Recurrence<Bill, {
      anchors: ["calendar"];
      timeZone: "UTC";
      horizonDays: 2;
      remindBefore: "PT20S";
      overdueAfter: "P1D";
      permission: "smoke.write";
    }>
{
  @key id: AutoGenerate<Identity.UUID>;
  title: Validate<string, { minLength: 1; maxLength: 200 }>;
  owner: Identity.UUID;
}

// The notifier's tables, which the build fills (D153): the in-app inbox,
// the deliveries through each channel, the routes and the preferences an
// app writes as rows, each channel kind's trust, the replies, and the
// dispatch queue the worker Notify drains.
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

// The addresses a user or a party is reached at and writes from.
@channelIdentities()
export abstract class ChannelIdentity {}

// One message per row whose indexed text changed; the build fills its
// fields. The worker IndexSearch keeps the index current from it (D155).
@queue()
export abstract class SearchIndexing {}

// A memo a search finds by the words of its title and its text.
@searchable<Memo>({ title: "title", body: "text", queue: SearchIndexing })
export abstract class Memo {
  @key id: AutoGenerate<Identity.UUID>;
  title: Validate<string, { minLength: 1; maxLength: 200 }>;
  text: Nullable<string>;
}

// The grants a party reaches rows through (D156).
@accessGrants()
export abstract class Grant {}

// A payee outside the user table: a principal of kind party, which acts
// through its grants alone (D157).
@party<Payee>({ title: "name" })
export abstract class Payee {
  @key id: AutoGenerate<Identity.UUID>;
  name: Validate<string, { minLength: 1; maxLength: 200 }>;
}

// The conversations, one per party and channel, and their messages, which
// point at the rows they are about and hold the files they came with: a
// message filed onto a line item is among its backlinks, and its files
// join the line item's with no second upload (D154, D157). A file's
// object is released on FileReleased, as a line item's is.
@conversations("conversations")
export abstract class Conversation {}

@conversations("messages")
@references<Message>()
@attachments<Message>({ released: FileReleased })
export abstract class Message {}

// The run requests a text from a payee with an active grant puts on the
// queue, which the worker Runs takes (D157).
@queue()
@runRequests()
export abstract class RunRequest {}
