import { Secret } from "@superschematic/schema";
import { envVars } from "@superschematic/schema-config";

// The API's settings, which do not extend FieldKeys: the API has two
// @envVars classes and neither is its config, so the load fails (D94).
@envVars
export abstract class FinancesConfig {
  PLAID_CLIENT_ID: Secret<string>;
  EMBEDDING_MODEL: string;
}
