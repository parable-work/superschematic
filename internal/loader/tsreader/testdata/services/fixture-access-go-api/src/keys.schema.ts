import { Secret } from "@superschematic/schema";
import { envVars } from "@superschematic/schema-config";

// The keys of fixture-access-go-db's sealed fields (D85).
@envVars
export abstract class AccessKeys {
  ACCESS_GO_DB_FIELD_KEYS: Secret<string>;
}
