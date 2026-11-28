// Type test, checked by `bun run typecheck` (tsconfig.test.json) and never
// run: @rowFeatureOperations takes a class, the table, and its options,
// and goes on a class with no members, whose @auth, @requirePermission,
// @publicRoute, service clause and rate limit apply to every route the set
// serves (#723).
import { auth, publicRoute, rateLimit, requirePermission, rowFeatureOperations } from "@superschematic/api";

declare abstract class Issue {
  id: string;
}

@rowFeatureOperations(Issue)
@auth
export abstract class IssueLinks {}

@rowFeatureOperations(Issue, {
  operations: ["references", "addReference", "backlinks", "beginUpload", "completeUpload"],
  permissions: { beginUpload: ["issues.upload"] },
  bucket: "issue-files"
})
@requirePermission(["issues.read"])
@rateLimit({ requestsPerMinute: 60 })
export abstract class NarrowedIssueLinks {}

@rowFeatureOperations(Issue, { operations: ["backlinks"] })
@publicRoute
export abstract class PublicIssueLinks {}

// @ts-expect-error the table is a class, not its name
@rowFeatureOperations("Issue")
export abstract class NamedByString {}

// @ts-expect-error release is no operation of the set
@rowFeatureOperations(Issue, { operations: ["release"] })
export abstract class UnknownOperation {}

// @ts-expect-error a permission list is per operation of the set
@rowFeatureOperations(Issue, { permissions: { release: ["issues.write"] } })
export abstract class UnknownPermission {}

export class Methods {
  // @ts-expect-error a class decorator, not a method's
  @rowFeatureOperations(Issue)
  backlinks(): void {}
}
