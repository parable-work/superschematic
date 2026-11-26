import { Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Mergeable, Relation, enqueueOnChange, index, key, queue } from "@superschematic/db";

// An account, merged when two records are one customer (D147). A queue's
// message holds an account's key as a value, never a relation to its
// table, so a merge repoints the tables that reference the account and
// leaves every message as it was written.
export abstract class Account implements Mergeable {
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
}

// Sent when an account is to be synced for a reason: one waits per account
// and reason, the two fields its key names (D80), and a later ask while it
// waits adds nothing.
@queue({ key: ["accountId", "reason"], onDuplicate: "skip" })
export abstract class AccountSynced {
  accountId: Identity.UUID;
  reason: string;
}

// Sent when an account's contacts on a channel change: a newer change
// replaces the one that waits, and a worker holds one claim per account
// and channel.
@queue({ key: ["accountId", "channel"], onDuplicate: "replace", serialize: true })
export abstract class ContactChanged {
  accountId: Identity.UUID;
  channel: string;
  contactId: Nullable<Identity.UUID>;
}

// A contact of an account on a channel. Whatever writes it enqueues its
// account's ContactChanged: a new contact, a deleted one, and a move to
// another account or channel, which a merge's repoint is.
@enqueueOnChange<ContactChanged, Contact>(ContactChanged, {
  on: ["insert", "update", "delete"],
  fields: ["account", "channel"],
  message: { accountId: "account", channel: "channel", contactId: "id" },
})
export abstract class Contact {
  @key
  id: AutoGenerate<Identity.UUID>;
  account: Relation<Account>;
  channel: string;
}

// Sent when an account joins or leaves a team: one waits per account and
// team, and a later change while it waits adds nothing.
@queue({ key: ["accountId", "team"], onDuplicate: "skip" })
export abstract class MembershipChanged {
  accountId: Identity.UUID;
  team: string;
}

// An account's place on a team, once per team: a merge of two accounts on
// one team deletes the place of the account merged away, and that delete
// enqueues as a move to another account does.
@index<Membership>(["account", "team"], { unique: true })
@enqueueOnChange<MembershipChanged, Membership>(MembershipChanged, {
  on: ["update", "delete"],
  fields: ["account"],
  message: { accountId: "account", team: "team" },
})
export abstract class Membership {
  @key
  id: AutoGenerate<Identity.UUID>;
  account: Relation<Account>;
  team: string;
}
