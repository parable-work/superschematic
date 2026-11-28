import { Contact, Identity } from "superscalar";
import {
  AutoGenerate,
  Comments,
  User,
  UserRole,
  audience,
  key,
  notifier,
  queue,
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

// A ticket, which its owner may keep to themselves (D156). Its
// conversation emits its mentions and its replies (#713), which the
// notifier raises into its inbox.
@audience<Ticket>({ owner: "owner" })
export abstract class Ticket implements Comments<Ticket, { events: true }> {
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

@queue()
@notifier("dispatch")
export abstract class NoticeWork {}
