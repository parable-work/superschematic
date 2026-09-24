import { Identity } from "superscalar";
import { HttpMethod, docs, icon, mcp, rest } from "@superschematic/api";

export abstract class Return {
  id: Identity.UUID;
  orderId: Identity.UUID;
  approved: boolean;
}

export abstract class OpenReturnInput {
  orderId: Identity.UUID;
}

// confirm is acme's invocation policy key (ext/mcp.go). tsc accepts it
// because tsconfig.json includes the augmentation in @acme/schema; the
// acme registry accepts it, validates the value and gives a tool without it
// "never".
export class ReturnMutations {
  @docs({
    title: "Open a return",
    description: "Opens a return for one order.",
    capability: "returns.open",
    lifecycle: "active",
    visibility: "public",
    audience: "shoppers"
  })
  @icon("box")
  @mcp({ handle: "open_return" })
  @rest(HttpMethod.POST, "returns")
  openReturn(input: OpenReturnInput): Return {
    throw new Error("schema declaration only");
  }

  @docs({
    title: "Approve a return",
    description: "Approves an open return and refunds the order.",
    capability: "returns.approve",
    lifecycle: "active",
    visibility: "internal",
    audience: "staff"
  })
  @icon("tag")
  @mcp({ handle: "approve_return", confirm: "always" })
  @rest(HttpMethod.POST, "returns/{id}/approve")
  approveReturn(id: Identity.UUID): Return {
    throw new Error("schema declaration only");
  }
}
