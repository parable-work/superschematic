import { platform } from "@superschematic/platform";

// "cache" is shared by a service that is not a member.
@platform({
  visibility: "public",
  services: ["api"],
  shared: { cache: ["api", "worker"] }
})
export abstract class Core {}
