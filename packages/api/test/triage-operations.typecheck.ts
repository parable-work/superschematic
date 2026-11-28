// Type test, checked by `bun run typecheck` (tsconfig.test.json) and never
// run: @triageOperations takes a class, the party table, and its options,
// and goes on a class with no members, whose @auth, @requirePermission,
// service clause and rate limit apply to every route the set serves
// (#746).
import { rateLimit, requirePermission, triageOperations } from "@superschematic/api";

declare abstract class Contact {
  id: string;
}

declare abstract class ContactView {
  id: string;
  name: string;
}

@triageOperations(Contact)
export abstract class Triage {}

@triageOperations(Contact, { refersTo: ContactView })
@requirePermission(["parties.triage"])
@rateLimit({ requestsPerMinute: 60 })
export abstract class StaffTriage {}

// @ts-expect-error the party table is a class, not its name
@triageOperations("Contact")
export abstract class NamedByString {}

// @ts-expect-error refersTo is a class
@triageOperations(Contact, { refersTo: "ContactView" })
export abstract class RefersToByString {}

// @ts-expect-error channels is no option of the set
@triageOperations(Contact, { channels: ["sms"] })
export abstract class UnknownOption {}

export class Methods {
  // @ts-expect-error a class decorator, not a method's
  @triageOperations(Contact)
  triage(): void {}
}
