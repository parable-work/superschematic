import { Identity } from "superscalar";
import { Authenticated, HttpMethod, rest, source, tableOperation, worker } from "@superschematic/api";
import { AudienceResolving, Page } from "@schemas/fixture-audience-queue-db";

// A page as the API answers it.
@source(Page)
export abstract class PageView {
  id: Identity.UUID;
  title: string;
}

export class Pages extends Authenticated {
  // One page, read as its caller: a page keeps to its folder's audience.
  @rest(HttpMethod.GET, "pages/{id}")
  @tableOperation()
  getPage(id: Identity.UUID): PageView {
    throw new Error("schema declaration only");
  }
}

// The pages' queue (D156, #734): the generated worker resolves each
// message's pages again, and the implementation has no method for it.
@worker({ queue: AudienceResolving })
export abstract class ResolveAudience {}
