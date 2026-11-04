import { auth, behaviorOperations } from "@superschematic/api";
import { Account } from "@schemas/fixture-agents-db";

// An account's budget and status operations (D104), for any signed-in caller:
// the core checks accounts.limits on a limit's change and accounts.observe on
// a provider's usage.
@behaviorOperations(Account)
@auth
export abstract class AccountActions {}
