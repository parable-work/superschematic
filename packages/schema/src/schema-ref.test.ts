import { describe, expect, test } from "bun:test";

import { schemaOf } from "./schema-ref";

describe("schemaOf", () => {
  test("records a class reference as a schema value", () => {
    abstract class GitlabGroupMember {}

    expect(schemaOf(GitlabGroupMember)).toEqual({ schemaRef: "GitlabGroupMember" });
  });
});
