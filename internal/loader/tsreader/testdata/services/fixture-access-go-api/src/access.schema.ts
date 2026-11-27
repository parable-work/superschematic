import { Identity } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import {
  Authenticated,
  HttpMethod,
  SearchQuery,
  decrypted,
  paginated,
  requirePermission,
  rest,
  source,
  tableOperation
} from "@superschematic/api";
import { Contract, Document, Folder, Request, Unit } from "@schemas/fixture-access-go-db";

// fixture-access-api's operations as a Go server serves them (D180). Its
// views leave out audienceNamed, a list, which the Go server's table
// operations do not write yet (D106): a named audience is set through the
// ORM.

// A folder, with who may see it.
@source(Folder)
export abstract class FolderView {
  id: Identity.UUID;
  name: string;
  owner: Identity.UUID;
  audience: string;
}

// What a caller sets to make a folder: it owns it.
@source(Folder)
export abstract class CreateFolderInput {
  name: string;
  owner: Identity.UUID;
  audience: Nullable<string>;
}

// What a caller may change of a folder: its name and who may see it.
@source(Folder)
export abstract class UpdateFolderInput {
  name: Nullable<string>;
  audience: Nullable<string>;
}

// A document, with who may see it and what an agent run may do with it.
@source(Document)
export abstract class DocumentView {
  id: Identity.UUID;
  folderId: Nullable<Identity.UUID>;
  title: string;
  audience: string;
  agentAccess: string;
  createdBy: Identity.UUID;
}

// What a caller sets to write a document.
@source(Document)
export abstract class CreateDocumentInput {
  folderId: Nullable<Identity.UUID>;
  title: string;
  audience: Nullable<string>;
  agentAccess: Nullable<string>;
}

// What a caller may change of a document.
@source(Document)
export abstract class UpdateDocumentInput {
  title: Nullable<string>;
  audience: Nullable<string>;
  agentAccess: Nullable<string>;
}

// A contract, its terms opened only for a caller who may reveal them.
@source(Contract)
export abstract class ContractView {
  id: Identity.UUID;
  name: string;
  status: string;
  @decrypted({ permission: "contracts.reveal" })
  terms: Nullable<string>;
}

// A unit of a contract.
@source(Unit)
export abstract class UnitView {
  id: Identity.UUID;
  name: string;
  contractId: Identity.UUID;
}

// A request about a unit.
@source(Request)
export abstract class RequestView {
  id: Identity.UUID;
  unitId: Identity.UUID;
  title: string;
}

// What a caller sets to make a request of the unit the path names.
@source(Request)
export abstract class CreateRequestInput {
  title: Validate<string, { minLength: 1 }>;
}

export class Folders extends Authenticated {
  // The folders the caller may see.
  @rest(HttpMethod.GET, "folders")
  @paginated()
  @tableOperation()
  listFolders(): FolderView[] {
    throw new Error("schema declaration only");
  }

  // A new folder.
  @rest(HttpMethod.POST, "folders")
  @tableOperation()
  createFolder(input: CreateFolderInput): FolderView {
    throw new Error("schema declaration only");
  }

  // Changes a folder's name or who may see it: its owner alone.
  @rest(HttpMethod.PATCH, "folders/{id}")
  @tableOperation({ by: ["owner"] })
  updateFolder(id: Identity.UUID, input: UpdateFolderInput): FolderView {
    throw new Error("schema declaration only");
  }
}

export class Documents extends Authenticated {
  // The documents the caller may see, searched by their titles.
  @rest(HttpMethod.GET, "documents")
  @paginated()
  @tableOperation()
  listDocuments(q: SearchQuery<string>): DocumentView[] {
    throw new Error("schema declaration only");
  }

  // One document.
  @rest(HttpMethod.GET, "documents/{id}")
  @tableOperation()
  getDocument(id: Identity.UUID): DocumentView {
    throw new Error("schema declaration only");
  }

  // A new document.
  @rest(HttpMethod.POST, "documents")
  @tableOperation()
  createDocument(input: CreateDocumentInput): DocumentView {
    throw new Error("schema declaration only");
  }

  // Changes a document.
  @rest(HttpMethod.PATCH, "documents/{id}")
  @tableOperation()
  updateDocument(id: Identity.UUID, input: UpdateDocumentInput): DocumentView {
    throw new Error("schema declaration only");
  }
}

export class Contracts extends Authenticated {
  // One contract.
  @rest(HttpMethod.GET, "contracts/{id}")
  @requirePermission(["contracts.read"])
  @tableOperation()
  getContract(id: Identity.UUID): ContractView {
    throw new Error("schema declaration only");
  }

  // The units the caller reaches.
  @rest(HttpMethod.GET, "units")
  @requirePermission(["requests.read"])
  @paginated()
  @tableOperation()
  listUnits(): UnitView[] {
    throw new Error("schema declaration only");
  }
}

export class Requests extends Authenticated {
  // The requests about the units of the contracts the caller is a party
  // to: a row rule through three relations (D108).
  @rest(HttpMethod.GET, "my/requests")
  @requirePermission(["requests.read"])
  @paginated()
  @tableOperation({ by: ["unit.contract.parties.party"], overridePermission: "requests.admin" })
  listMyRequests(): RequestView[] {
    throw new Error("schema declaration only");
  }

  // The requests the caller reaches.
  @rest(HttpMethod.GET, "requests")
  @requirePermission(["requests.read"])
  @paginated()
  @tableOperation()
  listRequests(): RequestView[] {
    throw new Error("schema declaration only");
  }

  // One request.
  @rest(HttpMethod.GET, "requests/{id}")
  @requirePermission(["requests.read"])
  @tableOperation()
  getRequest(id: Identity.UUID): RequestView {
    throw new Error("schema declaration only");
  }

  // A new request about the unit the path names.
  @rest(HttpMethod.POST, "units/{unitId}/requests")
  @requirePermission(["requests.write"])
  @tableOperation()
  createRequest(unitId: Identity.UUID, input: CreateRequestInput): RequestView {
    throw new Error("schema declaration only");
  }
}
