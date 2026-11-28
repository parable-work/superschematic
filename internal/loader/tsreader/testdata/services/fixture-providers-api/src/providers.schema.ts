import { Identity } from "superscalar";
import { Nullable, Secret } from "@superschematic/schema";
import { Authenticated, HttpMethod, requirePermission, rest, source, tableOperation } from "@superschematic/api";
import { envVars, providers } from "@superschematic/schema-config";
import { Delivery } from "@schemas/fixture-providers-db";

// The API's config (D159): a slot per runtime interface, each a field
// whose value names the provider in each environment, and the fields
// after it the settings of the providers that may fill it.
@envVars
@providers({ SMS: "channel", VAULT: "credentials", RUNNER: "sandbox", MODEL: "model", PREVIEWS: "files" })
export abstract class ProvidersApiConfig {
  SMS: Nullable<string>;
  SMS_URL: Nullable<string>;
  SMS_ACCOUNT: Nullable<string>;
  SMS_SENDER: Nullable<string>;
  SMS_TOKEN: Nullable<Secret<string>>;
  // The key sms-relay-a calls the app with, to read its deliveries (#744).
  SMS_CALLER_KEY: Nullable<Secret<string>>;

  VAULT: Nullable<string>;
  VAULT_URL: Nullable<string>;
  VAULT_VAULTS: Nullable<string>;
  VAULT_TOKEN: Nullable<Secret<string>>;

  RUNNER: Nullable<string>;
  RUNNER_WORKDIR: Nullable<string>;
  MODEL: Nullable<string>;
  MODEL_MODEL: Nullable<string>;

  PREVIEWS: Nullable<string>;
  PREVIEWS_MAX_CHARS: Nullable<string>;
}

// A delivery as the API answers it.
@source(Delivery)
export abstract class DeliveryView {
  id: Identity.UUID;
  recipient: string;
  body: string;
  provider: string;
  providerMessageId: string;
}

export class Deliveries extends Authenticated {
  // A delivery, which a relay holding messages.relay reads.
  @rest(HttpMethod.GET, "deliveries/{id}")
  @requirePermission(["messages.relay"])
  @tableOperation()
  getDelivery(id: Identity.UUID): DeliveryView {
    throw new Error("schema declaration only");
  }
}
