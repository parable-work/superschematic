import { Identity } from "superscalar";
import { Nullable, Secret } from "@superschematic/schema";
import { Authenticated, HttpMethod, rest, source, tableOperation } from "@superschematic/api";
import { envVars, providers } from "@superschematic/schema-config";
import { Note } from "@notes/notes-db";

// The API's config (D159). Each slot @providers names is a field whose
// value, per environment, names the runtime provider that serves it, and
// the fields after it hold that provider's settings: SMS_URL and SMS_TOKEN
// for the slot SMS. sms-relay-b reads SMS_ACCOUNT too, so swapping
// sms-relay-a for it is the environment's config alone. A secret setting
// is a Secret<string>, which `stack secrets set` enters per environment.
@envVars
@providers({ SMS: "channel", VAULT: "credentials", RUNNER: "sandbox", MODEL: "model", PREVIEWS: "files" })
export abstract class NotesApiConfig {
  // The channel adapter notices go out through: sms-relay-a or sms-relay-b.
  SMS: Nullable<string>;
  SMS_URL: Nullable<string>;
  SMS_ACCOUNT: Nullable<string>;
  SMS_SENDER: Nullable<string>;
  SMS_TOKEN: Nullable<Secret<string>>;

  // The credential broker a run's steps sign in with.
  VAULT: Nullable<string>;
  VAULT_URL: Nullable<string>;
  VAULT_VAULTS: Nullable<string>;
  VAULT_TOKEN: Nullable<Secret<string>>;

  // Where an agent run's instance runs, and its model.
  RUNNER: Nullable<string>;
  RUNNER_WORKDIR: Nullable<string>;
  MODEL: Nullable<string>;
  MODEL_MODEL: Nullable<string>;

  // What extracts a stored file's text and makes its preview.
  PREVIEWS: Nullable<string>;
  PREVIEWS_MAX_CHARS: Nullable<string>;
}

// A note as the API answers it.
@source(Note)
export abstract class NoteView {
  id: Identity.UUID;
  title: string;
}

export class Notes extends Authenticated {
  // One note.
  @rest(HttpMethod.GET, "notes/{id}")
  @tableOperation()
  getNote(id: Identity.UUID): NoteView {
    throw new Error("schema declaration only");
  }
}
