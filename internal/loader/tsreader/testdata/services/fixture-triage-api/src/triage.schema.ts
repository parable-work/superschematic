import { Identity } from "superscalar";
import { HttpMethod, rest, requirePermission, triageOperations } from "@superschematic/api";
import { display } from "@superschematic/schema";
import { Contact } from "@schemas/fixture-parties-db";

// A contact as the staff see one, named by its name.
@display({ noun: "Contact", plural: "Contacts", titleField: "name" })
export abstract class ContactView {
  id: Identity.UUID;

  name: string;
}

export class ContactQueries {
  @requirePermission(["contacts.read"])
  @rest(HttpMethod.GET, "contacts")
  listContacts(): ContactView[] {
    throw new Error("schema declaration only");
  }
}

// The inbound path's triage as routes (#746): the held messages, one of
// them, settling one onto a contact and dismissing one, each for a caller
// who holds parties.triage, the settle's contact picked from listContacts.
@triageOperations(Contact, { refersTo: ContactView })
@requirePermission(["parties.triage"])
export abstract class Triage {}
