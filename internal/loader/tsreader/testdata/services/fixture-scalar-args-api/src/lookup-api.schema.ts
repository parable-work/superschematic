// Body arguments and responses that are scalars rather than object types,
// so an SDK names each scalar's type itself: a scalar whose name ends in a
// common word (UUID, UserID, JWT, JSON), alone and in a list.
import { Auth, Generic, Identity } from "superscalar";
import { HttpMethod, rest } from "@superschematic/api";

export class LookupMutations {
  // Scalar body arguments, one of them a list, and a list of JSON values back.
  @rest(HttpMethod.POST, "lookups")
  lookup(owner: Identity.UserID, ids: Identity.UUID[], token: Auth.JWT, note: string): Generic.JSON[] {
    throw new Error("schema declaration only");
  }
}

export class LookupQueries {
  // A scalar response.
  @rest(HttpMethod.GET, "lookups/{id}/owner")
  owner(id: Identity.UUID): Identity.UserID {
    throw new Error("schema declaration only");
  }
}
