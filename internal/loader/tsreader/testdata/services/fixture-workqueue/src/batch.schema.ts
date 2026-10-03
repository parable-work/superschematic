import { behavior } from "@superschematic/schema";

@behavior("Blueprint", {
  schema: "steps",
  parentLink: "batch",
  keyField: "step",
  steps: {
    fetch: {},
    check: { after: ["fetch"], when: { field: "topic", equals: "search" } },
    index: { after: ["check"], data: { title: "Index what was fetched" } },
  },
  copyFields: ["topic"],
})
export abstract class Batch {
  title: string;
  topic?: string;
}
