import { Identity } from "superscalar";
import { Nullable, Secret } from "@superschematic/schema";
import { Authenticated, HttpMethod, rest, source, tableOperation } from "@superschematic/api";
import { envVars, providers } from "@superschematic/schema-config";
import { Ticket } from "@schemas/fixture-references-db";

// The API's config (D159): each slot @providers names is a field whose
// value, per environment, names the runtime provider that serves it, and
// the fields after it hold that provider's settings. sms-relay-b reads
// SMS_ACCOUNT too, so swapping sms-relay-a for it is the config alone.
@envVars
@providers({ SMS: "channel", VAULT: "credentials", RUNNER: "sandbox", MODEL: "model", PREVIEWS: "files" })
export abstract class FixtureProvidersConfig {
  SMS: Nullable<string>;
  SMS_URL: Nullable<string>;
  SMS_ACCOUNT: Nullable<string>;
  SMS_SENDER: Nullable<string>;
  SMS_TOKEN: Nullable<Secret<string>>;

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

// A ticket as the API answers it.
@source(Ticket)
export abstract class TicketView {
  id: Identity.UUID;
  title: string;
}

export class Tickets extends Authenticated {
  // One ticket.
  @rest(HttpMethod.GET, "tickets/{id}")
  @tableOperation()
  getTicket(id: Identity.UUID): TicketView {
    throw new Error("schema declaration only");
  }
}
