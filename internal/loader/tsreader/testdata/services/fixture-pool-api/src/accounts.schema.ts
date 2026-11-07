import { auth, behaviorOperations, behaviorWorker } from "@superschematic/api";
import { Account } from "@schemas/fixture-pool-db";

// An account's pool and budget operations (D104), for any signed-in caller:
// pick, a read of the table as a whole at account/pool/pick, for anyone the
// route admits; markOut and reportRoom for callers holding accounts.pool,
// and observe for those holding accounts.observe.
@behaviorOperations(Account)
@auth
export abstract class AccountActions {}

// Runs Account's rechecks as they fall due and hands each recheck event to
// the project's Workers method accountRechecksPoolRecheck, which asks the
// account's provider and reports with reportRoom (D104). The worker
// fixture-pool-api-account-rechecks.
@behaviorWorker(Account, { permissions: ["accounts.pool"] })
export abstract class AccountRechecks {}
