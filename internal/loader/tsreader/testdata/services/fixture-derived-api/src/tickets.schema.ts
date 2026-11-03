import { HttpMethod, deriver, rest } from "@superschematic/api";

export abstract class TicketView {
  id: string;
}

export class TicketQueries {
  @rest(HttpMethod.GET, "tickets/{id}")
  getTicket(id: string): TicketView {
    throw new Error("schema declaration only");
  }
}

// Triages each ticket, eight answers at a time.
@deriver({ schemas: ["Ticket"], concurrency: 8, batchSize: 20 })
export abstract class TriageTickets {}

// Fills every derived field of the database.
@deriver()
export abstract class FillAll {}
