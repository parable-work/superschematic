import { Secret } from "@superschematic/schema";
import { envVars } from "@superschematic/schema-config";

// The class that declares the database's keys.
@envVars
export abstract class FieldKeys {
  FINANCES_DB_FIELD_KEYS: Secret<string>;
}
