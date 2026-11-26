import { Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, key } from "@superschematic/db";

// A note.
export abstract class Note {
  @key
  id: AutoGenerate<Identity.UUID>;

  title: string;

  body: string;
}

// A notice about a note to one recipient, which the SMS slot's channel
// adapter sends. Notifications (D153) will raise these from events; the
// example's build-time check counts this table as using a channel.
export abstract class Notice {
  @key
  id: AutoGenerate<Identity.UUID>;

  noteId: Identity.UUID;

  recipient: string;

  body: string;

  sentAt: Nullable<Temporal.DateTime>;
}
