import { Identity } from "superscalar";
import { Secret } from "@superschematic/schema";
import { envVars } from "@superschematic/schema-config";
import { HttpMethod, rest } from "@superschematic/api";

// The documented Encrypted<T> pair (guides/database-tables): the class that
// declares the key finances-db's config names, and the API's config, which
// extends it. The config is the @envVars class no other extends, so every
// loader, the stack and the handle read FinancesConfig, whose fields are
// FieldKeys' and its own (D94).
@envVars
export abstract class FieldKeys {
  FINANCES_DB_FIELD_KEYS: Secret<string>;
}

@envVars
export abstract class FinancesConfig extends FieldKeys {
  PLAID_CLIENT_ID: Secret<string>;
  EMBEDDING_MODEL: string;
}

export abstract class ItemView {
  id: Identity.UUID;
  itemId: string;
}

export class Items {
  @rest(HttpMethod.GET, "items/{id}")
  getItem(id: Identity.UUID): ItemView {
    throw new Error("schema declaration only");
  }
}
