import { Identity } from "superscalar";
import { AutoGenerate, Recurrence, attachments, key, queue, references } from "@superschematic/db";

// One message per reference a target's delete cleared, which the build
// fills.
@queue()
export abstract class ReferenceCleared {}

// One message per file row deleted, or upload never completed, which the
// build fills.
@queue()
export abstract class AttachmentReleased {}

// A filing that recurs (D152), whose occurrences' events a behavior worker
// hands to the project's Workers (D104), and which points at rows of any
// table and holds files (D154): the database's ORM, and each transaction
// it opens, then holds references and attachments.
@references<Filing>({ cleared: ReferenceCleared })
@attachments<Filing>({ released: AttachmentReleased })
export abstract class Filing
  implements
    Recurrence<Filing, {
      anchors: ["calendar"];
      timeZone: "Europe/London";
      opensBefore: "P14D";
      remindBefore: "P3D";
      overdueAfter: "P1D";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}
