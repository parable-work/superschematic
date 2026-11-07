import { Generic, Identity } from "superscalar";
import { AutoGenerate, Budget, Pool, key } from "@superschematic/db";

// Whether an account is a person's account at a vendor or a paid key.
export enum AccountKind {
  Account = "account",
  Key = "key"
}

// An account a person's agents run their turns on (D104): their accounts at
// two vendors, and paid keys. A turn picks the same vendor's open accounts,
// then its near ones, then the others by rank, a key last; an account a
// provider refuses is out until the time it names, thirty minutes when it
// names none, and comes back only when a check of its usage made after that
// time finds room, rechecked every two minutes until one does. Its tokens
// are metered by the hour, and an account with none left counts as out
// until the hour rolls over, or until the provider's own figure lapses.
export abstract class Account
  implements
    Pool<Account, {
      order: "rank";
      outForMs: 1800000;
      recheckAfterMs: 120000;
      permission: "accounts.pool";
    }>,
    Budget<Account, {
      meters: { tokens: { limit: 1000; reset: "hourly" } };
      observePermission: "accounts.observe";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  owner: Identity.UUID;
  name: string;
  vendor: string;
  kind: AccountKind;
  rank: Generic.Int64;
}
