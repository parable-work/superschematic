import { platform } from "@superschematic/platform";

@platform({
  description: "Customer-facing API and its database",
  visibility: "public",
  services: ["api", "db"],
  shared: { database: ["api", "db"] }
})
export abstract class Core {}

@platform({
  visibility: "internal",
  services: ["metrics"]
})
export abstract class Observability {}
