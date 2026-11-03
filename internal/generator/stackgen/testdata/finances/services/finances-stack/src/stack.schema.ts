import { FinancesApi } from "@schemas/finances-api";
import { environment, stack } from "@superschematic/stack";

// finances-api on stacktest's fake target. Its env sets a field FieldKeys
// does not declare, EMBEDDING_MODEL, which FinancesConfig, the API's config,
// does (D94); FINANCES_DB_FIELD_KEYS and PLAID_CLIENT_ID are secrets.
@stack({ deploy: [FinancesApi] })
export abstract class Finances {}

@environment({
  target: "fake",
  fake: { project: "finances-staging", region: "us-east1" },
  settings: [{ of: FinancesApi, env: { EMBEDDING_MODEL: "voyage-4-large" } }]
})
export abstract class Staging {}
