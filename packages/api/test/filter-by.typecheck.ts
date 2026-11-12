// Type test, checked by `bun run typecheck` (tsconfig.test.json) and never
// run: FilterBy names the item type's own fields and is optional, and
// @tableOperation takes no argument, or its row rule (D106, D108).
import { HttpMethod, paginated, rest, tableOperation } from "@superschematic/api";
import type { FilterBy } from "@superschematic/api";

type UUID = string & { readonly __brand: "Identity.UUID" };

export abstract class IssueView {
  id: UUID;
  assigneeId: UUID | null;
  priority: number;
  title: string;
}

export class Issues {
  @rest(HttpMethod.GET, "issues")
  @paginated()
  @tableOperation()
  listIssues(filter: FilterBy<IssueView, ["assigneeId", "priority"]>): IssueView[] {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "issues/by-label")
  // @ts-expect-error a field that is not one of the item's
  listByLabel(filter: FilterBy<IssueView, ["label"]>): IssueView[] {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "issues/{id}")
  // @ts-expect-error @tableOperation takes its row rule, not a kind
  @tableOperation("read")
  getIssue(id: UUID): IssueView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.PATCH, "issues/{id}")
  @tableOperation({ by: ["assigneeId"], overridePermission: "issues.admin" })
  updateIssue(id: UUID): IssueView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.DELETE, "issues/{id}")
  // @ts-expect-error a row rule names its members in by
  @tableOperation({ overridePermission: "issues.admin" })
  deleteIssue(id: UUID): IssueView {
    throw new Error("schema declaration only");
  }
}

// A filter may be left out.
export const none: FilterBy<IssueView, ["assigneeId", "priority"]> = null;
